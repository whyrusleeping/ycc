import Foundation
import YccProto

/// Private client usage analytics (docs/design/usage-analytics.md): which
/// screens are visited and for how long, which actions are used and how, and
/// which requests fail — reported to the daemon, never to a third party.
///
/// Events carry only identifiers from a fixed vocabulary (screen names, action
/// ids, input methods, error codes) — never titles, prompts, paths, or ids.
///
/// Recording is cheap and synchronous (a lock and an append); delivery is
/// batched and best-effort: every ``flushInterval`` while in the foreground,
/// when ``flushThreshold`` events are buffered, and on backgrounding. A failed
/// send drops the batch silently. Thread-safe: transport interceptors record
/// from URLSession queues while views record from the main actor.
public final class UsageAnalytics: @unchecked Sendable {
    public static let shared = UsageAnalytics()

    /// How an action was invoked.
    public enum Via: String, Sendable {
        case tap, swipe, menu, gesture, keyboard, link, auto, pull
        case contextMenu = "context_menu"
    }

    public typealias Sender = @Sendable (Ycc_V1_RecordUiEventsRequest) async -> Void

    public static let flushInterval: TimeInterval = 30
    public static let flushThreshold = 100
    /// Events kept while no sender is configured (e.g. before connecting).
    static let bufferCap = 500

    private let lock = NSLock()
    private let uptime: @Sendable () -> TimeInterval
    private let wallMS: @Sendable () -> Int64
    private let clientVersion: String

    private var sender: Sender?
    private var buffer: [Ycc_V1_UiEvent] = []
    private var visitID = UsageAnalytics.newVisitID()
    private var catalog: [Ycc_V1_UiCatalogEntry] = []
    private var catalogPending = true
    private var foreground = false
    private var timer: Task<Void, Never>?
    private var flushing = false
    private var flows: [String: Bool] = [:] // open flow -> submitted

    // View tracking: the navigation stack's top screen with presented
    // surfaces (sheets, drawer) layered over it; the topmost is "current".
    private var screen = "home"
    private var overlays: [String] = []
    private var current: String?
    private var currentFrom: String?
    private var currentSince: TimeInterval = 0

    public init(
        clientVersion: String = UsageAnalytics.bundleVersion(),
        uptime: @escaping @Sendable () -> TimeInterval = { ProcessInfo.processInfo.systemUptime },
        wallMS: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) }
    ) {
        self.clientVersion = UsageAnalytics.token(clientVersion, max: 64)
        self.uptime = uptime
        self.wallMS = wallMS
    }

    // MARK: Configuration

    /// Route batches through a daemon connection, or stop sending (`nil`).
    public func setSender(_ sender: Sender?) {
        lock.lock()
        self.sender = sender
        if sender != nil { catalogPending = true }
        lock.unlock()
    }

    /// Everything this client could record, so the report can list what was
    /// never used. Sent with the first batch of each visit.
    public func setCatalog(views: [String], actions: [String]) {
        var entries: [Ycc_V1_UiCatalogEntry] = []
        for (kind, names) in [("view", views), ("action", actions)] {
            for name in names {
                var entry = Ycc_V1_UiCatalogEntry()
                entry.kind = kind
                entry.name = UsageAnalytics.token(name, max: 80)
                entries.append(entry)
            }
        }
        lock.lock()
        catalog = entries
        catalogPending = true
        lock.unlock()
    }

    // MARK: Lifecycle

    /// The app came to the foreground: a new visit begins and the current
    /// screen's dwell clock restarts.
    public func enterForeground(attrs: [String: String] = [:]) {
        lock.lock()
        guard !foreground else { lock.unlock(); return }
        foreground = true
        visitID = UsageAnalytics.newVisitID()
        catalogPending = true
        appendLocked(kind: "visit", name: "start", attrs: attrs)
        currentSince = uptime()
        if current == nil { current = overlays.last ?? screen }
        lock.unlock()
        startTimer()
    }

    /// The app is leaving the foreground: close the current screen's dwell and
    /// send what is buffered. Callers should hold a background-task assertion
    /// across the await so the send can finish.
    public func enterBackground() async {
        lock.withLock {
            if foreground {
                closeCurrentLocked()
                foreground = false
            }
            timer?.cancel()
            timer = nil
        }
        await flush()
    }

    // MARK: Views

    /// The navigation stack's top screen changed (`home` at the root).
    public func setScreen(_ name: String) {
        lock.lock()
        screen = UsageAnalytics.token(name, max: 80)
        updateCurrentLocked()
        lock.unlock()
    }

    /// A sheet/drawer-like surface appeared over the current screen.
    public func present(_ name: String) {
        lock.lock()
        overlays.append(UsageAnalytics.token(name, max: 80))
        updateCurrentLocked()
        lock.unlock()
    }

    /// A presented surface went away.
    public func dismiss(_ name: String) {
        let name = UsageAnalytics.token(name, max: 80)
        lock.lock()
        if let index = overlays.lastIndex(of: name) {
            overlays.remove(at: index)
            updateCurrentLocked()
        }
        lock.unlock()
    }

    /// The view the user is looking at right now.
    public var currentView: String {
        lock.lock()
        defer { lock.unlock() }
        return overlays.last ?? screen
    }

    // MARK: Flows

    /// A form-like surface opened (new session, quick capture, …): records
    /// `<name>.open` and tracks it as a view. Pair with ``submitFlow(_:)`` on
    /// success; ``endFlow(_:)`` then records `<name>.cancel` only if the flow
    /// was abandoned.
    public func beginFlow(_ name: String, via: Via = .tap) {
        let name = UsageAnalytics.token(name, max: 72)
        lock.lock()
        flows[name] = false
        lock.unlock()
        present(name)
        action("\(name).open", via: via)
    }

    /// The flow completed (records `<name>.submit` once).
    public func submitFlow(_ name: String, via: Via = .tap) {
        let name = UsageAnalytics.token(name, max: 72)
        lock.lock()
        let open = flows[name] == false
        if open { flows[name] = true }
        lock.unlock()
        if open { action("\(name).submit", via: via) }
    }

    /// The flow's surface went away; unsubmitted means abandoned.
    public func endFlow(_ name: String) {
        let name = UsageAnalytics.token(name, max: 72)
        lock.lock()
        let submitted = flows.removeValue(forKey: name)
        lock.unlock()
        if submitted == false { action("\(name).cancel", via: .auto) }
        dismiss(name)
    }

    // MARK: Actions and errors

    /// The user did something. `name` is `<area>.<verb>`; flows record
    /// `<flow>.open` and then `<flow>.submit` or `<flow>.cancel`.
    public func action(_ name: String, via: Via = .tap, attrs: [String: String] = [:]) {
        lock.lock()
        appendLocked(kind: "action", name: name, view: overlays.last ?? screen, via: via.rawValue, attrs: attrs)
        lock.unlock()
        flushIfFull()
    }

    /// A user-visible failure of operation `name` with a short code.
    public func error(_ name: String, code: String) {
        lock.lock()
        appendLocked(kind: "error", name: name, view: overlays.last ?? screen, attrs: ["code": code])
        lock.unlock()
        flushIfFull()
    }

    /// A failed unary RPC, from the transport layer. Cancellations are the
    /// client's own doing and are ignored, as are the analytics RPC's own
    /// failures (which would otherwise feed themselves).
    func rpcFailed(procedure path: String, code: String) {
        guard code != "ok", code != "canceled", !path.hasSuffix("/RecordUiEvents") else { return }
        let method = path.split(separator: "/").last.map(String.init) ?? path
        error("rpc.\(method)", code: code)
    }

    // MARK: Delivery

    /// Send the buffer (and the catalog when due) now. Concurrent calls
    /// coalesce; a failed send drops the batch.
    public func flush() async {
        guard let (sender, request) = takeBatch() else { return }
        await sender(request)
        lock.withLock { flushing = false }
    }

    /// Claim the buffered batch for sending, or nil when there is nothing to
    /// send, no sender, or a send already in flight.
    private func takeBatch() -> (Sender, Ycc_V1_RecordUiEventsRequest)? {
        lock.lock()
        defer { lock.unlock() }
        guard let sender, !flushing, !buffer.isEmpty || (catalogPending && !catalog.isEmpty) else {
            return nil
        }
        flushing = true
        var request = Ycc_V1_RecordUiEventsRequest()
        request.client = "ios"
        request.clientVersion = clientVersion
        request.visitID = visitID
        request.events = buffer
        buffer.removeAll()
        if catalogPending {
            request.catalog = catalog
            catalogPending = false
        }
        return (sender, request)
    }

    /// Snapshot of buffered events (tests).
    var pendingEvents: [Ycc_V1_UiEvent] {
        lock.lock()
        defer { lock.unlock() }
        return buffer
    }

    // MARK: Internals

    private func updateCurrentLocked() {
        let next = overlays.last ?? screen
        guard next != current else { return }
        let previous = current
        if foreground { closeCurrentLocked() }
        currentFrom = previous
        current = next
        currentSince = uptime()
    }

    /// Emit the current view with its dwell so far.
    private func closeCurrentLocked() {
        guard let current else { return }
        let dwell = max(0, uptime() - currentSince)
        var attrs: [String: String] = [:]
        if let currentFrom { attrs["from"] = currentFrom }
        appendLocked(kind: "view", name: current, durationMS: Int64(dwell * 1000), attrs: attrs)
        currentSince = uptime()
    }

    private func appendLocked(kind: String, name: String, view: String = "", via: String = "",
                              durationMS: Int64 = 0, attrs: [String: String] = [:]) {
        var event = Ycc_V1_UiEvent()
        event.timeMs = wallMS()
        event.kind = kind
        event.name = UsageAnalytics.token(name, max: 80)
        event.view = view.isEmpty ? "" : UsageAnalytics.token(view, max: 80)
        event.via = via
        event.durationMs = durationMS
        for (key, value) in attrs.sorted(by: { $0.key < $1.key }).prefix(8) {
            event.attrs[UsageAnalytics.token(key, max: 32)] = UsageAnalytics.token(value, max: 64)
        }
        buffer.append(event)
        if sender == nil, buffer.count > Self.bufferCap {
            buffer.removeFirst(buffer.count - Self.bufferCap)
        }
    }

    private func flushIfFull() {
        lock.lock()
        let full = buffer.count >= Self.flushThreshold && sender != nil
        lock.unlock()
        if full { Task { await flush() } }
    }

    private func startTimer() {
        let task = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: UInt64(Self.flushInterval * 1_000_000_000))
                guard !Task.isCancelled, let self else { return }
                await self.flush()
            }
        }
        lock.lock()
        timer?.cancel()
        timer = task
        lock.unlock()
    }

    /// Coerce to the daemon's identifier charset (`[A-Za-z0-9_.:/-]`, bounded),
    /// so a programming slip degrades the name rather than losing the event.
    static func token(_ value: String, max: Int) -> String {
        var out = String.UnicodeScalarView()
        for scalar in value.unicodeScalars {
            if out.count >= max { break }
            switch scalar {
            case "a"..."z", "A"..."Z", "0"..."9", "_", ".", ":", "/", "-":
                out.append(scalar)
            default:
                out.append("_")
            }
        }
        return out.isEmpty ? "unknown" : String(out)
    }

    static func newVisitID() -> String {
        String(UUID().uuidString.replacingOccurrences(of: "-", with: "").prefix(16)).lowercased()
    }

    public static func bundleVersion() -> String {
        let info = Bundle.main.infoDictionary
        let short = info?["CFBundleShortVersionString"] as? String ?? ""
        let build = info?["CFBundleVersion"] as? String ?? ""
        switch (short.isEmpty, build.isEmpty) {
        case (false, false): return "\(short)-\(build)"
        case (false, true): return short
        case (true, false): return build
        default: return "dev"
        }
    }
}
