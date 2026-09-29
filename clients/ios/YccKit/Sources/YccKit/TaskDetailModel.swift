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
    func listSessionHistory(project: String, limit: Int32, cursor: String) async throws -> SessionHistoryPage
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

    /// True once ``task`` has been confirmed by `GetTask` in this model. Until
    /// then it may be a placeholder built from the backlog row or a cached
    /// detail, rendered immediately while the real load is in flight.
    public private(set) var hasLoaded = false
    /// ``task`` is a placeholder built from a backlog summary: header fields
    /// are real, but the body/dates are not loaded yet.
    public private(set) var isPlaceholder = false

    /// Editing replaces every field, so it needs the daemon's current detail.
    public var canEdit: Bool { task != nil && hasLoaded && !isUpdating }

    private let source: TaskDetailSource
    private let cache: AppDataCache?
    private let cacheGeneration: UInt64

    /// - Parameters:
    ///   - seed: the backlog row the user tapped, if known.
    ///   - cache: app-level cache; a cached detail (or the cached backlog row)
    ///     renders at once while ``load()`` revalidates.
    public init(
        source: TaskDetailSource, project: String = "", taskID: String,
        seed: Ycc_V1_BacklogTaskSummary? = nil, cache: AppDataCache? = nil
    ) {
        self.source = source
        self.project = project
        self.taskID = taskID
        self.cache = cache
        self.cacheGeneration = cache?.generation ?? 0
        let summary = seed ?? cache?
            .value(.backlog(project), as: [Ycc_V1_BacklogTaskSummary].self)?
            .first(where: { $0.id == taskID })
        if var cached = cache?.value(.task(project: project, id: taskID), as: Ycc_V1_TaskDetail.self) {
            // The list row is revalidated far more often (and carries this
            // client's optimistic status changes); prefer its header fields.
            if let summary { Self.overlay(summary, onto: &cached) }
            task = cached
        } else if let summary {
            task = Self.placeholder(from: summary)
            isPlaceholder = true
        }
    }

    /// A header-only detail built from a backlog row.
    static func placeholder(from summary: Ycc_V1_BacklogTaskSummary) -> Ycc_V1_TaskDetail {
        var detail = Ycc_V1_TaskDetail()
        detail.id = summary.id
        overlay(summary, onto: &detail)
        detail.dependsOn = summary.dependsOn
        return detail
    }

    private static func overlay(_ summary: Ycc_V1_BacklogTaskSummary, onto detail: inout Ycc_V1_TaskDetail) {
        detail.title = summary.title
        detail.status = summary.status
        detail.priority = summary.priority
        detail.ready = summary.ready
        detail.blockedBy = summary.blockedBy
    }

    private func install(_ detail: Ycc_V1_TaskDetail) {
        task = detail
        hasLoaded = true
        isPlaceholder = false
        cache?.store(detail, for: .task(project: project, id: taskID), ifGeneration: cacheGeneration)
        // Keep a cached backlog row in step, so a rebuilt backlog screen does
        // not briefly show the task in its old lane.
        if var rows = cache?.value(.backlog(project), as: [Ycc_V1_BacklogTaskSummary].self),
           let index = rows.firstIndex(where: { $0.id == detail.id }) {
            let updated = BacklogModel.summary(from: detail)
            if rows[index] != updated {
                rows[index] = updated
                cache?.store(rows, for: .backlog(project), ifGeneration: cacheGeneration)
            }
        }
    }

    /// The task's current status, or ``TaskStatus/unknown`` before load.
    public var status: TaskStatus {
        TaskStatus(status: task?.status ?? "")
    }

    /// (Re)load the task detail. Unauthorized bubbles up via ``unauthorized``.
    ///
    /// Active-session discovery only matters for an in-progress task, but the
    /// status is unknown until GetTask answers. On a remote link a second
    /// serial round trip is the dominant cost, so the history lookup starts
    /// concurrently with GetTask and is discarded (cancelled) when the task
    /// turns out not to be in progress. A reload of a task already known not
    /// to be in progress skips the speculation and fetches history only if
    /// the status changed to in progress.
    public func load() async {
        isLoading = true
        defer { isLoading = false }
        let source = source
        let project = project
        let taskID = taskID
        // A placeholder/cached status may be stale, so speculate until the
        // daemon has confirmed the task in this model.
        let speculate = !hasLoaded || status == .inProgress
        do {
            let detail: Ycc_V1_TaskDetail
            var history: Result<SessionHistoryPage, Error>?
            if speculate {
                async let pending = Self.fetchHistory(source: source, project: project)
                detail = try await source.getTask(project: project, id: taskID)
                if TaskStatus(status: detail.status) == .inProgress {
                    history = await pending
                }
                // Otherwise the unawaited lookup is cancelled at scope exit.
            } else {
                detail = try await source.getTask(project: project, id: taskID)
                if TaskStatus(status: detail.status) == .inProgress {
                    history = await Self.fetchHistory(source: source, project: project)
                }
            }
            install(detail)
            activeSessions = []
            switch history {
            case .success(let page)?:
                activeSessions = Self.activeSessions(for: taskID, in: page.sessions + page.pinned)
            case .failure(YccError.unauthorized)?:
                throw YccError.unauthorized
            case .failure?, nil:
                // Session discovery is an enhancement to task detail, not a
                // reason to hide the task if history is temporarily unavailable.
                break
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

    nonisolated private static func fetchHistory(
        source: TaskDetailSource, project: String
    ) async -> Result<SessionHistoryPage, Error> {
        do {
            return .success(try await source.listSessionHistory(project: project, limit: 50, cursor: ""))
        } catch {
            return .failure(error)
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
        guard let task, canEdit else { return }
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
            let updated = try await source.updateTask(
                project: project,
                id: taskID,
                title: draftTitle.trimmingCharacters(in: .whitespacesAndNewlines),
                status: draftStatus.rawValue,
                priority: draftPriority,
                body: draftBody,
                dependsOn: parseList(draftDependsOn),
                specRefs: parseList(draftSpecRefs))
            install(updated)
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
        // Show the new status at once; the response (or a failure) settles it.
        let previousStatus = task?.status
        task?.status = newStatus.rawValue
        do {
            let updated = try await source.updateTaskStatus(
                project: project, id: taskID, status: newStatus.rawValue)
            install(updated)
            errorMessage = nil
            return true
        } catch YccError.unauthorized {
            revertStatus(to: previousStatus, from: newStatus)
            unauthorized = true
            return false
        } catch let YccError.rpc(message) {
            revertStatus(to: previousStatus, from: newStatus)
            errorMessage = message
            return false
        } catch let YccError.notFound(message) {
            revertStatus(to: previousStatus, from: newStatus)
            errorMessage = message
            return false
        } catch let YccError.failedPrecondition(message) {
            revertStatus(to: previousStatus, from: newStatus)
            errorMessage = message
            return false
        } catch {
            revertStatus(to: previousStatus, from: newStatus)
            errorMessage = error.localizedDescription
            return false
        }
    }

    private func revertStatus(to previous: String?, from attempted: TaskStatus) {
        guard let previous, task?.status == attempted.rawValue else { return }
        task?.status = previous
    }
}
