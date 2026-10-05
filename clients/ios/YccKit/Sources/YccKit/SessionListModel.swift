import Foundation
import Observation
import YccProto

/// The data source a ``SessionListModel`` reads from. Abstracting it behind a
/// protocol lets the sorting / sectioning / filtering logic be unit-tested
/// headlessly with an in-memory mock — no network, no simulator. ``YccClient``
/// is the production conformer.
public struct SessionHistoryPage: Sendable {
    public let sessions: [Ycc_V1_SessionSummary]
    public let pinned: [Ycc_V1_SessionSummary]
    public let nextCursor: String

    public init(sessions: [Ycc_V1_SessionSummary], pinned: [Ycc_V1_SessionSummary], nextCursor: String) {
        self.sessions = sessions
        self.pinned = pinned
        self.nextCursor = nextCursor
    }
}

public protocol SessionListSource: Sendable {
    /// List one keyset page of history for a named project.
    func listSessionHistory(project: String, limit: Int32, cursor: String) async throws -> SessionHistoryPage
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

private struct HistoryLoad: Sendable, Equatable {
    let project: String
    var sessions: [Ycc_V1_SessionSummary] = []
    var pinned: [Ycc_V1_SessionSummary] = []
    var nextCursor = ""
    var loopSessionIDs: Set<String> = []
    var error: String?
    var unauthorized = false
    var hasHistory = false
}

private enum HistoryUpdate: Sendable {
    case history(HistoryLoad)
    // nil means the supplemental request failed; an empty set clears badges.
    case loop(project: String, ids: Set<String>?)
    /// ListProjects (run concurrently with the cached-project fan-out) resolved.
    case projects([Ycc_V1_ProjectInfo])
    /// ListProjects failed; `nil` message means unauthorized.
    case projectsFailed(message: String?)
    /// The partial-publication valve fired: a slow project has held the
    /// single end-of-refresh publication back long enough.
    case publishPartial
}

/// Rows derived from the loaded pages for the current scope, cached until the
/// data or the selected project changes so UI reads never re-sort.
private struct ScopedRows {
    let revision: Int
    let project: String?
    let sessions: [Ycc_V1_SessionSummary]
    let sections: [SessionSection]
}

/// Drawer activity derived from the loaded rows and the read marks, cached
/// until either changes (the drawer is always mounted, so it is read on every
/// model change).
private struct ActivitySnapshot {
    let revision: Int
    let marksRevision: Int
    let byProject: [String: ProjectActivity]
    let total: ProjectActivity
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
    /// Live sessions currently running or paused, or idle while delegated work
    /// will still resume them — work in flight.
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
    /// Globally ordered visible sessions, plus pinned live rows. Rows below
    /// the newest truncated project's oldest page row are held back until all
    /// projects have paged past them. ``sessions`` shows the selected project's
    /// loaded pages without that aggregate frontier.
    public private(set) var allSessions: [Ycc_V1_SessionSummary] = []
    /// Registered projects; drives the workspace drawer.
    public private(set) var projects: [Ycc_V1_ProjectInfo] = []
    /// Whether ``projects`` reflects a successful `ListProjects` (rather than
    /// the empty initial value), so it can be shared app-wide as authoritative.
    public private(set) var hasLoadedProjects = false
    /// The selected project filter. `nil` is the daemon-wide recent-session feed;
    /// a value is a registered project name. Scope changes use loaded pages
    /// locally, without a network round-trip.
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

    /// Monotonic clock (seconds) used by the passive-refresh throttle. The
    /// default keeps counting while the device sleeps (unlike `systemUptime`),
    /// so a phone unlocked an hour later is never mistaken for "just refreshed".
    @ObservationIgnored private let clock: @Sendable () -> TimeInterval
    /// Passive refreshes (return to root, foregrounding, re-appearance) are
    /// skipped when a successful refresh completed less than this long ago.
    @ObservationIgnored private let staleAfter: TimeInterval
    /// A refresh publishes its merged result once, when every project has
    /// answered. If some project is still outstanding after this long, publish
    /// what has arrived and continue progressively so one slow project cannot
    /// hold the whole list back. `nil` disables the valve.
    @ObservationIgnored private let partialPublishDelay: TimeInterval?
    @ObservationIgnored private var lastSuccessfulRefresh: TimeInterval?
    /// How many times loaded pages were merged and published (diagnostics/tests).
    @ObservationIgnored private(set) var publicationCount = 0

    public init(
        source: SessionListSource,
        selectedProject: String? = nil,
        readMarks: SessionReadStore? = nil,
        retryDelays: [TimeInterval] = [0.4, 1.2],
        staleAfter: TimeInterval = 10,
        partialPublishDelay: TimeInterval? = 1.5,
        clock: @escaping @Sendable () -> TimeInterval = { SessionListModel.continuousSeconds() }
    ) {
        self.source = source
        self.selectedProject = selectedProject
        self.readMarks = readMarks ?? .ephemeral()
        self.retryDelays = retryDelays
        self.staleAfter = staleAfter
        self.partialPublishDelay = partialPublishDelay
        self.clock = clock
    }

    /// Bumped whenever the loaded rows, routing or per-project pages change.
    /// Derived views (``sessions``, ``sections``, drawer activity) key their
    /// caches on it, so reads between changes cost a comparison, not a sort.
    private(set) var dataRevision = 0
    @ObservationIgnored private var scopedCache: ScopedRows?
    @ObservationIgnored private var activityCache: ActivitySnapshot?

    /// The sessions to display: everything, or just the selected project's.
    public var sessions: [Ycc_V1_SessionSummary] { scopedRows.sessions }

    private var scopedRows: ScopedRows {
        // Reading both keys registers the observation dependencies even when
        // the cached value is returned.
        let revision = dataRevision
        let project = selectedProject
        if let cached = scopedCache, cached.revision == revision, cached.project == project {
            return cached
        }
        let rows: [Ycc_V1_SessionSummary]
        let sections: [SessionSection]
        if let project {
            if let load = historyLoads[project] {
                var scoped = load.sessions
                let seen = Set(scoped.map(\.sessionID))
                scoped.append(contentsOf: load.pinned.filter { !seen.contains($0.sessionID) })
                rows = Self.sortedByRecency(scoped)
            } else {
                rows = []
            }
            // A scoped project view keeps the needs-answer pinning.
            sections = Self.sectionsFromSorted(rows)
        } else {
            // The aggregate is already sorted on ingestion.
            rows = allSessions
            sections = rows.isEmpty ? [] : [SessionSection(kind: .all, title: nil, sessions: rows)]
        }
        let value = ScopedRows(revision: revision, project: project, sessions: rows, sections: sections)
        scopedCache = value
        return value
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
    public var sections: [SessionSection] { scopedRows.sections }

    /// Maps each aggregate-visible session id to the project argument required by
    /// transcript and resume RPCs. Rows remain routable after histories
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

    /// Live-activity counts per project name, for the drawer's badges. Derived
    /// from the loaded rows and cached until they or the read marks change, so
    /// a local correction like ``markAnswered(sessionID:)`` or a mark-read is
    /// reflected immediately while ordinary renders do no per-row work.
    public var activityByProject: [String: ProjectActivity] { activitySnapshot.byProject }

    private var activitySnapshot: ActivitySnapshot {
        let revision = dataRevision
        let marksRevision = readMarks.revision
        if let cached = activityCache, cached.revision == revision, cached.marksRevision == marksRevision {
            return cached
        }
        var counts: [String: ProjectActivity] = [:]
        var total = ProjectActivity()
        for project in loadedProjects { counts[project] = ProjectActivity() }
        for session in allSessions {
            var own = ProjectActivity()
            Self.accumulate(session, into: &own)
            if readMarks.isUnread(session) { own.unread += 1 }
            total.active += own.active
            total.needsAnswer += own.needsAnswer
            total.unread += own.unread
            guard let project = sessionProjects[session.sessionID] else { continue }
            var activity = counts[project] ?? ProjectActivity()
            activity.active += own.active
            activity.needsAnswer += own.needsAnswer
            activity.unread += own.unread
            counts[project] = activity
        }
        let value = ActivitySnapshot(
            revision: revision, marksRevision: marksRevision, byProject: counts, total: total)
        activityCache = value
        return value
    }

    /// Project names that produced a successful history load, so a project with
    /// no sessions still reports (empty) activity rather than being absent.
    private var loadedProjects: [String] = []
    private var historyLoads: [String: HistoryLoad] = [:]
    @ObservationIgnored private var refreshGeneration = 0
    public private(set) var isLoadingMoreHistory = false

    public var hasMoreHistory: Bool {
        historyLoads.contains { project, load in
            !load.nextCursor.isEmpty && (selectedProject == nil || selectedProject == project)
        }
    }

    /// Fetch one older page per truncated project, concurrently. Cursor pages
    /// merge by ID, so a live row already supplied as pinned cannot appear twice.
    public func loadMoreHistory() async {
        guard !isLoadingMoreHistory && !isLoading else { return }
        let requests: [(project: String, cursor: String)] = loadedProjects.compactMap { project in
            guard selectedProject == nil || selectedProject == project,
                  let cursor = historyLoads[project]?.nextCursor, !cursor.isEmpty
            else { return nil }
            return (project, cursor)
        }
        guard !requests.isEmpty else { return }
        isLoadingMoreHistory = true
        defer { isLoadingMoreHistory = false }
        let generation = refreshGeneration
        let source = source
        let pages = await withTaskGroup(
            of: (String, Result<SessionHistoryPage, Error>).self
        ) { group -> [String: Result<SessionHistoryPage, Error>] in
            for request in requests {
                group.addTask {
                    do {
                        let page = try await source.listSessionHistory(
                            project: request.project, limit: 50, cursor: request.cursor)
                        return (request.project, .success(page))
                    } catch {
                        return (request.project, .failure(error))
                    }
                }
            }
            var results: [String: Result<SessionHistoryPage, Error>] = [:]
            for await (project, result) in group { results[project] = result }
            return results
        }
        // A refresh that finished meanwhile owns newer first pages; an older
        // cursor page must not be merged into them.
        guard generation == refreshGeneration else { return }
        var loads = historyLoads
        var failures: [String] = []
        for request in requests {
            switch pages[request.project] {
            case .success(let page)?:
                guard var load = loads[request.project] else { continue }
                for row in page.sessions {
                    if let index = load.sessions.firstIndex(where: { $0.sessionID == row.sessionID }) {
                        load.sessions[index] = row
                    } else {
                        load.sessions.append(row)
                    }
                }
                load.nextCursor = page.nextCursor
                loads[request.project] = load
            case .failure(YccError.unauthorized)?:
                unauthorized = true
                return
            case .failure?:
                failures.append(request.project)
            case nil:
                continue
            }
        }
        apply(loads: loadedProjects.compactMap { loads[$0] })
        if !failures.isEmpty {
            partialWarning = "Couldn’t load older sessions for \(failures.joined(separator: ", "))."
        }
        readMarks.noteSeen(historyLoads.values.flatMap { $0.sessions + $0.pinned })
    }

    /// Daemon-wide live-activity counts, for the drawer's "Recent sessions" row.
    public var totalActivity: ProjectActivity { activitySnapshot.total }

    /// Activity for one project (zero when it has none).
    public func activity(forProject name: String) -> ProjectActivity {
        activitySnapshot.byProject[name] ?? ProjectActivity()
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
        dataRevision &+= 1
        for project in historyLoads.keys {
            if let i = historyLoads[project]?.sessions.firstIndex(where: { $0.sessionID == sessionID }) {
                historyLoads[project]?.sessions[i].waitingInput = false
            }
            if let i = historyLoads[project]?.pinned.firstIndex(where: { $0.sessionID == sessionID }) {
                historyLoads[project]?.pinned[i].waitingInput = false
            }
        }
    }

    /// (Re)load the project list and every project's history. The aggregate feed
    /// queries each distinct registered workspace once, merges and deduplicates
    /// the results, and keeps successful projects when another project fails —
    /// so the drawer's badges stay accurate no matter which project is selected.
    /// Concurrent callers await the same load rather than issuing overlapping
    /// foreground POST bursts.
    ///
    /// `force: false` is for passive triggers (returning to the list,
    /// foregrounding): it is skipped when a successful refresh completed within
    /// the last ``staleAfter`` seconds. Pull-to-refresh and post-mutation
    /// reloads use the default forced refresh.
    public func refresh(force: Bool = true) async {
        if let refreshTask {
            await refreshTask.value
            return
        }
        if !force, let lastSuccessfulRefresh {
            let elapsed = clock() - lastSuccessfulRefresh
            if elapsed >= 0 && elapsed < staleAfter { return }
        }

        refreshGeneration += 1
        let task = Task { await performRefresh() }
        refreshTask = task
        await task.value
        refreshTask = nil
    }

    nonisolated private static let clockOrigin = ContinuousClock.now

    /// Seconds on a clock that includes time the device spent asleep.
    public nonisolated static func continuousSeconds() -> TimeInterval {
        let components = clockOrigin.duration(to: ContinuousClock.now).components
        return TimeInterval(components.seconds) + TimeInterval(components.attoseconds) / 1e18
    }

    /// A throttled ``refresh(force:)`` for passive triggers.
    public func refreshIfStale() async {
        await refresh(force: false)
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
            hasLoadedProjects = true
            if loaded != projects { projects = loaded }
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            // A background badge refresh is best-effort and must not blank the
            // drawer or replace a useful screen-level error.
        }
    }

    private func performRefresh() async {
        let loadSpan = LatencyDiagnostics.shared.begin("home.load")
        isLoading = true
        defer {
            isLoading = false
            loadSpan.end(rows: allSessions.count)
        }
        unauthorized = false
        if await refreshHistories(cachedProjects: projects) {
            lastSuccessfulRefresh = clock()
        }
    }

    /// The project argument needed to open or resume a loaded row.
    public func project(for session: Ycc_V1_SessionSummary) -> String {
        sessionProjects[session.sessionID] ?? selectedProject ?? ""
    }

    /// The project name to SHOW on a row in the unscoped feed. Falls back to the
    /// session's workspace folder name when routing yielded no registered name
    /// (e.g. a daemon serving its startup workspace without a registration), so
    /// the Recent feed always says where a row lives. Display-only: routing RPCs
    /// keep using `project(for:)`, where an empty name means "resolve
    /// server-side".
    public func displayProject(for session: Ycc_V1_SessionSummary) -> String {
        let routed = project(for: session)
        if !routed.isEmpty { return routed }
        let workspace = session.workspace.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !workspace.isEmpty else { return "" }
        let base = (workspace as NSString).lastPathComponent
        return base == "/" ? "" : base
    }

    /// The history queries a project list implies. Project aliases that point at
    /// the same workspace would return the same event logs, so keep the first
    /// registration for a stable display/routing name. A daemon that reports no
    /// registered project can still own a session log for its startup
    /// workspace; query it by `fallback` (an empty name resolves server-side).
    private static func historyTargets(
        for projects: [Ycc_V1_ProjectInfo], fallback: String
    ) -> [String] {
        guard !projects.isEmpty else { return [fallback] }
        var seenPaths = Set<String>()
        return projects.compactMap { project in
            let path = project.path.trimmingCharacters(in: .whitespacesAndNewlines)
            let identity = path.isEmpty ? "name:\(project.name)" : "path:\(path)"
            return seenPaths.insert(identity).inserted ? project.name : nil
        }
    }

    /// Start one project's history page and its supplemental work-loop query.
    nonisolated private static func addFetches(
        for project: String,
        source: SessionListSource,
        retryDelays: [TimeInterval],
        to group: inout TaskGroup<HistoryUpdate>
    ) {
        group.addTask {
            let span = LatencyDiagnostics.shared.begin("home.history")
            do {
                let history = try await Self.retrying(delays: retryDelays) {
                    try await source.listSessionHistory(project: project, limit: 50, cursor: "")
                }
                span.end(rows: history.sessions.count)
                return .history(HistoryLoad(project: project, sessions: history.sessions,
                    pinned: history.pinned, nextCursor: history.nextCursor, hasHistory: true))
            } catch YccError.unauthorized {
                span.end()
                return .history(HistoryLoad(project: project, unauthorized: true))
            } catch {
                span.end()
                return .history(HistoryLoad(
                    project: project,
                    error: (error as? YccError)?.displayMessage ?? error.localizedDescription))
            }
        }
        group.addTask {
            let span = LatencyDiagnostics.shared.begin("home.workloop")
            do {
                let loop = try await source.workLoop(project: project)
                let ids = Self.loopSessionIDs(from: loop)
                span.end(rows: ids.count)
                return .loop(project: project, ids: ids)
            } catch {
                span.end()
                return .loop(project: project, ids: nil)
            }
        }
    }

    /// One refresh round. With a cached project list the per-project history
    /// and work-loop queries start immediately, concurrently with ListProjects
    /// (one round trip instead of two); when ListProjects answers, newly
    /// registered projects are fetched and removed ones dropped. On first load
    /// there is nothing cached, so the fan-out waits for ListProjects.
    ///
    /// The merged result is published once, when every project has answered —
    /// not once per project — unless the partial-publication valve fires first.
    /// Returns whether the refresh succeeded (for the passive-refresh throttle).
    private func refreshHistories(cachedProjects: [Ycc_V1_ProjectInfo]) async -> Bool {
        let source = source
        let retryDelays = retryDelays
        let fallback = selectedProject ?? ""
        var targets = cachedProjects.isEmpty
            ? [] : Self.historyTargets(for: cachedProjects, fallback: fallback)
        var byProject: [String: HistoryLoad] = [:]
        var pendingHistories = Set<String>()
        var projectsResolved = false
        var projectsError: String?
        var progressive = false
        var receivedHistory = false
        var finished = false
        var succeeded = false
        let partialDelay = partialPublishDelay
        let valve: Task<Void, Never>? = partialDelay.map { delay in
            Task {
                let nanoseconds = UInt64(max(0, min(delay, 3_600)) * 1_000_000_000)
                try? await Task<Never, Never>.sleep(nanoseconds: nanoseconds)
            }
        }
        defer { valve?.cancel() }

        // Seed each target with its pre-refresh snapshot, so a pending or failed
        // project keeps its rows (and routing) in every publication.
        func seed(_ project: String) {
            // Keep existing badges until their supplemental request completes.
            let ids = Set(loopSessionIDs.filter { sessionProjects[$0] == project })
            byProject[project] = HistoryLoad(
                project: project,
                sessions: historyLoads[project]?.sessions ?? [],
                pinned: historyLoads[project]?.pinned ?? [],
                nextCursor: historyLoads[project]?.nextCursor ?? "",
                loopSessionIDs: ids,
                hasHistory: loadedProjects.contains(project))
            pendingHistories.insert(project)
        }

        // Apply in registry order, not completion order: duplicate IDs and
        // equal timestamps must converge to the same result.
        func publish() {
            apply(loads: targets.compactMap { byProject[$0] })
        }

        func finishIfComplete() {
            guard !finished, projectsResolved, pendingHistories.isEmpty else { return }
            finished = true
            valve?.cancel()
            if !targets.isEmpty {
                publish()
                // One aggregate baseline per refresh: advancing the shared
                // watermark for partial results would make unread status
                // depend on which project happened to finish first.
                readMarks.noteSeen(historyLoads.values.flatMap { $0.sessions + $0.pinned })
            }
            if let projectsError {
                // The project list itself failed: report it (histories for the
                // cached projects above are still shown).
                errorMessage = projectsError
            } else {
                succeeded = errorMessage == nil
            }
            isLoading = false
        }

        func stop() {
            unauthorized = true
            valve?.cancel()
        }

        await withTaskGroup(of: HistoryUpdate.self) { group in
            group.addTask {
                do {
                    return .projects(try await Self.retrying(delays: retryDelays) {
                        try await source.listProjects()
                    })
                } catch YccError.unauthorized {
                    return .projectsFailed(message: nil)
                } catch let YccError.rpc(message) {
                    return .projectsFailed(message: message)
                } catch {
                    return .projectsFailed(message: error.localizedDescription)
                }
            }
            if let valve {
                group.addTask {
                    await valve.value
                    return .publishPartial
                }
            }
            for project in targets {
                seed(project)
                Self.addFetches(for: project, source: source, retryDelays: retryDelays, to: &group)
            }

            for await update in group {
                guard !unauthorized else { continue }
                switch update {
                case .projects(let loaded):
                    hasLoadedProjects = true
                    if loaded != projects { projects = loaded }
                    let resolved = Self.historyTargets(for: loaded, fallback: fallback)
                    let known = Set(targets)
                    // Projects removed meanwhile: drop them; their in-flight
                    // results are ignored when they arrive.
                    let kept = Set(resolved)
                    for project in targets where !kept.contains(project) {
                        byProject.removeValue(forKey: project)
                        pendingHistories.remove(project)
                    }
                    targets = resolved
                    for project in resolved where !known.contains(project) {
                        seed(project)
                        Self.addFetches(for: project, source: source, retryDelays: retryDelays, to: &group)
                    }
                    projectsResolved = true
                    finishIfComplete()
                    if !finished, progressive { publish() }
                case .projectsFailed(let message):
                    guard let message else {
                        stop()
                        group.cancelAll()
                        continue
                    }
                    projectsError = message
                    projectsResolved = true
                    finishIfComplete()
                case .history(var load):
                    guard pendingHistories.contains(load.project) else { continue }
                    if load.unauthorized {
                        stop()
                        group.cancelAll()
                        continue
                    }
                    let previous = byProject[load.project]
                    load.loopSessionIDs = previous?.loopSessionIDs ?? []
                    if load.error != nil {
                        load.sessions = previous?.sessions ?? []
                        load.pinned = previous?.pinned ?? []
                        load.nextCursor = previous?.nextCursor ?? ""
                        load.hasHistory = previous?.hasHistory ?? false
                    }
                    byProject[load.project] = load
                    pendingHistories.remove(load.project)
                    receivedHistory = true
                    finishIfComplete()
                    if !finished, progressive { publish() }
                case let .loop(project, ids):
                    guard let ids, byProject[project] != nil else { continue }
                    byProject[project]?.loopSessionIDs = ids
                    // Before the publication the badges ride along with it;
                    // afterwards a late badge must not re-sort/re-baseline.
                    guard finished || progressive else { continue }
                    let merged = targets.reduce(into: Set<String>()) { result, project in
                        result.formUnion(byProject[project]?.loopSessionIDs ?? [])
                    }
                    if merged != loopSessionIDs { loopSessionIDs = merged }
                case .publishPartial:
                    guard !finished, !progressive else { continue }
                    progressive = true
                    if receivedHistory { publish() }
                }
            }
        }
        return succeeded && !unauthorized
    }

    /// Merge per-project history loads into the aggregate feed, its routing
    /// table, the drawer's activity counts, and the error/partial-warning state.
    /// Unchanged values are not reassigned, so an idempotent refresh (nothing
    /// new on the daemon) does not invalidate or re-render the list.
    private func apply(loads: [HistoryLoad]) {
        publicationCount += 1
        // Pending/failed projects carry their pre-refresh snapshot in loads,
        // independent of any temporary deduplication during partial publication.
        var merged: [Ycc_V1_SessionSummary] = []
        var routes: [String: String] = [:]
        var seenSessionIDs = Set<String>()
        var succeeded: [String] = []
        let newLoads = Dictionary(loads.map { ($0.project, $0) }, uniquingKeysWith: { _, last in last })
        // A globally sorted feed cannot show a row below a truncated project's
        // oldest loaded row: that project may have intervening unseen rows.
        let frontiers = loads.filter { $0.hasHistory && !$0.nextCursor.isEmpty }
            .compactMap { Self.sortedByRecency($0.sessions).last }
        let frontier = Self.sortedByRecency(frontiers).first
        for load in loads where load.hasHistory {
            succeeded.append(load.project)
            for session in load.sessions {
                if let frontier, Self.precedes(frontier, session) { continue }
                if seenSessionIDs.insert(session.sessionID).inserted {
                    merged.append(session)
                    routes[session.sessionID] = load.project
                }
            }
            for session in load.pinned where seenSessionIDs.insert(session.sessionID).inserted {
                merged.append(session)
                routes[session.sessionID] = load.project
            }
        }
        let sorted = Self.sortedByRecency(merged)
        var changed = false
        if newLoads != historyLoads { historyLoads = newLoads; changed = true }
        if sorted != allSessions { allSessions = sorted; changed = true }
        if routes != sessionProjects { sessionProjects = routes; changed = true }
        if succeeded != loadedProjects { loadedProjects = succeeded; changed = true }
        if changed { dataRevision &+= 1 }
        let loopIDs = loads.reduce(into: Set<String>()) { ids, load in
            ids.formUnion(load.loopSessionIDs)
        }
        if loopIDs != loopSessionIDs { loopSessionIDs = loopIDs }

        let failed = loads.filter { $0.error != nil }
        let warning: String?
        let error: String?
        if failed.isEmpty {
            warning = nil
            error = nil
        } else if failed.count < loads.count {
            let names = failed.map(\.project)
            warning = "Some projects couldn’t be loaded: \(names.joined(separator: ", "))."
            error = nil
        } else {
            warning = nil
            error = failed.first?.error ?? "Couldn’t load sessions."
        }
        if warning != partialWarning { partialWarning = warning }
        if error != errorMessage { errorMessage = error }
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
        // Idle while delegated work will still resume it: active, not finished.
        case .idle where session.awaitingJobs: counts.active += 1
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
    /// session ID ascending for ties (matching the daemon's cursor order).
    public static func sections(from sessions: [Ycc_V1_SessionSummary]) -> [SessionSection] {
        sectionsFromSorted(sortedByRecency(sessions))
    }

    private static func sectionsFromSorted(_ sessions: [Ycc_V1_SessionSummary]) -> [SessionSection] {
        // Stable partition of the already recency-sorted input.
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
                sessions: needsAnswer))
        }
        if !rest.isEmpty {
            out.append(SessionSection(
                kind: .all,
                // Only label the remainder when there's a needs-answer section
                // above it to distinguish from.
                title: needsAnswer.isEmpty ? nil : "All sessions",
                sessions: rest))
        }
        return out
    }

    /// Match the daemon's keyset ordering, including timestamp ties.
    static func precedes(_ a: Ycc_V1_SessionSummary, _ b: Ycc_V1_SessionSummary) -> Bool {
        let activityA = recencyDate(a) ?? .distantPast
        let activityB = recencyDate(b) ?? .distantPast
        if activityA != activityB { return activityA > activityB }
        let startA = parseTimestamp(a.startedAt) ?? .distantPast
        let startB = parseTimestamp(b.startedAt) ?? .distantPast
        if startA != startB { return startA > startB }
        return a.sessionID < b.sessionID
    }

    static func sortedByRecency(_ sessions: [Ycc_V1_SessionSummary]) -> [Ycc_V1_SessionSummary] {
        // Date parsing is much costlier than comparison; parse once per row.
        sessions.map { row in
            (row: row, activity: recencyDate(row) ?? .distantPast,
                start: parseTimestamp(row.startedAt) ?? .distantPast)
        }.sorted { a, b in
            if a.activity != b.activity { return a.activity > b.activity }
            if a.start != b.start { return a.start > b.start }
            return a.row.sessionID < b.row.sessionID
        }.map(\.row)
    }

    /// The date to sort a session by: `lastActivity`, falling back to
    /// `startedAt`. Returns `nil` when neither parses.
    public static func recencyDate(_ session: Ycc_V1_SessionSummary) -> Date? {
        parseTimestamp(session.lastActivity) ?? parseTimestamp(session.startedAt)
    }

    /// Parse an RFC3339 / ISO8601 timestamp. Daemon timestamps may carry
    /// fractional seconds and a numeric offset, so try with fractional seconds
    /// first, then without. Empty / unparseable input returns `nil`.
    ///
    /// Results are memoized: the same daemon stamps are parsed over and over
    /// (sorting, frontier checks, unread comparisons against read marks), and
    /// ISO-8601 parsing dominates those paths. The cache is bounded and
    /// thread-safe.
    static func parseTimestamp(_ value: String) -> Date? {
        if value.isEmpty { return nil }
        return timestampCache.value(for: value) { text in
            isoWithFraction.date(from: text) ?? isoPlain.date(from: text)
        }
    }

    private static let timestampCache = MemoCache<Date?>(limit: 4_096)

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
            // Both patterns are anchored at the start; skip the regex entirely
            // for the (common) titles that cannot match.
            guard title.hasPrefix(id)
                || title.prefix(4).caseInsensitiveCompare("work") == .orderedSame
            else { continue }
            for expression in taskPrefixExpressions(for: id) {
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

    /// Compiled task-boilerplate expressions per task id, cached because
    /// ``displayTitle(for:)`` runs for every row on every list render.
    private static func taskPrefixExpressions(for id: String) -> [NSRegularExpression] {
        let expressions = taskPrefixCache.value(for: id) { id -> [NSRegularExpression]? in
            let escaped = NSRegularExpression.escapedPattern(for: id)
            let patterns = [
                #"(?i)^work\s+on\s+task\s+"# + escaped + #"\s*[:\-–—]\s*"#,
                #"^"# + escaped + #"\s*[:\-–—]\s*"#,
            ]
            return patterns.compactMap { try? NSRegularExpression(pattern: $0) }
        }
        return expressions ?? []
    }

    private static let taskPrefixCache = MemoCache<[NSRegularExpression]?>(limit: 512)

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

    /// Human-sized context length for the metadata line — how full the
    /// session's active conversation is (the daemon's estimate from the newest
    /// coordinator model turn), not cumulative spend. Zero/missing telemetry
    /// (older daemons or logs, or no completed turn yet) is omitted rather than
    /// presented as a misleading `0 ctx`.
    public static func contextSummary(for session: Ycc_V1_SessionSummary) -> String? {
        guard let value = compactTokenCount(session.contextTokens) else { return nil }
        return "\(value) ctx"
    }

    /// Human-sized token count ("999", "12K", "9.6M", "1B"), or nil for
    /// zero/negative input.
    static func compactTokenCount(_ tokens: Int64) -> String? {
        guard tokens > 0 else { return nil }
        if tokens < 1_000 {
            return String(tokens)
        } else if tokens < 999_500 {
            return compactDecimal(Double(tokens) / 1_000) + "K"
        } else if tokens < 999_500_000 {
            // Promote values whose compact rounding would otherwise say 1000K.
            return compactDecimal(Double(tokens) / 1_000_000) + "M"
        }
        // Likewise, never display 1000M at the next unit boundary.
        return compactDecimal(Double(tokens) / 1_000_000_000) + "B"
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
        // An idle coordinator whose subagent / background job still runs.
        if session.live && session.awaitingJobs { return "background" }
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
        if let context = contextSummary(for: session) { items.append(context) }
        if isLoopOwned { items.append("via loop") }
        return items
    }

}

/// A small thread-safe memo table for pure, string-keyed derivations (parsed
/// timestamps, compiled regexes). Cleared wholesale when it reaches `limit`,
/// which keeps it bounded without per-entry bookkeeping.
final class MemoCache<Value>: @unchecked Sendable {
    private let lock = NSLock()
    private var storage: [String: Value] = [:]
    private let limit: Int

    init(limit: Int) { self.limit = max(1, limit) }

    func value(for key: String, compute: (String) -> Value) -> Value {
        lock.lock()
        if let cached = storage[key] {
            lock.unlock()
            return cached
        }
        lock.unlock()
        let computed = compute(key)
        lock.lock()
        if storage.count >= limit { storage.removeAll(keepingCapacity: true) }
        storage[key] = computed
        lock.unlock()
        return computed
    }
}
