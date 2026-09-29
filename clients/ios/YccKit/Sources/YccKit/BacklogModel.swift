import Foundation
import Observation
import YccProto

/// The data source a ``BacklogModel`` reads from and drives. Abstracting it
/// behind a protocol lets the sectioning / validation logic be unit-tested
/// headlessly with an in-memory mock — no network, no simulator. ``YccClient``
/// is the production conformer. (Mirrors the ``SessionListSource`` pattern.)
public protocol BacklogSource: Sendable {
    /// List the backlog for a named project.
    func listBacklog(project: String) async throws -> [Ycc_V1_BacklogTaskSummary]
    /// List the daemon's registered projects (drives the project filter).
    func listProjects() async throws -> [Ycc_V1_ProjectInfo]
    /// Create a task from a title, markdown body, and P1–P5 priority; returns its detail.
    func createTask(
        project: String, title: String, body: String, priority: Int
    ) async throws -> Ycc_V1_TaskDetail
    /// Change a task's status; returns the refreshed detail.
    func updateTaskStatus(project: String, id: String, status: String) async throws -> Ycc_V1_TaskDetail
}

extension YccClient: BacklogSource {}

/// A task's lifecycle status (internal/docs), parsed from the
/// daemon's free-form `status` string. Kept here so the section ordering, colour
/// mapping, and the status-picker choices are a single, unit-testable source of
/// truth. Unknown strings fall back to ``unknown`` rather than crashing.
public enum TaskStatus: String, Sendable, CaseIterable, Identifiable {
    case proposed
    case todo
    case inProgress = "in_progress"
    case inReview = "in_review"
    case blocked
    case done
    case unknown

    public var id: String { rawValue }

    public init(status: String) {
        self = TaskStatus(rawValue: status.lowercased()) ?? .unknown
    }

    /// The statuses a user can pick in the status editor (the
    /// daemon's UpdateTask validation accepts these six; `unknown` is excluded).
    public static var selectable: [TaskStatus] {
        [.proposed, .todo, .inProgress, .inReview, .blocked, .done]
    }

    /// A human-facing label for pills and pickers.
    public var title: String {
        switch self {
        case .proposed: return "Proposed"
        case .todo: return "Todo"
        case .inProgress: return "In progress"
        case .inReview: return "In review"
        case .blocked: return "Blocked"
        case .done: return "Done"
        case .unknown: return "Unknown"
        }
    }

    /// Section ordering: active work first, then queued, then blocked/proposed,
    /// with done trailing (it is included in ListBacklog output). Lower sorts
    /// first. ``unknown`` sorts just before done so odd rows stay visible.
    public var sortOrder: Int {
        switch self {
        case .inProgress: return 0
        case .inReview: return 1
        case .todo: return 2
        case .blocked: return 3
        case .proposed: return 4
        case .unknown: return 5
        case .done: return 6
        }
    }

    /// Column ordering for the board, which is *workflow* order rather than the
    /// list's "most interesting first" order: a card moves left to right as the
    /// work progresses, which is the whole point of a kanban lane.
    public var boardOrder: Int {
        switch self {
        case .proposed: return 0
        case .todo: return 1
        case .inProgress: return 2
        case .inReview: return 3
        case .blocked: return 4
        case .done: return 5
        case .unknown: return 6
        }
    }

    /// The board's lanes, left to right. ``unknown`` is not a lane; it is
    /// appended only when tasks actually carry an unrecognised status.
    public static var boardColumns: [TaskStatus] {
        selectable.sorted { $0.boardOrder < $1.boardOrder }
    }

    /// The lane immediately before/after this one, for "move the card left/right"
    /// actions. Returns nil at the ends (and for ``unknown``, which is off-board).
    public var previousBoardColumn: TaskStatus? {
        let columns = Self.boardColumns
        guard let index = columns.firstIndex(of: self), index > 0 else { return nil }
        return columns[index - 1]
    }

    public var nextBoardColumn: TaskStatus? {
        let columns = Self.boardColumns
        guard let index = columns.firstIndex(of: self), index + 1 < columns.count else { return nil }
        return columns[index + 1]
    }
}

/// The ordering used for cards within a board lane and rows within a list
/// section. Backlog ids are allocated monotonically, so id-descending is the
/// closest available proxy for creation time.
public enum BacklogSort: String, Sendable, CaseIterable, Identifiable {
    case newestFirst
    case oldestFirst
    case priority

    public var id: String { rawValue }

    public var title: String {
        switch self {
        case .newestFirst: return "Newest first"
        case .oldestFirst: return "Oldest first"
        case .priority: return "Priority"
        }
    }
}

/// A group of backlog tasks sharing a status, for a sectioned list.
public struct BacklogSection: Identifiable, Sendable {
    public let status: TaskStatus
    public let tasks: [Ycc_V1_BacklogTaskSummary]
    public var id: String { status.rawValue }
    public var title: String { status.title }
}

/// Drives the backlog browser: loads ``ListBacklog`` (plus ``ListProjects``
/// only when no app-level project list is cached), holds the selected project
/// filter, groups tasks into ordered status sections, and handles quick-capture
/// (`CreateTask`) and in-place status changes. The data source is injected
/// (``BacklogSource``) so the sectioning / validation logic is testable
/// headlessly. `@MainActor` because it publishes observable UI state.
///
/// Remote-link behaviour: the model seeds itself from ``AppDataCache`` so a
/// revisited backlog renders instantly while it revalidates; status changes are
/// applied optimistically (rolled back with an error on failure) and followed
/// by a background, backlog-only refresh; a captured task is inserted from the
/// `CreateTask` response rather than waiting for a reload.
@MainActor
@Observable
public final class BacklogModel {
    /// Raw tasks from the last successful load (view reads ``sections``).
    public private(set) var tasks: [Ycc_V1_BacklogTaskSummary] = []
    /// Registered projects; drives the project filter menu.
    public private(set) var projects: [Ycc_V1_ProjectInfo] = []
    /// The selected registered project. Empty means no choice has been made yet.
    /// Setting it does not auto-refresh — the view calls ``refresh()``.
    public var selectedProject: String = ""
    /// Ordering within each status section or board lane.
    public var sort: BacklogSort = .newestFirst

    public private(set) var isLoading = false
    public private(set) var errorMessage: String?
    /// Set when a load failed with ``YccError/unauthorized``; the view observes
    /// this to route back to the connect screen via `AppModel.handleUnauthorized`.
    public private(set) var unauthorized = false

    /// Set while a quick-capture create is in flight (disables the Save button).
    public private(set) var isCreating = false
    /// A quick-capture failure message, surfaced inline in the capture sheet.
    public private(set) var createError: String?

    /// Tasks whose status change is in flight. Each row's own menu is disabled
    /// meanwhile; other rows stay fully interactive.
    public private(set) var updatingTaskIDs: Set<String> = []
    /// A status-change failure message, surfaced as an alert over the list.
    public var updateError: String?

    /// Whether `taskID` has a status change in flight.
    public func isUpdating(_ taskID: String) -> Bool { updatingTaskIDs.contains(taskID) }

    /// Compatibility: some task with a status change in flight, if any.
    public var updatingTaskID: String? { updatingTaskIDs.first }

    private let source: BacklogSource
    private let cache: AppDataCache?
    private let cacheGeneration: UInt64
    /// The project whose backlog ``tasks`` currently shows.
    private var loadedProject: String?
    /// Optimistic statuses of in-flight updates, re-applied over any list that
    /// lands while they are outstanding so a refresh cannot flicker them back.
    private var optimisticStatuses: [String: String] = [:]
    private var activeLoads = 0
    private var backgroundRefreshPending = false
    /// The coalesced backlog-only revalidation started after a mutation.
    @ObservationIgnored private(set) var backgroundRefreshTask: Task<Void, Never>?

    public init(source: BacklogSource, selectedProject: String = "", cache: AppDataCache? = nil) {
        self.source = source
        self.cache = cache
        self.cacheGeneration = cache?.generation ?? 0
        self.selectedProject = selectedProject
        if let cachedProjects = cache?.projects {
            projects = cachedProjects
            if self.selectedProject.isEmpty, cachedProjects.count == 1 {
                self.selectedProject = cachedProjects[0].name
            }
        }
        seedFromCache(for: self.selectedProject)
    }

    /// The project picker is useful only when there is a real choice.
    public var showsProjectFilter: Bool { projects.count > 1 }

    /// Tasks grouped into ordered status sections.
    public var sections: [BacklogSection] { Self.sections(from: tasks, sort: sort) }

    /// Tasks grouped into board lanes, in workflow order.
    public var board: [BacklogSection] { Self.board(from: tasks, sort: sort) }

    /// (Re)load the backlog for the selected project. The project list comes
    /// from the app-level cache when available; otherwise it is fetched
    /// concurrently but never delays the backlog itself. A load superseded by a
    /// project switch is discarded. Unauthorized bubbles up via
    /// ``unauthorized`` for the view to handle.
    public func refresh() async {
        let project = selectedProject
        if project != loadedProject { seedFromCache(for: project) }
        if let cachedProjects = cache?.projects, cachedProjects != projects {
            projects = cachedProjects
        }
        let fetchProjects = cache?.projects == nil
        activeLoads += 1
        isLoading = true
        defer {
            activeLoads -= 1
            isLoading = activeLoads > 0
        }
        async let projectList = Self.projects(from: source, fetch: fetchProjects)
        do {
            let loaded = try await source.listBacklog(project: project)
            if project == selectedProject {
                apply(loaded, project: project)
                errorMessage = nil
            }
        } catch YccError.unauthorized {
            unauthorized = true
        } catch let YccError.rpc(message) {
            errorMessage = message
        } catch {
            errorMessage = (error as? YccError)?.displayMessage ?? error.localizedDescription
        }
        guard fetchProjects else { return }
        do {
            if let loadedProjects = try await projectList {
                projects = loadedProjects
                cache?.updateProjects(loadedProjects, ifGeneration: cacheGeneration)
                if selectedProject.isEmpty, loadedProjects.count == 1 {
                    // A sole project: adopt it without refetching — the empty
                    // project request already resolved to it.
                    selectedProject = loadedProjects[0].name
                    if loadedProject == "" { loadedProject = selectedProject }
                    storeCache()
                }
            }
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            // The project list only drives the filter menu; keep the backlog.
        }
    }

    nonisolated private static func projects(
        from source: BacklogSource, fetch: Bool
    ) async throws -> [Ycc_V1_ProjectInfo]? {
        guard fetch else { return nil }
        return try await source.listProjects()
    }

    /// Whether a quick-capture create is allowed: a non-blank title and no create
    /// already in flight.
    public func canCreate(title: String) -> Bool {
        !isCreating && !title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// Quick-capture: create a task from a title, markdown body, and P1–P5
    /// priority. The created task is inserted from the response straight away
    /// (the sheet can dismiss after one round trip) and the list revalidates in
    /// the background. Returns `true` on success. On failure sets
    /// ``createError`` / ``unauthorized`` and returns `false`. Invalid input is
    /// rejected client-side without a round-trip.
    public func create(title: String, body: String, priority: Int = 3) async -> Bool {
        let trimmedTitle = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedTitle.isEmpty, (1...5).contains(priority), !isCreating else { return false }
        isCreating = true
        defer { isCreating = false }
        let project = selectedProject
        do {
            let detail = try await source.createTask(
                project: project,
                title: trimmedTitle,
                body: body.trimmingCharacters(in: .whitespacesAndNewlines),
                priority: priority)
            createError = nil
            if project == selectedProject, !detail.id.isEmpty,
               !tasks.contains(where: { $0.id == detail.id }) {
                tasks.append(Self.summary(from: detail))
                storeCache()
            }
            scheduleBackgroundRefresh()
            return true
        } catch YccError.unauthorized {
            unauthorized = true
            return false
        } catch let YccError.rpc(message) {
            createError = message
            return false
        } catch let YccError.notFound(message) {
            createError = message
            return false
        } catch let YccError.failedPrecondition(message) {
            createError = message
            return false
        } catch {
            createError = error.localizedDescription
            return false
        }
    }

    /// Clear any lingering quick-capture error (called when the sheet opens).
    public func clearCreateError() { createError = nil }

    /// Change a task's status straight from the list (`UpdateTask` with only the
    /// status field set). The row moves to its new section immediately; the
    /// response then patches it with the daemon's canonical fields and a
    /// background backlog-only refresh picks up knock-on changes (a completed
    /// dependency flips other rows' ready/blocked flags). A no-op (returning
    /// `true`) when the status is unchanged; a second change to the same row
    /// while one is in flight is rejected. On failure the row reverts and
    /// ``updateError`` / ``unauthorized`` is set.
    @discardableResult
    public func setStatus(taskID: String, to newStatus: TaskStatus) async -> Bool {
        guard newStatus != .unknown, !updatingTaskIDs.contains(taskID) else { return false }
        let project = selectedProject
        let index = tasks.firstIndex(where: { $0.id == taskID })
        if let index, TaskStatus(status: tasks[index].status) == newStatus {
            return true
        }
        let previousStatus = index.map { tasks[$0].status }
        if let index { tasks[index].status = newStatus.rawValue }
        optimisticStatuses[taskID] = newStatus.rawValue
        updatingTaskIDs.insert(taskID)
        storeCache()
        do {
            let detail = try await source.updateTaskStatus(
                project: project, id: taskID, status: newStatus.rawValue)
            updatingTaskIDs.remove(taskID)
            optimisticStatuses.removeValue(forKey: taskID)
            if project == selectedProject,
               let current = tasks.firstIndex(where: { $0.id == taskID }) {
                tasks[current].status = detail.status
                tasks[current].title = detail.title
                tasks[current].priority = detail.priority
                tasks[current].ready = detail.ready
                tasks[current].blockedBy = detail.blockedBy
                storeCache()
            }
            updateError = nil
            scheduleBackgroundRefresh()
            return true
        } catch {
            updatingTaskIDs.remove(taskID)
            optimisticStatuses.removeValue(forKey: taskID)
            if project == selectedProject {
                if let previousStatus,
                   let current = tasks.firstIndex(where: { $0.id == taskID }),
                   tasks[current].status == newStatus.rawValue {
                    tasks[current].status = previousStatus
                }
                storeCache()
            } else {
                // The optimistic value may have been cached for the project the
                // user has since left; drop it rather than persist a lie.
                cache?.removeValue(for: .backlog(project))
            }
            switch error {
            case YccError.unauthorized:
                unauthorized = true
            case let YccError.rpc(message), let YccError.notFound(message),
                 let YccError.failedPrecondition(message):
                updateError = message
            default:
                updateError = error.localizedDescription
            }
            return false
        }
    }

    // MARK: - Cache & background revalidation

    private func seedFromCache(for project: String) {
        guard let cached = cache?.value(.backlog(project), as: [Ycc_V1_BacklogTaskSummary].self) else {
            return
        }
        tasks = cached
        loadedProject = project
    }

    private func storeCache() {
        guard let loadedProject else { return }
        cache?.store(tasks, for: .backlog(loadedProject), ifGeneration: cacheGeneration)
    }

    /// Install a freshly loaded list, keeping in-flight optimistic statuses.
    private func apply(_ loaded: [Ycc_V1_BacklogTaskSummary], project: String) {
        var merged = loaded
        if !optimisticStatuses.isEmpty {
            for index in merged.indices {
                if let status = optimisticStatuses[merged[index].id] {
                    merged[index].status = status
                }
            }
        }
        if merged != tasks { tasks = merged }
        loadedProject = project
        storeCache()
    }

    /// Coalesce post-mutation revalidations: at most one backlog-only reload is
    /// in flight, and requests made meanwhile trigger exactly one more.
    private func scheduleBackgroundRefresh() {
        backgroundRefreshPending = true
        guard backgroundRefreshTask == nil else { return }
        backgroundRefreshTask = Task { @MainActor [weak self] in
            while true {
                guard let self, self.backgroundRefreshPending else { break }
                self.backgroundRefreshPending = false
                await self.revalidateBacklog()
            }
            self?.backgroundRefreshTask = nil
        }
    }

    private func revalidateBacklog() async {
        let project = selectedProject
        do {
            let loaded = try await source.listBacklog(project: project)
            guard project == selectedProject else { return }
            apply(loaded, project: project)
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            // Background revalidation is best-effort; the list keeps the
            // mutation's response and the next refresh retries.
        }
    }

    /// The list row for a created/updated task detail.
    static func summary(from detail: Ycc_V1_TaskDetail) -> Ycc_V1_BacklogTaskSummary {
        var summary = Ycc_V1_BacklogTaskSummary()
        summary.id = detail.id
        summary.title = detail.title
        summary.status = detail.status
        summary.priority = detail.priority
        summary.dependsOn = detail.dependsOn
        summary.ready = detail.ready
        summary.blockedBy = detail.blockedBy
        return summary
    }

    // MARK: - Pure logic (unit-tested)

    /// Return tasks in the selected display order. Numeric ids sort numerically
    /// before all non-numeric ids, with their original strings breaking numeric
    /// ties; non-numeric ids sort lexicographically. Exact id ties retain their
    /// incoming order.
    public static func sorted(
        _ tasks: [Ycc_V1_BacklogTaskSummary], by sort: BacklogSort
    ) -> [Ycc_V1_BacklogTaskSummary] {
        tasks.enumerated().sorted { lhs, rhs in
            if sort == .priority {
                let leftPriority = lhs.element.priority > 0 ? lhs.element.priority : Int32.max
                let rightPriority = rhs.element.priority > 0 ? rhs.element.priority : Int32.max
                if leftPriority != rightPriority { return leftPriority < rightPriority }
            }

            let idOrder = compareIDs(lhs.element.id, rhs.element.id)
            if idOrder != 0 {
                return sort == .oldestFirst ? idOrder < 0 : idOrder > 0
            }
            return lhs.offset < rhs.offset
        }.map(\.element)
    }

    /// Group tasks by status into ordered sections (active work first, done
    /// trailing — see ``TaskStatus/sortOrder``). Within a section, tasks use the
    /// selected display order, newest-first by default. Empty statuses produce no
    /// section.
    public static func sections(
        from tasks: [Ycc_V1_BacklogTaskSummary], sort: BacklogSort = .newestFirst
    ) -> [BacklogSection] {
        var byStatus: [TaskStatus: [Ycc_V1_BacklogTaskSummary]] = [:]
        var order: [TaskStatus] = []
        for task in sorted(tasks, by: sort) {
            let status = TaskStatus(status: task.status)
            if byStatus[status] == nil { order.append(status) }
            byStatus[status, default: []].append(task)
        }
        return order
            .sorted { $0.sortOrder < $1.sortOrder }
            .map { BacklogSection(status: $0, tasks: byStatus[$0] ?? []) }
    }

    /// Group tasks into board lanes in workflow order (see
    /// ``TaskStatus/boardOrder``), ordering cards within each lane newest-first by
    /// default. Unlike ``sections(from:)`` this keeps **empty lanes** — a board
    /// with a missing "In review" column stops being a board, and an empty lane is
    /// also the target you want to move a card into. A lane for ``unknown`` is
    /// appended only when some task actually has one.
    public static func board(
        from tasks: [Ycc_V1_BacklogTaskSummary], sort: BacklogSort = .newestFirst
    ) -> [BacklogSection] {
        var byStatus: [TaskStatus: [Ycc_V1_BacklogTaskSummary]] = [:]
        for task in sorted(tasks, by: sort) {
            byStatus[TaskStatus(status: task.status), default: []].append(task)
        }
        var lanes = TaskStatus.boardColumns.map {
            BacklogSection(status: $0, tasks: byStatus[$0] ?? [])
        }
        if let strays = byStatus[.unknown], !strays.isEmpty {
            lanes.append(BacklogSection(status: .unknown, tasks: strays))
        }
        return lanes
    }

    /// Ascending total id comparison: numeric ids first, then non-numeric ids.
    private static func compareIDs(_ lhs: String, _ rhs: String) -> Int {
        switch (Int(lhs), Int(rhs)) {
        case let (leftNumber?, rightNumber?) where leftNumber != rightNumber:
            return leftNumber < rightNumber ? -1 : 1
        case (_?, nil):
            return -1
        case (nil, _?):
            return 1
        default:
            if lhs == rhs { return 0 }
            return lhs < rhs ? -1 : 1
        }
    }

    /// A short readiness annotation for a summary row: `nil` when ready (no
    /// annotation needed), otherwise "Blocked by 0173, 0174" listing the
    /// not-yet-done dependencies. Done tasks are never annotated.
    public static func blockedAnnotation(for task: Ycc_V1_BacklogTaskSummary) -> String? {
        if task.ready || task.blockedBy.isEmpty { return nil }
        if TaskStatus(status: task.status) == .done { return nil }
        return "Blocked by " + task.blockedBy.joined(separator: ", ")
    }
}
