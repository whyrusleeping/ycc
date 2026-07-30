import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``TaskDetailSource`` for headless model tests. Records
/// the last status-update args so the round-trip is testable.
private final class MockTaskDetailSource: TaskDetailSource, @unchecked Sendable {
    var detail: Ycc_V1_TaskDetail
    var getError: Error?
    var historyError: Error?
    var updateError: Error?
    var sessions: [Ycc_V1_SessionSummary] = []

    private(set) var historyProjects: [String] = []
    private(set) var updateArgs: (project: String, id: String, status: String)?

    init(detail: Ycc_V1_TaskDetail) {
        self.detail = detail
    }

    func getTask(project: String, id: String) async throws -> Ycc_V1_TaskDetail {
        if let getError { throw getError }
        return detail
    }

    func listSessionHistory(project: String) async throws -> [Ycc_V1_SessionSummary] {
        historyProjects.append(project)
        if let historyError { throw historyError }
        return sessions
    }

    func updateTaskStatus(project: String, id: String, status: String) async throws -> Ycc_V1_TaskDetail {
        updateArgs = (project, id, status)
        if let updateError { throw updateError }
        detail.status = status
        return detail
    }
}

private func detail(_ id: String, status: String, title: String = "A task") -> Ycc_V1_TaskDetail {
    var d = Ycc_V1_TaskDetail()
    d.id = id
    d.title = title
    d.status = status
    d.priority = 2
    return d
}

private func session(
    _ id: String,
    status: String = "running",
    live: Bool = true,
    tasks: [String] = ["0010"],
    lastActivity: String = "2026-07-15T12:00:00Z"
) -> Ycc_V1_SessionSummary {
    var s = Ycc_V1_SessionSummary()
    s.sessionID = id
    s.status = status
    s.live = live
    s.focusTasks = tasks
    s.lastActivity = lastActivity
    return s
}

@MainActor
final class TaskDetailModelTests: XCTestCase {
    func testLoadPopulatesTaskAndStatus() async {
        let source = MockTaskDetailSource(detail: detail("0010", status: "proposed"))
        let model = TaskDetailModel(source: source, taskID: "0010")
        await model.load()
        XCTAssertEqual(model.task?.id, "0010")
        XCTAssertEqual(model.status, .proposed)
        XCTAssertNil(model.errorMessage)
        XCTAssertTrue(source.historyProjects.isEmpty)
    }

    func testLoadFindsOnlyLiveRunningOrPausedFocusedSessionsNewestFirst() async {
        let source = MockTaskDetailSource(detail: detail("0010", status: "in_progress"))
        source.sessions = [
            session("older", status: "paused", lastActivity: "2026-07-14T12:00:00Z"),
            session("idle", status: "idle"),
            session("persisted", live: false),
            session("other-task", tasks: ["9999"]),
            session("newer", lastActivity: "2026-07-15T13:00:00Z"),
        ]
        let model = TaskDetailModel(source: source, project: "proj", taskID: "0010")

        await model.load()

        XCTAssertEqual(source.historyProjects, ["proj"])
        XCTAssertEqual(model.activeSessions.map(\.sessionID), ["newer", "older"])
    }

    func testLoadKeepsTaskWhenOptionalHistoryLookupFails() async {
        let source = MockTaskDetailSource(detail: detail("0010", status: "in_progress"))
        source.historyError = YccError.rpc(message: "history unavailable")
        let model = TaskDetailModel(source: source, taskID: "0010")
        await model.load()
        XCTAssertEqual(model.task?.id, "0010")
        XCTAssertTrue(model.activeSessions.isEmpty)
        XCTAssertNil(model.errorMessage)
    }

    func testLoadSurfacesNotFound() async {
        let source = MockTaskDetailSource(detail: detail("0010", status: "todo"))
        source.getError = YccError.notFound(message: "no such task")
        let model = TaskDetailModel(source: source, taskID: "0010")
        await model.load()
        XCTAssertNil(model.task)
        XCTAssertEqual(model.errorMessage, "no such task")
    }

    func testLoadSurfacesUnauthorized() async {
        let source = MockTaskDetailSource(detail: detail("0010", status: "todo"))
        source.getError = YccError.unauthorized
        let model = TaskDetailModel(source: source, taskID: "0010")
        await model.load()
        XCTAssertTrue(model.unauthorized)
    }

    func testSetStatusSendsRequestAndReflectsResponse() async {
        let source = MockTaskDetailSource(detail: detail("0010", status: "proposed"))
        let model = TaskDetailModel(source: source, project: "proj", taskID: "0010")
        await model.load()

        let ok = await model.setStatus(.todo)

        XCTAssertTrue(ok)
        XCTAssertEqual(source.updateArgs?.project, "proj")
        XCTAssertEqual(source.updateArgs?.id, "0010")
        XCTAssertEqual(source.updateArgs?.status, "todo")
        XCTAssertEqual(model.status, .todo)
    }

    func testSetStatusNoOpWhenUnchanged() async {
        let source = MockTaskDetailSource(detail: detail("0010", status: "todo"))
        let model = TaskDetailModel(source: source, taskID: "0010")
        await model.load()
        let ok = await model.setStatus(.todo)
        XCTAssertFalse(ok)
        XCTAssertNil(source.updateArgs)   // never called
    }

    func testSetStatusRejectsUnknown() async {
        let source = MockTaskDetailSource(detail: detail("0010", status: "todo"))
        let model = TaskDetailModel(source: source, taskID: "0010")
        await model.load()
        let ok = await model.setStatus(.unknown)
        XCTAssertFalse(ok)
        XCTAssertNil(source.updateArgs)
    }

    func testSetStatusSurfacesError() async {
        let source = MockTaskDetailSource(detail: detail("0010", status: "todo"))
        source.updateError = YccError.rpc(message: "invalid status")
        let model = TaskDetailModel(source: source, taskID: "0010")
        await model.load()
        let ok = await model.setStatus(.done)
        XCTAssertFalse(ok)
        XCTAssertEqual(model.errorMessage, "invalid status")
    }

    func testSetStatusSurfacesUnauthorized() async {
        let source = MockTaskDetailSource(detail: detail("0010", status: "todo"))
        source.updateError = YccError.unauthorized
        let model = TaskDetailModel(source: source, taskID: "0010")
        await model.load()
        let ok = await model.setStatus(.done)
        XCTAssertFalse(ok)
        XCTAssertTrue(model.unauthorized)
    }
}
