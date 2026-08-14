import Foundation
import Observation
import YccProto

/// The data source a ``TaskDetailModel`` reads from and drives. Abstracting it
/// behind a protocol lets the load / status-update logic be unit-tested
/// headlessly with an in-memory mock. ``YccClient`` is the production conformer.
public protocol TaskDetailSource: Sendable {
    /// Fetch one task's full detail (frontmatter + markdown body).
    func getTask(project: String, id: String) async throws -> Ycc_V1_TaskDetail
    /// List session-history rows so an in-progress task can link to the live
    /// session currently focused on it.
    func listSessionHistory(project: String) async throws -> [Ycc_V1_SessionSummary]
    /// Change a task's status; returns the refreshed detail.
    func updateTaskStatus(project: String, id: String, status: String) async throws -> Ycc_V1_TaskDetail
    /// Replace the task fields exposed by the detail editor.
    func updateTask(
        project: String, id: String, title: String, status: String,
        priority: Int, body: String, dependsOn: [String], specRefs: [String]
    ) async throws -> Ycc_V1_TaskDetail
}

extension YccClient: TaskDetailSource {}

/// Drives the task-detail screen: loads ``GetTask`` and exposes the frontmatter
/// fields and markdown
/// `body`, and applies status changes via ``UpdateTask`` (reflecting the
/// refreshed detail from the response). The data source is injected
/// (``TaskDetailSource``) so the logic is testable headlessly. `@MainActor`
/// because it publishes observable UI state.
@MainActor
@Observable
public final class TaskDetailModel {
    /// The task's id (stable across the model's life).
    public let taskID: String
    /// The named project the task lives in.
    public let project: String

    /// The last-loaded task detail, or `nil` before the first successful load.
    public private(set) var task: Ycc_V1_TaskDetail?
    /// Live running/paused sessions whose durable task focus includes this task.
    /// Usually one, but retaining every match avoids hiding parallel workstreams.
    public private(set) var activeSessions: [Ycc_V1_SessionSummary] = []

    public private(set) var isLoading = false
    /// Set while a status change is in flight (disables the picker).
    public private(set) var isUpdating = false
    public private(set) var errorMessage: String?
    /// Set when a load/update failed with ``YccError/unauthorized``; the view
    /// routes back to the connect screen via `AppModel.handleUnauthorized`.
    public private(set) var unauthorized = false

    /// Editable draft fields. They are seeded only when editing begins, so a
    /// cancelled edit cannot mutate the displayed canonical task.
    public private(set) var isEditing = false
    public var draftTitle = ""
    public var draftStatus: TaskStatus = .todo
    public var draftPriority = 3
    public var draftBody = ""
    public var draftDependsOn = ""
    public var draftSpecRefs = ""

    private let source: TaskDetailSource

    public init(source: TaskDetailSource, project: String = "", taskID: String) {
        self.source = source
        self.project = project
        self.taskID = taskID
    }

    /// The task's current status, or ``TaskStatus/unknown`` before load.
    public var status: TaskStatus {
        TaskStatus(status: task?.status ?? "")
    }

    /// (Re)load the task detail. Unauthorized bubbles up via ``unauthorized``.
    public func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let detail = try await source.getTask(project: project, id: taskID)
            task = detail
            activeSessions = []
            if TaskStatus(status: detail.status) == .inProgress {
                // Session discovery is an enhancement to task detail, not a
                // reason to hide the task if history is temporarily unavailable.
                do {
                    let history = try await source.listSessionHistory(project: project)
                    activeSessions = Self.activeSessions(for: taskID, in: history)
                } catch YccError.unauthorized {
                    throw YccError.unauthorized
                } catch {
                    activeSessions = []
                }
            }
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch let YccError.rpc(message) {
            errorMessage = message
        } catch let YccError.notFound(message) {
            errorMessage = message
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    /// Find genuinely active live sessions focused on `taskID`, newest first.
    /// Persisted rows that merely ended while marked running are excluded by
    /// `live`, and idle/error sessions do not offer a misleading "open active"
    /// action.
    public static func activeSessions(
        for taskID: String,
        in sessions: [Ycc_V1_SessionSummary]
    ) -> [Ycc_V1_SessionSummary] {
        let target = taskID.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !target.isEmpty else { return [] }
        return sessions
            .filter { session in
                session.live
                    && (session.status == "running" || session.status == "paused")
                    && session.focusTasks.contains {
                        $0.trimmingCharacters(in: .whitespacesAndNewlines) == target
                    }
            }
            .sorted { lhs, rhs in
                let left = SessionListModel.recencyDate(lhs)
                let right = SessionListModel.recencyDate(rhs)
                switch (left, right) {
                case let (l?, r?): return l > r
                case (_?, nil): return true
                case (nil, _?): return false
                case (nil, nil): return lhs.sessionID < rhs.sessionID
                }
            }
    }

    /// Seed an editable draft from the last canonical daemon response.
    public func beginEditing() {
        guard let task, !isUpdating else { return }
        draftTitle = task.title
        draftStatus = TaskStatus(status: task.status)
        if draftStatus == .unknown { draftStatus = .todo }
        draftPriority = Int(task.priority)
        draftBody = task.body
        draftDependsOn = task.dependsOn.joined(separator: ", ")
        draftSpecRefs = task.specRefs.joined(separator: "\n")
        errorMessage = nil
        isEditing = true
    }

    /// Discard the draft without changing the displayed task.
    public func cancelEditing() {
        guard !isUpdating else { return }
        isEditing = false
    }

    /// A local validation message for the current edit draft.
    public var draftValidationMessage: String? {
        if draftTitle.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return "Title is required."
        }
        if !(1...5).contains(draftPriority) {
            return "Priority must be between 1 and 5."
        }
        if parseList(draftDependsOn).contains(taskID) {
            return "A task cannot depend on itself."
        }
        return nil
    }

    /// Save the full draft through `UpdateTask`. The editor remains open after a
    /// failure so the user's text is not lost; success replaces the canonical
    /// detail with the server response and closes the editor.
    @discardableResult
    public func saveEditing() async -> Bool {
        guard isEditing, !isUpdating, draftValidationMessage == nil else { return false }
        isUpdating = true
        defer { isUpdating = false }
        do {
            task = try await source.updateTask(
                project: project,
                id: taskID,
                title: draftTitle.trimmingCharacters(in: .whitespacesAndNewlines),
                status: draftStatus.rawValue,
                priority: draftPriority,
                body: draftBody,
                dependsOn: parseList(draftDependsOn),
                specRefs: parseList(draftSpecRefs))
            errorMessage = nil
            isEditing = false
            return true
        } catch YccError.unauthorized {
            unauthorized = true
        } catch let YccError.rpc(message) {
            errorMessage = message
        } catch let YccError.notFound(message) {
            errorMessage = message
        } catch let YccError.failedPrecondition(message) {
            errorMessage = message
        } catch {
            errorMessage = error.localizedDescription
        }
        return false
    }

    /// Parse comma- or newline-separated task metadata, trimming whitespace,
    /// dropping empties, and retaining first occurrence order.
    private func parseList(_ value: String) -> [String] {
        var seen: Set<String> = []
        return value
            .components(separatedBy: CharacterSet(charactersIn: ",\n"))
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty && seen.insert($0).inserted }
    }

    /// Apply a status change (`UpdateTask`), reflecting the refreshed detail from
    /// the response. A no-op when the status is unchanged. Returns `true` on
    /// success; on failure sets ``errorMessage`` / ``unauthorized``.
    @discardableResult
    public func setStatus(_ newStatus: TaskStatus) async -> Bool {
        guard newStatus != .unknown, newStatus != status, !isUpdating else { return false }
        isUpdating = true
        defer { isUpdating = false }
        do {
            task = try await source.updateTaskStatus(
                project: project, id: taskID, status: newStatus.rawValue)
            errorMessage = nil
            return true
        } catch YccError.unauthorized {
            unauthorized = true
            return false
        } catch let YccError.rpc(message) {
            errorMessage = message
            return false
        } catch let YccError.notFound(message) {
            errorMessage = message
            return false
        } catch let YccError.failedPrecondition(message) {
            errorMessage = message
            return false
        } catch {
            errorMessage = error.localizedDescription
            return false
        }
    }
}
