import Foundation

/// A small app-level LRU of ``SessionViewModel``s, so re-opening a recently
/// viewed session shows its transcript instantly and resumes the stream from
/// the cached cursor instead of re-downloading and re-laying-out the first
/// page (task 0404).
///
/// Lifetime rules — a cached model streams only while a screen presents it:
///
/// - A session screen takes a ``SessionPresentation`` when it appears and
///   ends it when it really leaves (a pop, or the router replacing the stack).
///   A view pushed *on top* of the transcript (a diff) or a sheet keeps the
///   presentation, so the stream stays open and returning costs nothing.
/// - When a model's last presentation ends it is ``SessionViewModel/park()``ed:
///   the stream stops (no leaked subscriptions) but the projection, cursor and
///   optimistic state are kept. The next ``SessionViewModel/start(reopen:)``
///   resumes from the cursor (see ``SessionViewModel/defaultResumeWindow``).
/// - Least-recently-used models beyond ``capacity`` are stopped and dropped,
///   never one that is currently presented.
/// - ``removeAll()`` (called by ``AppDataCache/clear()`` on every connection,
///   profile or credential change) stops and drops everything, so one daemon's
///   session can never be shown or streamed for another.
@MainActor
public final class SessionModelCache {
    public struct Key: Hashable, Sendable {
        public let project: String
        public let sessionID: String
    }

    private struct Entry {
        let key: Key
        /// Identity of the transport the model was built on. The cache holds
        /// the model, which holds the client, so the identity cannot be reused
        /// by another object while the entry exists.
        let owner: ObjectIdentifier
        let model: SessionViewModel
    }

    public nonisolated static let defaultCapacity = 5
    public let capacity: Int
    /// Least recently used first.
    private var entries: [Entry] = []

    public init(capacity: Int = SessionModelCache.defaultCapacity) {
        self.capacity = max(1, capacity)
    }

    public var count: Int { entries.count }

    /// Cached session keys, least recently used first (diagnostics/tests).
    public var keys: [Key] { entries.map(\.key) }

    /// The cached model for this session on this transport, if any. Pure: no
    /// recency bump, eviction or mode change, so it is safe from a SwiftUI
    /// view's `init`, which may run (and be discarded) on every parent update.
    /// Pair it with ``activate(_:project:sessionID:live:owner:)`` when the
    /// screen appears.
    public func lookup(project: String, sessionID: String, owner: AnyObject) -> SessionViewModel? {
        let key = Key(project: project, sessionID: sessionID)
        let ownerID = ObjectIdentifier(owner)
        return entries.first(where: { $0.key == key && $0.owner == ownerID })?.model
    }

    /// A screen showing `model` appeared: make exactly this instance the cached
    /// one for its session (replacing and stopping any other, unless another
    /// screen presents it), mark it most recently used, evict overflow, and —
    /// if the caller's listing says `live` — let a parked persisted model
    /// adopt live mode before it resumes. Call before
    /// ``SessionViewModel/present(reopen:)``.
    ///
    /// `live` is only a hint: a parked model that was persisted but is listed
    /// live adopts live mode so the resume streams; the reverse is left to the
    /// stream/revalidation, which falls back to persisted when the daemon no
    /// longer has the session.
    public func activate(
        _ model: SessionViewModel, project: String, sessionID: String, live: Bool, owner: AnyObject
    ) {
        let key = Key(project: project, sessionID: sessionID)
        let ownerID = ObjectIdentifier(owner)
        if let index = entries.firstIndex(where: { $0.key == key }) {
            let entry = entries.remove(at: index)
            // Another transport's model never streams on; a same-transport
            // duplicate keeps streaming only while some screen still shows it.
            if entry.model !== model, entry.owner != ownerID || !entry.model.isPresented {
                entry.model.stop()
            }
        }
        entries.append(Entry(key: key, owner: ownerID, model: model))
        if live { model.adoptListedLive() }
        evictOverflow()
    }

    /// ``lookup(project:sessionID:owner:)`` or `make()`, then
    /// ``activate(_:project:sessionID:live:owner:)`` — for callers that are not
    /// a view `init`.
    public func model(
        project: String,
        sessionID: String,
        live: Bool,
        owner: AnyObject,
        make: () -> SessionViewModel
    ) -> SessionViewModel {
        let resolved = lookup(project: project, sessionID: sessionID, owner: owner) ?? make()
        activate(resolved, project: project, sessionID: sessionID, live: live, owner: owner)
        return resolved
    }

    /// The cached model for a key, without touching recency (tests).
    public func cachedModel(project: String, sessionID: String) -> SessionViewModel? {
        let key = Key(project: project, sessionID: sessionID)
        return entries.first(where: { $0.key == key })?.model
    }

    /// Stop every cached model and forget them all.
    public func removeAll() {
        let dropped = entries
        entries.removeAll()
        for entry in dropped { entry.model.stop() }
    }

    private func evictOverflow() {
        while entries.count > capacity {
            // Never the entry just touched (last), never a presented model.
            guard let index = entries.dropLast().firstIndex(where: { !$0.model.isPresented })
            else { return }
            let evicted = entries.remove(at: index)
            evicted.model.stop()
        }
    }
}

/// One screen's claim on a ``SessionViewModel``. Create it when the screen
/// first appears (never in a SwiftUI view `init`, which may be discarded),
/// ``begin()`` on every appearance and ``end()`` when the screen really leaves.
/// Dropping the object ends it too, which covers a screen that was covered by
/// a pushed view and then removed without another `onDisappear`.
@MainActor
public final class SessionPresentation {
    /// Main-actor state reachable from the nonisolated `deinit`.
    private final class State: @unchecked Sendable {
        weak var model: SessionViewModel?
        var token: UInt64?
    }

    private let state = State()

    public init(model: SessionViewModel) {
        state.model = model
    }

    public var isActive: Bool { state.token != nil }

    /// Idempotent: a screen re-appearing (e.g. back from a diff) keeps its claim.
    public func begin() {
        guard state.token == nil, let model = state.model else { return }
        state.token = model.beginPresentation()
    }

    /// Idempotent. Parks the model if this was its last presentation.
    public func end() {
        guard let token = state.token else { return }
        state.token = nil
        state.model?.endPresentation(token)
    }

    deinit {
        let state = self.state
        Task { @MainActor in
            guard let token = state.token else { return }
            state.token = nil
            state.model?.endPresentation(token)
        }
    }
}
