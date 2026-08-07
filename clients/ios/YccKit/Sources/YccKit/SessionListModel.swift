import Foundation
import Observation
import YccProto

/// The data source a ``SessionListModel`` reads from. Abstracting it behind a
/// protocol lets the sorting / sectioning / filtering logic be unit-tested
/// headlessly with an in-memory mock — no network, no simulator. ``YccClient``
/// is the production conformer.
public protocol SessionListSource: Sendable {
    /// List session history for a named project.
    func listSessionHistory(project: String) async throws -> [Ycc_V1_SessionSummary]
    /// List the daemon's registered projects (drives the project filter).
    func listProjects() async throws -> [Ycc_V1_ProjectInfo]
    /// Deregister a project. Workspace files remain untouched.
    func removeProject(name: String) async throws
    /// Fetch a project's daemon-side work-loop snapshot for row ownership badges.
    func workLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo?
}

/// Existing sources need not support loop ownership; it is supplemental and a
/// failure must never degrade the session list.
public extension SessionListSource {
    func workLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo? { nil }
}

extension YccClient: SessionListSource {
    public func workLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo? {
        try await getWorkLoop(project: project)
    }
}

private struct HistoryLoad: Sendable {
    let project: String
    var sessions: [Ycc_V1_SessionSummary] = []
    var loopSessionIDs: Set<String> = []
    var error: String?
    var unauthorized = false
}

/// The canonical status of a session, parsed from the daemon's free-form
/// `status` string (`running` | `idle` | `error` | `paused` | `stopped`). The
/// view maps each case to a colour + label; kept here so the mapping is
/// unit-testable and forward-compatible (unknown strings fall back to
/// ``unknown`` rather than crashing).
public enum SessionStatusKind: String, Sendable, CaseIterable {
    case running
    case idle
    case error
    case paused
    case stopped
    case unknown

    public init(status: String) {
        self = SessionStatusKind(rawValue: status.lowercased()) ?? .unknown
    }
}

/// Live-activity counts for one project (or the whole daemon). Drives the
/// workspace drawer's badges, where a waiting question outranks mere activity.
public struct ProjectActivity: Sendable, Equatable {
    /// Live sessions currently running or paused — work in flight.
    public var active = 0
    /// Live sessions blocked on an unanswered question. The loudest state a
    /// phone client exists to surface.
    public var needsAnswer = 0
    /// Sessions carrying agent activity this device has not looked at yet —
    /// including finished ones, which is the whole point: an agent that wrapped
    /// up while the phone was in a pocket must still say so.
    public var unread = 0

    public init(active: Int = 0, needsAnswer: Int = 0, unread: Int = 0) {
        self.active = active
        self.needsAnswer = needsAnswer
        self.unread = unread
    }

    public var isEmpty: Bool { active == 0 && needsAnswer == 0 && unread == 0 }
}

/// A grouped list of sessions for display. Needs-answer sessions are pinned to
/// the top in their own section; the rest follow most-recent-first.
public struct SessionSection: Identifiable, Sendable {
    public enum Kind: String, Sendable {
        /// Live sessions blocked on an unanswered question — the loud, pinned
        /// section a phone client exists to surface.
        case needsAnswer
        /// Everything else, most-recent-first.
        case all
    }

    public let kind: Kind
    /// A section header title, or `nil` for the ungrouped remainder when there
    /// is no needs-answer section to distinguish it from.
    public let title: String?
    public let sessions: [Ycc_V1_SessionSummary]

    public var id: String { kind.rawValue }
}

/// Drives the session-list home screen: loads ``ListSessionHistory`` +
/// ``ListProjects``, holds the selected project filter, and exposes the sorted /
/// sectioned view of sessions. The data source is injected
/// (``SessionListSource``) so the sorting / filtering logic is testable
/// headlessly. `@MainActor` because it publishes observable UI state.
@MainActor
@Observable
public final class SessionListModel {
    /// Every session loaded across the daemon, most-recent-first. ``sessions``
    /// is the ``selectedProject``-filtered view of this; the drawer's badges and
    /// deep-link resolution read the unfiltered set.
    public private(set) var allSessions: [Ycc_V1_SessionSummary] = []
    /// Registered projects; drives the workspace drawer.
    public private(set) var projects: [Ycc_V1_ProjectInfo] = []
    /// The selected project filter. `nil` is the daemon-wide recent-session feed;
    /// a value is a registered project name. Filtering is client-side over the
    /// aggregate load, so changing it needs no refresh and no network round-trip.
    public var selectedProject: String?

    public private(set) var isLoading = false
    /// A fatal load error. Partial aggregate failures use ``partialWarning`` and
    /// preserve every project that loaded successfully.
    public private(set) var errorMessage: String?
    public private(set) var partialWarning: String?
    /// Set when a load failed with ``YccError/unauthorized``; the view observes
    /// this to route back to the connect screen via `AppModel.handleUnauthorized`.
    public private(set) var unauthorized = false

    private let source: SessionListSource
    /// Durable "which sessions have agent activity I haven't seen" marks. Owned
    /// by the app (one per process) and injected so the list, the drawer badges
    /// and the session view all agree on what is unread. Defaults to a
    /// memory-only store, so a model built outside the app (tests, previews)
    /// neither reads nor writes the user's real marks.
    @ObservationIgnored public let readMarks: SessionReadStore

    public init(
        source: SessionListSource,
        selectedProject: String? = nil,
        readMarks: SessionReadStore? = nil
    ) {
        self.source = source
        self.selectedProject = selectedProject
        self.readMarks = readMarks ?? .ephemeral()
    }

    /// The sessions to display: everything, or just the selected project's.
    public var sessions: [Ycc_V1_SessionSummary] {
        guard let selectedProject else { return allSessions }
        return allSessions.filter { sessionProjects[$0.sessionID] == selectedProject }
    }

    /// The filter is meaningful when projects exist (alongside All projects).
    public var showsProjectFilter: Bool { !projects.isEmpty }

    /// Starting a chat from the daemon-wide Recent Sessions feed must ask for a
    /// project instead of silently choosing one.
    /// A project-scoped session list can start directly in its selected project.
    public var requiresProjectChoiceForNewSession: Bool { selectedProject == nil }

    /// Project names offered by the Recent Sessions new-chat prompt.
    public var newSessionProjectChoices: [String] { projects.map(\.name) }

    /// The daemon-wide home feed is one globally recency-sorted list. A scoped
    /// project view retains the phone-focused needs-answer pinning behavior.
    public var sections: [SessionSection] {
        guard selectedProject != nil else {
            return sessions.isEmpty ? [] : [SessionSection(
                kind: .all, title: nil, sessions: Self.sortedByRecency(sessions))]
        }
        return Self.sections(from: sessions)
    }

    /// Maps each loaded session id to the project argument required by transcript
    /// and resume RPCs. Aggregate rows therefore remain routable after histories
    /// from several projects are merged.
    public private(set) var sessionProjects: [String: String] = [:]

    /// Session ids owned by work loops in the projects loaded by the latest
    /// refresh. Rebuilt from completed sessions plus each current session.
    public private(set) var loopSessionIDs: Set<String> = []

    public func isLoopSession(_ session: Ycc_V1_SessionSummary) -> Bool {
        isLoopSession(sessionID: session.sessionID)
    }

    public func isLoopSession(sessionID: String) -> Bool {
        loopSessionIDs.contains(sessionID)
    }

    // MARK: - Unread agent activity

    /// Whether a row carries agent activity this device has not seen yet.
    public func isUnread(_ session: Ycc_V1_SessionSummary) -> Bool {
        readMarks.isUnread(session)
    }

    /// Unread rows in the current scope — what the list's "mark all read"
    /// affordance would clear.
    public var unreadCount: Int { readMarks.unreadCount(in: sessions) }

    /// Clear the unread flag on a single row without opening it.
    public func markRead(_ session: Ycc_V1_SessionSummary) {
        readMarks.markRead(session)
    }

    /// Clear every unread row in the current scope.
    public func markAllRead() {
        readMarks.markAllRead(sessions)
    }

    /// Live-activity counts per project name, for the drawer's badges. Computed
    /// from the loaded rows rather than cached at load time, so a local
    /// correction like ``markAnswered(sessionID:)`` is reflected immediately.
    public var activityByProject: [String: ProjectActivity] {
        var counts: [String: ProjectActivity] = [:]
        for project in loadedProjects { counts[project] = ProjectActivity() }
        for session in allSessions {
            guard let project = sessionProjects[session.sessionID] else { continue }
            var activity = counts[project] ?? ProjectActivity()
            Self.accumulate(session, into: &activity)
            if readMarks.isUnread(session) { activity.unread += 1 }
            counts[project] = activity
        }
        return counts
    }

    /// Project names that produced a successful history load, so a project with
    /// no sessions still reports (empty) activity rather than being absent.
    private var loadedProjects: [String] = []

    /// Daemon-wide live-activity counts, for the drawer's "Recent sessions" row.
    public var totalActivity: ProjectActivity {
        allSessions.reduce(into: ProjectActivity()) { total, session in
            Self.accumulate(session, into: &total)
            if readMarks.isUnread(session) { total.unread += 1 }
        }
    }

    /// Activity for one project (zero when it has none).
    public func activity(forProject name: String) -> ProjectActivity {
        activityByProject[name] ?? ProjectActivity()
    }

    /// Locally clear a session's "waiting for an answer" flag.
    ///
    /// The daemon is the source of truth, but the list only reloads on an
    /// explicit refresh — so after answering a question the inbox and the
    /// drawer badges kept nagging about a question that was already answered.
    /// This applies the correction the client already knows about; the next
    /// refresh overwrites it with the server's view either way.
    public func markAnswered(sessionID: String) {
        guard let index = allSessions.firstIndex(where: { $0.sessionID == sessionID }),
              allSessions[index].waitingInput
        else { return }
        allSessions[index].waitingInput = false
    }

    /// (Re)load the project list and every project's history. The aggregate feed
    /// queries each distinct registered workspace once, merges and deduplicates
    /// the results, and keeps successful projects when another project fails —
    /// so the drawer's badges stay accurate no matter which project is selected.
    public func refresh() async {
        isLoading = true
        defer { isLoading = false }
        unauthorized = false
        do {
            let loadedProjects = try await source.listProjects()
            projects = loadedProjects

            guard !loadedProjects.isEmpty else {
                // A daemon that reports no registered project can still own a
                // session log for its startup workspace; query it by the selected
                // name (an empty name resolves server-side).
                let name = selectedProject ?? ""
                let source = source
                async let history = source.listSessionHistory(project: name)
                async let loop = Self.loadWorkLoop(from: source, project: name)
                let loaded = try await history
                apply(loads: [HistoryLoad(
                    project: name,
                    sessions: loaded,
                    loopSessionIDs: Self.loopSessionIDs(from: await loop))])
                return
            }

            await refreshAcrossProjects(loadedProjects)
        } catch YccError.unauthorized {
            unauthorized = true
        } catch let YccError.rpc(message) {
            errorMessage = message
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    /// The project argument needed to open or resume a loaded row.
    public func project(for session: Ycc_V1_SessionSummary) -> String {
        sessionProjects[session.sessionID] ?? selectedProject ?? ""
    }

    private func refreshAcrossProjects(_ loadedProjects: [Ycc_V1_ProjectInfo]) async {
        // Project aliases that point at the same workspace would return the same
        // event logs. Keep the first registration for a stable display/routing name.
        var seenPaths = Set<String>()
        let targets: [String] = loadedProjects.compactMap { project in
            let path = project.path.trimmingCharacters(in: .whitespacesAndNewlines)
            let identity = path.isEmpty ? "name:\(project.name)" : "path:\(path)"
            return seenPaths.insert(identity).inserted ? project.name : nil
        }
        let queryTargets = targets

        let loads = await withTaskGroup(of: HistoryLoad.self, returning: [HistoryLoad].self) { group in
            for project in queryTargets {
                let source = source
                group.addTask {
                    do {
                        async let history = source.listSessionHistory(project: project)
                        async let loop = Self.loadWorkLoop(from: source, project: project)
                        return HistoryLoad(
                            project: project,
                            sessions: try await history,
                            loopSessionIDs: Self.loopSessionIDs(from: await loop))
                    } catch YccError.unauthorized {
                        return HistoryLoad(project: project, unauthorized: true)
                    } catch {
                        return HistoryLoad(
                            project: project,
                            error: (error as? YccError)?.displayMessage ?? error.localizedDescription)
                    }
                }
            }
            var byProject: [String: HistoryLoad] = [:]
            for await load in group { byProject[load.project] = load }
            // Restore project-list order so equal timestamps and deduplication are stable.
            return queryTargets.compactMap { byProject[$0] }
        }

        if loads.contains(where: \.unauthorized) {
            unauthorized = true
            return
        }
        apply(loads: loads)
    }

    /// Merge per-project history loads into the aggregate feed, its routing
    /// table, the drawer's activity counts, and the error/partial-warning state.
    private func apply(loads: [HistoryLoad]) {
        var merged: [Ycc_V1_SessionSummary] = []
        var routes: [String: String] = [:]
        var seenSessionIDs = Set<String>()
        var succeeded: [String] = []
        for load in loads where load.error == nil {
            succeeded.append(load.project)
            for session in load.sessions where seenSessionIDs.insert(session.sessionID).inserted {
                merged.append(session)
                routes[session.sessionID] = load.project
            }
        }
        allSessions = Self.sortedByRecency(merged)
        // Sessions this device has never seen are baselined as read: a first
        // load (or a freshly registered project's back-catalogue) must not shout
        // "unread" about history the user was never shown.
        readMarks.noteSeen(allSessions)
        sessionProjects = routes
        loopSessionIDs = loads.reduce(into: Set<String>()) { ids, load in
            ids.formUnion(load.loopSessionIDs)
        }
        loadedProjects = succeeded

        let failed = loads.filter { $0.error != nil }
        if failed.isEmpty {
            partialWarning = nil
            errorMessage = nil
        } else if failed.count < loads.count {
            let names = failed.map(\.project)
            partialWarning = "Some projects couldn’t be loaded: \(names.joined(separator: ", "))."
            errorMessage = nil
        } else {
            partialWarning = nil
            errorMessage = failed.first?.error ?? "Couldn’t load sessions."
        }
    }

    nonisolated private static func loadWorkLoop(
        from source: SessionListSource, project: String
    ) async -> Ycc_V1_WorkLoopInfo? {
        try? await source.workLoop(project: project)
    }

    nonisolated private static func loopSessionIDs(from loop: Ycc_V1_WorkLoopInfo?) -> Set<String> {
        guard let loop else { return [] }
        var ids = Set(loop.sessions.map(\.sessionID).filter { !$0.isEmpty })
        let current = loop.currentSessionID.trimmingCharacters(in: .whitespacesAndNewlines)
        if !current.isEmpty { ids.insert(current) }
        return ids
    }

    /// Count one session into a project's live-activity tally. Only live rows
    /// count: a persisted log's last-known status is history, not activity.
    static func accumulate(_ session: Ycc_V1_SessionSummary, into counts: inout ProjectActivity) {
        guard session.live else { return }
        if session.waitingInput { counts.needsAnswer += 1 }
        switch SessionStatusKind(status: session.status) {
        case .running, .paused: counts.active += 1
        default: break
        }
    }

    /// Deregister a project and reload the home screen. If it was selected, move
    /// to the daemon-wide recent feed before refreshing so no request is made
    /// with a now-stale project name. Returns true on success so the view
    /// can dismiss its confirmation state.
    @discardableResult
    public func removeProject(named name: String) async -> Bool {
        do {
            try await source.removeProject(name: name)
            projects.removeAll { $0.name == name }
            if selectedProject == name { selectedProject = nil }
            await refresh()
            return true
        } catch YccError.unauthorized {
            unauthorized = true
            return false
        } catch {
            errorMessage = (error as? YccError)?.displayMessage ?? error.localizedDescription
            return false
        }
    }

    // MARK: - Pure logic (unit-tested)

    /// Group + sort sessions: needs-answer rows (live && waitingInput) pinned to
    /// a top section, the remainder most-recent-first. Both sections are sorted
    /// by `lastActivity` (RFC3339) descending, falling back to `startedAt` then
    /// a stable original order when timestamps are missing/unparseable.
    public static func sections(from sessions: [Ycc_V1_SessionSummary]) -> [SessionSection] {
        // Stable partition preserving original order within each group so the
        // recency sort (which is stable) has a deterministic base.
        var needsAnswer: [Ycc_V1_SessionSummary] = []
        var rest: [Ycc_V1_SessionSummary] = []
        for session in sessions {
            if session.live && session.waitingInput {
                needsAnswer.append(session)
            } else {
                rest.append(session)
            }
        }

        var out: [SessionSection] = []
        if !needsAnswer.isEmpty {
            out.append(SessionSection(
                kind: .needsAnswer,
                title: "Needs answer",
                sessions: sortedByRecency(needsAnswer)))
        }
        if !rest.isEmpty {
            out.append(SessionSection(
                kind: .all,
                // Only label the remainder when there's a needs-answer section
                // above it to distinguish from.
                title: needsAnswer.isEmpty ? nil : "All sessions",
                sessions: sortedByRecency(rest)))
        }
        return out
    }

    /// Most-recent-first by `lastActivity` (fallback `startedAt`). Uses a stable
    /// sort so equal / unparseable timestamps keep their original relative order.
    static func sortedByRecency(_ sessions: [Ycc_V1_SessionSummary]) -> [Ycc_V1_SessionSummary] {
        enumeratedStableSort(sessions) { a, b in
            let da = recencyDate(a)
            let db = recencyDate(b)
            switch (da, db) {
            case let (x?, y?):
                return x > y
            case (_?, nil):
                return true   // rows with a date sort before rows without
            case (nil, _?):
                return false
            case (nil, nil):
                return false  // keep original order (stable)
            }
        }
    }

    /// The date to sort a session by: `lastActivity`, falling back to
    /// `startedAt`. Returns `nil` when neither parses.
    public static func recencyDate(_ session: Ycc_V1_SessionSummary) -> Date? {
        parseTimestamp(session.lastActivity) ?? parseTimestamp(session.startedAt)
    }

    /// Parse an RFC3339 / ISO8601 timestamp. Daemon timestamps may carry
    /// fractional seconds and a numeric offset, so try with fractional seconds
    /// first, then without. Empty / unparseable input returns `nil`.
    static func parseTimestamp(_ value: String) -> Date? {
        if value.isEmpty { return nil }
        return isoWithFraction.date(from: value) ?? isoPlain.date(from: value)
    }

    private static let isoWithFraction: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    private static let isoPlain: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()

    /// A row's display title: the derived title, falling back to
    /// `mode + short session id` when empty so no row is blank. Once the agent
    /// focuses backlog work, prefix the name with the focused task ids so the
    /// session remains identifiable from the phone without opening it.
    public static func displayTitle(for session: Ycc_V1_SessionSummary) -> String {
        let title = session.title.trimmingCharacters(in: .whitespacesAndNewlines)
        let base: String
        if !title.isEmpty {
            base = title
        } else {
            let shortID = String(session.sessionID.prefix(8))
            let mode = session.mode.isEmpty ? "session" : session.mode
            base = shortID.isEmpty ? mode : "\(mode) · \(shortID)"
        }

        var seen = Set<String>()
        let taskIDs = session.focusTasks.compactMap { task -> String? in
            let id = task.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !id.isEmpty, seen.insert(id).inserted else { return nil }
            return id
        }
        guard !taskIDs.isEmpty else { return base }
        return "[\(taskIDs.joined(separator: ","))] \(base)"
    }

    /// A stable sort: Swift's `sort(by:)` is not guaranteed stable, so decorate
    /// with the original index and break ties on it.
    private static func enumeratedStableSort(
        _ items: [Ycc_V1_SessionSummary],
        by areInIncreasingOrder: (Ycc_V1_SessionSummary, Ycc_V1_SessionSummary) -> Bool
    ) -> [Ycc_V1_SessionSummary] {
        items.enumerated()
            .sorted { lhs, rhs in
                if areInIncreasingOrder(lhs.element, rhs.element) { return true }
                if areInIncreasingOrder(rhs.element, lhs.element) { return false }
                return lhs.offset < rhs.offset
            }
            .map(\.element)
    }
}
