import Foundation
import Observation
import YccProto

/// The data source a ``BacklogModel`` reads from and drives. Abstracting it
/// behind a protocol lets the sectioning / validation logic be unit-tested
/// headlessly with an in-memory mock — no network, no simulator. ``YccClient``
/// is the production conformer. (Mirrors the ``SessionListSource`` pattern.)
public protocol BacklogSource: Sendable {
    /// List the backlog for a project (empty => daemon default workspace).
    func listBacklog(project: String) async throws -> [Ycc_V1_BacklogTaskSummary]
    /// List the daemon's registered projects (drives the project filter).
    func listProjects() async throws -> [Ycc_V1_ProjectInfo]
    /// Create a task from a title + markdown body; returns its detail.
    func createTask(project: String, title: String, body: String) async throws -> Ycc_V1_TaskDetail
}

extension YccClient: BacklogSource {}

/// A task's lifecycle status (spec §18.5 / internal/docs), parsed from the
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

    /// The statuses a user can pick in the status editor (spec §18.5 / the
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
}

/// A group of backlog tasks sharing a status, for a sectioned list.
public struct BacklogSection: Identifiable, Sendable {
    public let status: TaskStatus
    public let tasks: [Ycc_V1_BacklogTaskSummary]
    public var id: String { status.rawValue }
    public var title: String { status.title }
}

/// Drives the backlog browser (docs/design/ios-client.md §6 phase 2 step 6,
/// spec §18.5): loads ``ListBacklog`` + ``ListProjects``, holds the selected
/// project filter, groups tasks into ordered status sections, and handles
/// quick-capture (`CreateTask`). The data source is injected (``BacklogSource``)
/// so the sectioning / validation logic is testable headlessly. `@MainActor`
/// because it publishes observable UI state.
@MainActor
@Observable
public final class BacklogModel {
    /// Raw tasks from the last successful load (view reads ``sections``).
    public private(set) var tasks: [Ycc_V1_BacklogTaskSummary] = []
    /// Registered projects; drives the project filter menu.
    public private(set) var projects: [Ycc_V1_ProjectInfo] = []
    /// The selected project filter. `""` selects the daemon default workspace.
    /// Setting it does not auto-refresh — the view calls ``refresh()``.
    public var selectedProject: String = ""

    public private(set) var isLoading = false
    public private(set) var errorMessage: String?
    /// Set when a load failed with ``YccError/unauthorized``; the view observes
    /// this to route back to the connect screen via `AppModel.handleUnauthorized`.
    public private(set) var unauthorized = false

    /// Set while a quick-capture create is in flight (disables the Save button).
    public private(set) var isCreating = false
    /// A quick-capture failure message, surfaced inline in the capture sheet.
    public private(set) var createError: String?

    private let source: BacklogSource

    public init(source: BacklogSource, selectedProject: String = "") {
        self.source = source
        self.selectedProject = selectedProject
    }

    /// The project filter is only meaningful with more than one project.
    public var showsProjectFilter: Bool { projects.count > 1 }

    /// Tasks grouped into ordered status sections.
    public var sections: [BacklogSection] { Self.sections(from: tasks) }

    /// (Re)load the backlog for the selected project and the project list.
    /// Unauthorized bubbles up via ``unauthorized`` for the view to handle.
    public func refresh() async {
        isLoading = true
        defer { isLoading = false }
        do {
            async let backlog = source.listBacklog(project: selectedProject)
            async let projectList = source.listProjects()
            let (loaded, loadedProjects) = try await (backlog, projectList)
            tasks = loaded
            projects = loadedProjects
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch let YccError.rpc(message) {
            errorMessage = message
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    /// Whether a quick-capture create is allowed: a non-blank title and no create
    /// already in flight.
    public func canCreate(title: String) -> Bool {
        !isCreating && !title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// Quick-capture: create a task from a title + markdown body, then refresh the
    /// list so the new row appears. Returns `true` on success. On failure sets
    /// ``createError`` / ``unauthorized`` and returns `false`. A blank title is
    /// rejected client-side without a round-trip.
    public func create(title: String, body: String) async -> Bool {
        let trimmedTitle = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedTitle.isEmpty, !isCreating else { return false }
        isCreating = true
        defer { isCreating = false }
        do {
            _ = try await source.createTask(
                project: selectedProject,
                title: trimmedTitle,
                body: body.trimmingCharacters(in: .whitespacesAndNewlines))
            createError = nil
            await refresh()
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

    // MARK: - Pure logic (unit-tested)

    /// Group tasks by status into ordered sections (active work first, done
    /// trailing — see ``TaskStatus/sortOrder``). Within a section, tasks keep
    /// their incoming order (the daemon sorts by id ascending). Empty statuses
    /// produce no section.
    public static func sections(from tasks: [Ycc_V1_BacklogTaskSummary]) -> [BacklogSection] {
        var byStatus: [TaskStatus: [Ycc_V1_BacklogTaskSummary]] = [:]
        var order: [TaskStatus] = []
        for task in tasks {
            let status = TaskStatus(status: task.status)
            if byStatus[status] == nil { order.append(status) }
            byStatus[status, default: []].append(task)
        }
        return order
            .sorted { $0.sortOrder < $1.sortOrder }
            .map { BacklogSection(status: $0, tasks: byStatus[$0] ?? []) }
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
