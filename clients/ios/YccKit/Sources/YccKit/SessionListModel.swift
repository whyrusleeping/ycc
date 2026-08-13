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
    /// Rename a project in the daemon registry, returning the renamed project.
    func renameProject(name: String, to newName: String) async throws -> Ycc_V1_ProjectInfo
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
    /// Foregrounding can leave pooled TCP connections dead, and CFNetwork does
    /// not automatically retry our POSTs. Briefly retry those transient failures.
    private let retryDelays: [TimeInterval]
    /// `.task`, foregrounding, and router changes can request a refresh together.
    /// They share one load so those triggers do not multiply the POST burst.
    @ObservationIgnored private var refreshTask: Task<Void, Never>?
    /// Durable "which sessions have agent activity I haven't seen" marks. Owned
    /// by the app (one per process) and injected so the list, the drawer badges
    /// and the session view all agree on what is unread. Defaults to a
    /// memory-only store, so a model built outside the app (tests, previews)
    /// neither reads nor writes the user's real marks.
    @ObservationIgnored public let readMarks: SessionReadStore

    public init(
        source: SessionListSource,
        selectedProject: String? = nil,
        readMarks: SessionReadStore? = nil,
        retryDelays: [TimeInterval] = [0.4, 1.2]
    ) {
        self.source = source
        self.selectedProject = selectedProject
        self.readMarks = readMarks ?? .ephemeral()
        self.retryDelays = retryDelays
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
    /// Concurrent callers await the same load rather than issuing overlapping
    /// foreground POST bursts.
    public func refresh() async {
        if let refreshTask {
            await refreshTask.value
            return
        }

        let task = Task { await performRefresh() }
        refreshTask = task
        await task.value
        refreshTask = nil
    }

    /// Refresh only the lightweight project rows used by the visible drawer.
    /// ListProjects reads local refs plus daemon-cached fetch metadata, so this
    /// avoids repeatedly fanning out over every project's session history.
    /// Transient failures preserve the last snapshot; authorization still routes
    /// through the normal disconnect path.
    public func refreshProjects() async {
        do {
            let loaded = try await source.listProjects()
            guard !Task.isCancelled else { return }
            projects = loaded
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            // A background badge refresh is best-effort and must not blank the
            // drawer or replace a useful screen-level error.
        }
    }

    private func performRefresh() async {
        isLoading = true
        defer { isLoading = false }
        unauthorized = false
        do {
            let source = source
            let retryDelays = retryDelays
            let loadedProjects = try await Self.retrying(delays: retryDelays) {
                try await source.listProjects()
            }
            projects = loadedProjects

            guard !loadedProjects.isEmpty else {
                // A daemon that reports no registered project can still own a
                // session log for its startup workspace; query it by the selected
                // name (an empty name resolves server-side).
                let name = selectedProject ?? ""
                async let history = Self.retrying(delays: retryDelays) {
                    try await source.listSessionHistory(project: name)
                }
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
        let retryDelays = retryDelays

        let loads = await withTaskGroup(of: HistoryLoad.self, returning: [HistoryLoad].self) { group in
            for project in queryTargets {
                let source = source
                group.addTask {
                    do {
                        async let history = Self.retrying(delays: retryDelays) {
                            try await source.listSessionHistory(project: project)
                        }
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
        // A project that stays unreachable after retries should not disappear from
        // the drawer. Preserve its last successful rows and routing while still
        // surfacing the persistent warning/error below.
        let previousSessions = allSessions
        let previousRoutes = sessionProjects
        let previouslyLoaded = Set(loadedProjects)
        let previousLoopSessionIDs = loopSessionIDs

        var merged: [Ycc_V1_SessionSummary] = []
        var routes: [String: String] = [:]
        var seenSessionIDs = Set<String>()
        var succeeded: [String] = []
        var retainedLoopSessionIDs = Set<String>()
        for load in loads {
            if load.error == nil {
                succeeded.append(load.project)
                for session in load.sessions where seenSessionIDs.insert(session.sessionID).inserted {
                    merged.append(session)
                    routes[session.sessionID] = load.project
                }
            } else if previouslyLoaded.contains(load.project) {
                succeeded.append(load.project)
                for session in previousSessions
                where previousRoutes[session.sessionID] == load.project
                    && seenSessionIDs.insert(session.sessionID).inserted
                {
                    merged.append(session)
                    routes[session.sessionID] = load.project
                    if previousLoopSessionIDs.contains(session.sessionID) {
                        retainedLoopSessionIDs.insert(session.sessionID)
                    }
                }
            }
        }
        allSessions = Self.sortedByRecency(merged)
        // Sessions this device has never seen are baselined as read: a first
        // load (or a freshly registered project's back-catalogue) must not shout
        // "unread" about history the user was never shown.
        readMarks.noteSeen(allSessions)
        sessionProjects = routes
        loopSessionIDs = loads.reduce(into: retainedLoopSessionIDs) { ids, load in
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

    /// Retry short-lived foreground connection failures. Authorization failures
    /// are definitive and must route back to Connect without extra requests.
    nonisolated private static func retrying<Value: Sendable>(
        delays: [TimeInterval],
        operation: @Sendable () async throws -> Value
    ) async throws -> Value {
        var nextDelay = delays.makeIterator()
        while true {
            do {
                return try await operation()
            } catch YccError.unauthorized {
                throw YccError.unauthorized
            } catch {
                guard let delay = nextDelay.next() else { throw error }
                if delay > 0, delay.isFinite {
                    let maximum = TimeInterval(UInt64.max / 1_000_000_000)
                    let nanoseconds = UInt64(min(delay, maximum) * 1_000_000_000)
                    try await Task<Never, Never>.sleep(nanoseconds: nanoseconds)
                }
            }
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

    /// Rename a project and reload the home screen. Selection follows the
    /// rename so the user stays where they were, under the new name. Returns
    /// true on success so the view can dismiss its prompt state; on failure
    /// ``errorMessage`` carries the daemon's reason (collision, unknown name).
    @discardableResult
    public func renameProject(named name: String, to newName: String) async -> Bool {
        do {
            let renamed = try await source.renameProject(name: name, to: newName)
            if let index = projects.firstIndex(where: { $0.name == name }) {
                projects[index] = renamed
            }
            if selectedProject == name { selectedProject = renamed.name }
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

    /// Normalized focused task IDs in first-seen order. Empty and duplicate IDs
    /// are omitted so legacy/corrupt events cannot create blank chips.
    public static func taskIDs(for session: Ycc_V1_SessionSummary) -> [String] {
        var seen = Set<String>()
        return session.focusTasks.compactMap { raw -> String? in
            let id = raw.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !id.isEmpty, seen.insert(id).inserted else { return nil }
            return id
        }
    }

    /// Compact labels for task chips. Two IDs remain directly visible; further
    /// IDs collapse into a final count so narrow rows and Dynamic Type can wrap
    /// without an unbounded run of chips.
    public static func taskChipLabels(for session: Ycc_V1_SessionSummary) -> [String] {
        let ids = taskIDs(for: session)
        guard ids.count > 2 else { return ids }
        return Array(ids.prefix(2)) + ["+\(ids.count - 2)"]
    }

    /// A row's primary title, falling back to `mode + short session id` when
    /// empty. Matching task boilerplate is removed only at the beginning: old
    /// clients injected `[0198]`, and work prompts commonly begin `Work on task
    /// 0198:` or `0198 —`. Unrelated prompt text is deliberately untouched.
    public static func displayTitle(for session: Ycc_V1_SessionSummary) -> String {
        var title = session.title.trimmingCharacters(in: .whitespacesAndNewlines)
        let taskIDs = taskIDs(for: session)
        let taskSet = Set(taskIDs)
        // The former row helper injected all focused IDs as `[0198,0200]`.
        // Remove only a leading bracket whose complete comma-separated contents
        // are known focused tasks; arbitrary bracketed prompt text survives.
        if title.hasPrefix("["), let close = title.firstIndex(of: "]") {
            let inside = title[title.index(after: title.startIndex)..<close]
            let bracketIDs = inside.split(separator: ",", omittingEmptySubsequences: false)
                .map { String($0).trimmingCharacters(in: .whitespacesAndNewlines) }
            if !bracketIDs.isEmpty && bracketIDs.allSatisfy({ !$0.isEmpty && taskSet.contains($0) }) {
                title = String(title[title.index(after: close)...])
                    .trimmingCharacters(in: .whitespacesAndNewlines)
            }
        }
        for id in taskIDs {
            let escaped = NSRegularExpression.escapedPattern(for: id)
            let patterns = [
                #"(?i)^work\s+on\s+task\s+"# + escaped + #"\s*[:\-–—]\s*"#,
                #"^"# + escaped + #"\s*[:\-–—]\s*"#,
            ]
            for pattern in patterns {
                guard let expression = try? NSRegularExpression(pattern: pattern) else { continue }
                let range = NSRange(title.startIndex..<title.endIndex, in: title)
                if expression.firstMatch(in: title, range: range) != nil {
                    title = expression.stringByReplacingMatches(
                        in: title, range: range, withTemplate: ""
                    ).trimmingCharacters(in: .whitespacesAndNewlines)
                    break
                }
            }
        }
        if !title.isEmpty { return title }
        let shortID = String(session.sessionID.prefix(8))
        let mode = session.mode.trimmingCharacters(in: .whitespacesAndNewlines)
        let base = mode.isEmpty ? "session" : mode
        return shortID.isEmpty ? base : "\(base) · \(shortID)"
    }

    /// Compact, deterministic logical-model signal. Although the daemon sends
    /// usage in this order, sort here too so cached/older servers cannot produce
    /// a flickering label. Models without a name or positive usage are omitted.
    public static func modelSummary(for session: Ycc_V1_SessionSummary) -> String? {
        let models = session.modelUsage
            .filter { !$0.model.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && $0.tokens > 0 }
            .sorted {
                if $0.tokens != $1.tokens { return $0.tokens > $1.tokens }
                return $0.model < $1.model
            }
        guard let first = models.first else { return nil }
        let name = first.model.trimmingCharacters(in: .whitespacesAndNewlines)
        return models.count == 1 ? name : "\(name) +\(models.count - 1)"
    }

    /// Human-sized token total for the metadata line. Zero/missing usage is
    /// omitted rather than presented as a misleading `0 tok`.
    public static func tokenSummary(for session: Ycc_V1_SessionSummary) -> String? {
        let tokens = session.totalTokens
        guard tokens > 0 else { return nil }
        let value: String
        if tokens < 1_000 {
            value = String(tokens)
        } else if tokens < 999_500 {
            value = compactDecimal(Double(tokens) / 1_000) + "K"
        } else if tokens < 999_500_000 {
            // Promote values whose compact rounding would otherwise say 1000K.
            value = compactDecimal(Double(tokens) / 1_000_000) + "M"
        } else {
            // Likewise, never display 1000M at the next unit boundary.
            value = compactDecimal(Double(tokens) / 1_000_000_000) + "B"
        }
        return "\(value) tok"
    }

    private static func compactDecimal(_ value: Double) -> String {
        let locale = Locale(identifier: "en_US_POSIX")
        if value >= 100 || value.rounded() == value {
            return String(format: "%.0f", locale: locale, value)
        }
        let rounded = String(format: "%.1f", locale: locale, value)
        return rounded.hasSuffix(".0") ? String(rounded.dropLast(2)) : rounded
    }

    /// Lifecycle label worth showing in a history ledger. Idle is routine and
    /// unknown/empty legacy states have no trustworthy signal. Waiting overrides
    /// running because it is the action the user needs to take.
    public static func lifecycleLabel(for session: Ycc_V1_SessionSummary) -> String? {
        if session.live && session.waitingInput { return "waiting" }
        switch SessionStatusKind(status: session.status) {
        case .running: return "running"
        case .paused: return "paused"
        case .error: return "error"
        case .stopped: return "stopped"
        case .idle, .unknown: return nil
        }
    }

    /// Routine non-project metadata, kept testable outside SwiftUI. Missing
    /// values vanish cleanly and loop provenance is deliberately plain text.
    public static func metadataItems(
        for session: Ycc_V1_SessionSummary, isLoopOwned: Bool
    ) -> [String] {
        var items: [String] = []
        let mode = session.mode.trimmingCharacters(in: .whitespacesAndNewlines)
        if !mode.isEmpty { items.append(mode) }
        if let model = modelSummary(for: session) { items.append(model) }
        if let tokens = tokenSummary(for: session) { items.append(tokens) }
        if isLoopOwned { items.append("via loop") }
        return items
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
