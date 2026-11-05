import Foundation
import Observation
import YccProto

/// Where a ``SessionReadStore`` sends read marks: the daemon's shared, per-project
/// read state. ``YccClient`` is the production conformer.
public protocol SessionReadSync: Sendable {
    /// Advance read marks for sessions of one project, returning the resulting
    /// mark of every session the daemon accepted.
    func markSessionsRead(
        project: String, marks: [Ycc_V1_SessionReadMark]
    ) async throws -> [Ycc_V1_SessionReadMark]
}

extension YccClient: SessionReadSync {}

/// Tracks which sessions have **agent activity the user has not looked at yet**,
/// so the session list can flag "there are new messages in here" the way a mail
/// inbox does.
///
/// The daemon holds one read mark per session, shared by every client: each
/// summary reports it as `readThrough` (an explicit mark, else the project's
/// read baseline; empty when the project has no read state yet). This store
/// combines that with a durable local copy of the marks this device made:
///
/// - **Effective mark = the later of the local and the daemon mark.** Marking
///   read updates the local mark at once (so the badge clears immediately) and
///   pushes it to the daemon; a push that failed, or a mark this device kept
///   before read state was shared, is re-pushed when a later list shows the
///   daemon behind.
/// - **Listing never marks.** A session with no mark at all is not unread, and
///   seeing a row in a list neither creates nor advances a mark.
/// - **Daemon clocks only.** Marks are event / summary timestamps produced by the
///   daemon, never the phone's clock, so a skewed device clock cannot make a
///   just-read session look unread (or vice versa).
///
/// Local marks are persisted in `UserDefaults` (a UI convenience, not a secret)
/// and capped, evicting the least recently active sessions first.
@MainActor
@Observable
public final class SessionReadStore {
    /// sessionID → RFC3339 timestamp of the newest event this device marked read.
    private var marks: [String: String] = [:]
    /// sessionID → the daemon's shared mark, from the latest list or push reply.
    private var serverMarks: [String: String] = [:]
    /// Bumped whenever any mark changes. Observers that cache unread-derived
    /// state — the drawer's per-project badges — key their cache on this
    /// instead of re-evaluating every row on every render.
    public private(set) var revision = 0

    /// The active connection's daemon. `nil` keeps marks device-local until one
    /// is set; unpushed marks are sent once a later list shows them.
    @ObservationIgnored public var sync: SessionReadSync?
    /// sessionID → the project it was listed under; marks are pushed per project.
    @ObservationIgnored private var projects: [String: String] = [:]
    /// sessionID → stamp of a push still awaiting its reply, so overlapping
    /// refreshes do not resend it.
    @ObservationIgnored private var inFlight: [String: String] = [:]
    @ObservationIgnored private var pushTasks: [Int: Task<Void, Never>] = [:]
    @ObservationIgnored private var nextPushID = 0

    /// Backing store, or `nil` for a memory-only store (tests, previews, and any
    /// model constructed without the app's shared store).
    @ObservationIgnored private let defaults: UserDefaults?
    @ObservationIgnored private let key: String
    /// Upper bound on retained marks. A daemon's history is unbounded; the marks
    /// for sessions that have long fallen off the feed are worthless.
    @ObservationIgnored private let limit: Int

    public init(
        defaults: UserDefaults? = .standard,
        key: String = "ycc.sessionReadMarks",
        limit: Int = 600,
        sync: SessionReadSync? = nil
    ) {
        self.defaults = defaults
        self.key = key
        self.limit = limit
        self.sync = sync
        if let stored = defaults?.dictionary(forKey: key) as? [String: String] {
            marks = stored
        }
        // The per-device "first list is read" watermark is superseded by the
        // daemon's per-project read baseline.
        defaults?.removeObject(forKey: key + ".watermark")
    }

    /// A memory-only store: nothing is persisted, shared or pushed. Useful for
    /// previews and for models that are not the app's live session list.
    public static func ephemeral() -> SessionReadStore { SessionReadStore(defaults: nil) }

    // MARK: - Queries

    /// Whether a session has agent activity newer than its effective read mark.
    /// A session with no mark (local or daemon) is *not* unread.
    ///
    /// A session that is still **running** is never unread: the row already
    /// announces itself as live, its log grows every few seconds, and badging
    /// that would make the indicator meaningless noise. It goes unread the
    /// moment it stops producing (idle / paused / error / stopped) — which is
    /// precisely the "the agent finished while you were away" case this exists
    /// for.
    public func isUnread(_ session: Ycc_V1_SessionSummary) -> Bool {
        if session.live, SessionStatusKind(status: session.status) == .running { return false }
        // Idle but still waiting on delegated work: it has not finished yet; the
        // post-wake idle report is the "finished while you were away" moment.
        if session.live && session.awaitingJobs { return false }
        let id = session.sessionID
        guard let mark = Self.latest(marks[id], serverMarks[id], session.readThrough),
              let markMillis = Self.millis(mark),
              let activity = SessionListModel.recencyDate(session)
        else { return false }
        return Self.millis(activity) > markMillis
    }

    /// The effective read mark for a session (a daemon RFC3339 stamp): the later
    /// of this device's mark and the daemon's, or nil when neither exists.
    public func mark(for sessionID: String) -> String? {
        Self.latest(marks[sessionID], serverMarks[sessionID])
    }

    /// The row an unread session should open at: the agent's newest reply
    /// (model message or final report), provided it landed after `mark`.
    /// Nil when there is no such reply, so the caller keeps its default
    /// live-edge position.
    public static func unreadReplyRowID(
        in rows: some BidirectionalCollection<TranscriptRow>, after mark: String
    ) -> String? {
        guard let markDate = SessionListModel.parseTimestamp(mark) else { return nil }
        guard let reply = rows.last(where: { row in
            switch row.kind {
            case .modelMessage, .finalReport: return true
            default: return false
            }
        }) else { return nil }
        guard let replyDate = SessionListModel.parseTimestamp(reply.ts),
              replyDate > markDate else { return nil }
        return reply.id
    }

    /// How many of `sessions` carry unread agent activity.
    public func unreadCount(in sessions: [Ycc_V1_SessionSummary]) -> Int {
        sessions.reduce(into: 0) { count, session in
            if isUnread(session) { count += 1 }
        }
    }

    // MARK: - Daemon state

    /// Record the daemon's marks from freshly listed summaries, keyed by the
    /// project each list was loaded for. Re-pushes any local mark the daemon is
    /// behind on; never creates a mark.
    public func noteListed(_ sessionsByProject: [String: [Ycc_V1_SessionSummary]]) {
        var changed = false
        var pending: [String: [String: String]] = [:]
        for (project, sessions) in sessionsByProject {
            for session in sessions {
                let id = session.sessionID
                guard !id.isEmpty else { continue }
                projects[id] = project
                if Self.isLater(session.readThrough, than: serverMarks[id]) {
                    serverMarks[id] = session.readThrough
                    changed = true
                }
                if let local = marks[id], needsPush(id, local) {
                    pending[project, default: [:]][id] = local
                }
            }
        }
        if changed { revision &+= 1 }
        push(pending)
    }

    // MARK: - Marking

    /// Record that everything up to `timestamp` (a daemon RFC3339 event stamp)
    /// has been seen for a session. Never moves a mark backwards, so a late
    /// out-of-order event cannot un-read a session. `project` is needed to push
    /// the mark; without it the project the session was last listed under is
    /// used, and an unlisted session's mark is pushed once it is listed.
    public func markRead(
        sessionID: String, through timestamp: String, project: String? = nil
    ) {
        remember(project, for: sessionID)
        guard applyMark(sessionID: sessionID, through: timestamp) else { return }
        persist()
        pushLocal([sessionID])
    }

    /// Mark a listed session read at its last reported activity — the "I know,
    /// stop nagging" affordance on a row the user does not want to open.
    public func markRead(_ session: Ycc_V1_SessionSummary, project: String? = nil) {
        markRead(sessionID: session.sessionID, through: Self.stamp(session), project: project)
    }

    /// Mark a whole list read (the list's "mark all read" action) in one write
    /// and one push per project. `projects` maps session ids to their project.
    public func markAllRead(_ sessions: [Ycc_V1_SessionSummary], projects: [String: String] = [:]) {
        var changed: [String] = []
        for session in sessions
        where applyMark(sessionID: session.sessionID, through: Self.stamp(session)) {
            remember(projects[session.sessionID], for: session.sessionID)
            changed.append(session.sessionID)
        }
        guard !changed.isEmpty else { return }
        persist()
        pushLocal(changed)
    }

    /// Move one local mark forward, reporting whether anything actually changed.
    private func applyMark(sessionID: String, through timestamp: String) -> Bool {
        guard !sessionID.isEmpty, !timestamp.isEmpty else { return false }
        if let existing = marks[sessionID] {
            if existing == timestamp { return false }
            if let existingDate = SessionListModel.parseTimestamp(existing),
               let newDate = SessionListModel.parseTimestamp(timestamp),
               newDate <= existingDate {
                return false
            }
        }
        marks[sessionID] = timestamp
        return true
    }

    private func remember(_ project: String?, for sessionID: String) {
        if let project, !project.isEmpty { projects[sessionID] = project }
    }

    /// The activity stamp a summary should be marked read at.
    private static func stamp(_ session: Ycc_V1_SessionSummary) -> String {
        session.lastActivity.isEmpty ? session.startedAt : session.lastActivity
    }

    // MARK: - Pushing

    private func needsPush(_ sessionID: String, _ local: String) -> Bool {
        Self.isLater(local, than: serverMarks[sessionID])
            && Self.isLater(local, than: inFlight[sessionID])
    }

    /// Push the local marks of `sessionIDs` the daemon is behind on, grouped by
    /// project. Sessions whose project is unknown wait for a list to name it.
    private func pushLocal(_ sessionIDs: [String]) {
        var pending: [String: [String: String]] = [:]
        for id in sessionIDs {
            guard let project = projects[id], let local = marks[id], needsPush(id, local) else { continue }
            pending[project, default: [:]][id] = local
        }
        push(pending)
    }

    /// One `MarkSessionsRead` per project. A failure is silent: the local mark
    /// stands, and the next list that shows the daemon behind re-pushes it.
    private func push(_ pending: [String: [String: String]]) {
        guard let sync else { return }
        for (project, entries) in pending where !entries.isEmpty {
            for (id, stamp) in entries { inFlight[id] = stamp }
            let request = entries.sorted { $0.key < $1.key }.map { id, stamp in
                var mark = Ycc_V1_SessionReadMark()
                mark.sessionID = id
                mark.readThrough = stamp
                return mark
            }
            let pushID = nextPushID
            nextPushID &+= 1
            pushTasks[pushID] = Task { @MainActor [weak self] in
                let accepted = try? await sync.markSessionsRead(project: project, marks: request)
                self?.finishPush(pushID, entries: entries, accepted: accepted)
            }
        }
    }

    private func finishPush(_ pushID: Int, entries: [String: String], accepted: [Ycc_V1_SessionReadMark]?) {
        pushTasks[pushID] = nil
        for (id, stamp) in entries where inFlight[id] == stamp { inFlight[id] = nil }
        guard let accepted else { return }
        var changed = false
        for mark in accepted where Self.isLater(mark.readThrough, than: serverMarks[mark.sessionID]) {
            serverMarks[mark.sessionID] = mark.readThrough
            changed = true
        }
        if changed { revision &+= 1 }
    }

    /// Wait until every push started so far has finished (tests).
    func waitForPushes() async {
        while let task = pushTasks.values.first { await task.value }
    }

    // MARK: - Stamps

    /// Milliseconds since the epoch: the daemon stores read marks at millisecond
    /// precision, so finer event stamps must not compare as "later" than the
    /// mark they produced.
    private static func millis(_ date: Date) -> Int64 {
        Int64((date.timeIntervalSince1970 * 1_000 + 0.001).rounded(.down))
    }

    private static func millis(_ text: String) -> Int64? {
        SessionListModel.parseTimestamp(text).map { millis($0) }
    }

    /// Whether `stamp` parses and is later than `other` (or `other` is absent or
    /// unparseable).
    private static func isLater(_ stamp: String, than other: String?) -> Bool {
        guard let value = millis(stamp) else { return false }
        guard let other, let otherValue = millis(other) else { return true }
        return value > otherValue
    }

    /// The latest parseable stamp among `stamps`, or nil when none parses.
    private static func latest(_ stamps: String?...) -> String? {
        var best: String?
        for case let stamp? in stamps where isLater(stamp, than: best) { best = stamp }
        return best
    }

    // MARK: - Persistence

    private func persist() {
        revision &+= 1
        evictIfNeeded()
        defaults?.set(marks, forKey: key)
    }

    /// Keep the most recently active marks when over the cap. Marks that carry
    /// no parseable timestamp are dropped first: they can never make a row
    /// unread, so they are pure ballast.
    private func evictIfNeeded() {
        guard marks.count > limit else { return }
        let ranked = marks.sorted { lhs, rhs in
            let l = SessionListModel.parseTimestamp(lhs.value) ?? .distantPast
            let r = SessionListModel.parseTimestamp(rhs.value) ?? .distantPast
            if l == r { return lhs.key < rhs.key }
            return l > r
        }
        marks = Dictionary(uniqueKeysWithValues: ranked.prefix(limit).map { ($0.key, $0.value) })
    }
}
