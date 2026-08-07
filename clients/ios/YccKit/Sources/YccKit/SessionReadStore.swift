import Foundation
import Observation
import YccProto

/// Tracks which sessions have **agent activity the user has not looked at yet**,
/// so the session list can flag "there are new messages in here" the way a mail
/// inbox does (docs/design/ios-client.md §6 "Unread agent activity").
///
/// The daemon has no per-device read state, so this is client-side and durable:
/// for every session id we remember the timestamp of the newest event this
/// device has *seen*, and a row is unread when the daemon reports later activity
/// than that mark. Two properties matter:
///
/// - **Daemon clocks only.** Marks are recorded from event / summary timestamps
///   produced by the daemon, never from the phone's clock, so a skewed device
///   clock cannot make a just-read session look unread (or vice versa).
/// - **First sighting is read.** A session the store has never seen is baselined
///   at its current activity rather than shouted about, so installing the app —
///   or connecting to a daemon with months of history — does not present a
///   wall of false unread rows. Anything that happens *after* that first sighting
///   is unread until the session view is opened.
///
/// Marks are persisted in `UserDefaults` (they are a UI convenience, not a
/// secret) and capped, evicting the least recently active sessions first.
@MainActor
@Observable
public final class SessionReadStore {
    /// sessionID → RFC3339 timestamp of the newest event this device has seen.
    private var marks: [String: String] = [:]

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
        limit: Int = 600
    ) {
        self.defaults = defaults
        self.key = key
        self.limit = limit
        if let stored = defaults?.dictionary(forKey: key) as? [String: String] {
            marks = stored
        }
    }

    /// A memory-only store: nothing is persisted and nothing is shared. Useful
    /// for previews and for models that are not the app's live session list.
    public static func ephemeral() -> SessionReadStore { SessionReadStore(defaults: nil) }

    // MARK: - Queries

    /// Whether a session has agent activity newer than the last thing this
    /// device saw. Unknown sessions are *not* unread (see ``noteSeen(_:)``).
    ///
    /// A session that is still **running** is never unread: the row already
    /// announces itself as live, its log grows every few seconds, and badging
    /// that would make the indicator meaningless noise. It goes unread the
    /// moment it stops producing (idle / paused / error / stopped) — which is
    /// precisely the "the agent finished while you were away" case this exists
    /// for.
    public func isUnread(_ session: Ycc_V1_SessionSummary) -> Bool {
        if session.live, SessionStatusKind(status: session.status) == .running { return false }
        guard let markText = marks[session.sessionID] else { return false }
        guard let activity = SessionListModel.recencyDate(session) else { return false }
        guard let mark = SessionListModel.parseTimestamp(markText) else { return false }
        return activity > mark
    }

    /// How many of `sessions` carry unread agent activity.
    public func unreadCount(in sessions: [Ycc_V1_SessionSummary]) -> Int {
        sessions.reduce(into: 0) { count, session in
            if isUnread(session) { count += 1 }
        }
    }

    // MARK: - Marking

    /// Baseline every session the store has not seen before at its current
    /// activity, so a first load never reports history as unread. Sessions
    /// already known keep their mark (that is what makes them go unread).
    public func noteSeen(_ sessions: [Ycc_V1_SessionSummary]) {
        var changed = false
        for session in sessions where marks[session.sessionID] == nil {
            // A row with no usable timestamp cannot be compared later either;
            // record what we have (possibly empty) so it is still "known".
            marks[session.sessionID] = Self.stamp(session)
            changed = true
        }
        if changed { persist() }
    }

    /// Record that everything up to `timestamp` (a daemon RFC3339 event stamp)
    /// has been seen for a session. Never moves a mark backwards, so a late
    /// out-of-order event cannot un-read a session.
    public func markRead(sessionID: String, through timestamp: String) {
        if applyMark(sessionID: sessionID, through: timestamp) { persist() }
    }

    /// Mark a listed session read at its last reported activity — the "I know,
    /// stop nagging" affordance on a row the user does not want to open.
    public func markRead(_ session: Ycc_V1_SessionSummary) {
        if applyMark(sessionID: session.sessionID, through: Self.stamp(session)) { persist() }
    }

    /// Mark a whole list read (the list's "mark all read" action) in one write.
    public func markAllRead(_ sessions: [Ycc_V1_SessionSummary]) {
        var changed = false
        for session in sessions {
            if applyMark(sessionID: session.sessionID, through: Self.stamp(session)) {
                changed = true
            }
        }
        if changed { persist() }
    }

    /// Move one mark forward, reporting whether anything actually changed.
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

    /// The activity stamp a summary should be marked read at.
    private static func stamp(_ session: Ycc_V1_SessionSummary) -> String {
        session.lastActivity.isEmpty ? session.startedAt : session.lastActivity
    }

    // MARK: - Persistence

    private func persist() {
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
