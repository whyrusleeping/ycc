import Foundation
import Observation
import YccProto

/// Tracks which sessions have **agent activity the user has not looked at yet**,
/// so the session list can flag "there are new messages in here" the way a mail
/// inbox does.
///
/// The daemon has no per-device read state, so this is client-side and durable:
/// for every session id we remember the timestamp of the newest event this
/// device has *seen*, and a row is unread when the daemon reports later activity
/// than that mark. Two properties matter:
///
/// - **Daemon clocks only.** Marks are recorded from event / summary timestamps
///   produced by the daemon, never from the phone's clock, so a skewed device
///   clock cannot make a just-read session look unread (or vice versa).
/// - **The first list is read.** The newest activity shown in any session list
///   is retained as a daemon-clock watermark. A fresh install baselines its whole
///   first list as read, and previously unseen sessions at or before the watermark
///   are treated as back-catalogue. Sessions first appearing after the watermark
///   are genuinely new and remain unread until the session view is opened.
///
/// Marks and the watermark are persisted in `UserDefaults` (they are a UI
/// convenience, not a secret). Marks are capped, evicting the least recently
/// active sessions first.
@MainActor
@Observable
public final class SessionReadStore {
    /// sessionID → RFC3339 timestamp of the newest event this device has seen.
    private var marks: [String: String] = [:]
    /// RFC3339 timestamp of the newest session activity ever shown in a list.
    private var watermark: String?

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
        if let storedWatermark = defaults?.string(forKey: key + ".watermark") {
            watermark = storedWatermark
        } else if !marks.isEmpty {
            // Stores written before the watermark was introduced should classify
            // new sessions correctly on their first refresh after an upgrade.
            let derived = marks.values.compactMap { value -> (String, Date)? in
                guard let date = SessionListModel.parseTimestamp(value) else { return nil }
                return (value, date)
            }.max { $0.1 < $1.1 }?.0
            watermark = derived
            if let derived { defaults?.set(derived, forKey: key + ".watermark") }
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

    /// Baseline sessions from the first list and later back-catalogue as read.
    /// An unknown session newer than the pre-refresh watermark is genuinely new,
    /// so its mark starts at that watermark and its later activity is unread.
    /// Sessions already known keep their mark (that is what makes them go unread).
    public func noteSeen(_ sessions: [Ycc_V1_SessionSummary]) {
        let priorWatermarkText = watermark
        let priorWatermarkDate = priorWatermarkText.flatMap(SessionListModel.parseTimestamp)
        var changed = false

        for session in sessions where marks[session.sessionID] == nil {
            let recency = Self.recencyStamp(session)
            if let priorWatermarkText,
               let priorWatermarkDate,
               let recency,
               recency.date > priorWatermarkDate {
                marks[session.sessionID] = priorWatermarkText
            } else {
                // A row with no usable timestamp cannot be compared later either;
                // record what we have (possibly empty) so it is still "known".
                marks[session.sessionID] = recency?.text ?? Self.stamp(session)
            }
            changed = true
        }

        var newestWatermark = priorWatermarkText.flatMap { text -> (text: String, date: Date)? in
            guard let date = priorWatermarkDate else { return nil }
            return (text, date)
        }
        for session in sessions {
            guard let recency = Self.recencyStamp(session) else { continue }
            if let newest = newestWatermark {
                if recency.date > newest.date { newestWatermark = recency }
            } else {
                newestWatermark = recency
            }
        }
        if let newestWatermark, newestWatermark.text != watermark {
            watermark = newestWatermark.text
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

    /// The text and parsed date for a summary's recency, preserving
    /// `recencyDate`'s fallback to `startedAt` when `lastActivity` is unusable.
    private static func recencyStamp(
        _ session: Ycc_V1_SessionSummary
    ) -> (text: String, date: Date)? {
        guard let date = SessionListModel.recencyDate(session) else { return nil }
        if SessionListModel.parseTimestamp(session.lastActivity) != nil {
            return (session.lastActivity, date)
        }
        return (session.startedAt, date)
    }

    // MARK: - Persistence

    private func persist() {
        evictIfNeeded()
        defaults?.set(marks, forKey: key)
        if let watermark {
            defaults?.set(watermark, forKey: key + ".watermark")
        } else {
            defaults?.removeObject(forKey: key + ".watermark")
        }
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
