import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``BacklogSource`` for headless model tests. Records the
/// last create args so the request round-trip is testable, and can flip its
/// task list to simulate a post-create refresh.
private final class MockBacklogSource: BacklogSource, @unchecked Sendable {
    var tasks: [Ycc_V1_BacklogTaskSummary] = []
    var projects: [Ycc_V1_ProjectInfo] = []
    var listError: Error?
    var createError: Error?
    /// When set, replaces `tasks` on the next successful create (simulates the
    /// new row appearing after the create's implicit refresh).
    var tasksAfterCreate: [Ycc_V1_BacklogTaskSummary]?

    private(set) var createArgs: (project: String, title: String, body: String)?
    private(set) var listCount = 0

    func listBacklog(project: String) async throws -> [Ycc_V1_BacklogTaskSummary] {
        listCount += 1
        if let listError { throw listError }
        return tasks
    }

    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        projects
    }

    func createTask(project: String, title: String, body: String) async throws -> Ycc_V1_TaskDetail {
        createArgs = (project, title, body)
        if let createError { throw createError }
        if let after = tasksAfterCreate { tasks = after }
        var detail = Ycc_V1_TaskDetail()
        detail.id = "0099"
        detail.title = title
        detail.status = "todo"
        return detail
    }
}

private func summary(
    _ id: String, status: String, priority: Int32 = 3,
    ready: Bool = true, blockedBy: [String] = []
) -> Ycc_V1_BacklogTaskSummary {
    var s = Ycc_V1_BacklogTaskSummary()
    s.id = id
    s.title = "Task \(id)"
    s.status = status
    s.priority = priority
    s.ready = ready
    s.blockedBy = blockedBy
    return s
}

@MainActor
final class BacklogModelTests: XCTestCase {
    // MARK: - Sectioning

    func testSectionsOrderedActiveFirstDoneLast() {
        let tasks = [
            summary("0001", status: "done"),
            summary("0002", status: "proposed"),
            summary("0003", status: "todo"),
            summary("0004", status: "in_progress"),
            summary("0005", status: "blocked"),
            summary("0006", status: "in_review"),
        ]
        let sections = BacklogModel.sections(from: tasks)
        XCTAssertEqual(
            sections.map(\.status),
            [.inProgress, .inReview, .todo, .blocked, .proposed, .done])
    }

    func testSectionsGroupTasksAndSkipEmptyStatuses() {
        let tasks = [
            summary("0001", status: "todo"),
            summary("0002", status: "todo"),
            summary("0003", status: "in_progress"),
        ]
        let sections = BacklogModel.sections(from: tasks)
        XCTAssertEqual(sections.count, 2)
        XCTAssertEqual(sections.first?.status, .inProgress)
        XCTAssertEqual(sections.last?.status, .todo)
        XCTAssertEqual(sections.last?.tasks.count, 2)
    }

    func testUnknownStatusKeptVisible() {
        let sections = BacklogModel.sections(from: [summary("0001", status: "weird")])
        XCTAssertEqual(sections.first?.status, .unknown)
    }

    // MARK: - Ready / blocked annotation

    func testBlockedAnnotationListsDeps() {
        let task = summary("0005", status: "todo", ready: false, blockedBy: ["0173", "0174"])
        XCTAssertEqual(BacklogModel.blockedAnnotation(for: task), "Blocked by 0173, 0174")
    }

    func testReadyTaskHasNoAnnotation() {
        let task = summary("0005", status: "todo", ready: true)
        XCTAssertNil(BacklogModel.blockedAnnotation(for: task))
    }

    func testDoneTaskNeverAnnotated() {
        let task = summary("0005", status: "done", ready: false, blockedBy: ["0173"])
        XCTAssertNil(BacklogModel.blockedAnnotation(for: task))
    }

    // MARK: - Refresh

    func testRefreshLoadsTasksAndProjects() async {
        let source = MockBacklogSource()
        source.tasks = [summary("0001", status: "todo")]
        source.projects = [ {
            var p = Ycc_V1_ProjectInfo(); p.name = "a"; return p
        }(), {
            var p = Ycc_V1_ProjectInfo(); p.name = "b"; return p
        }() ]
        let model = BacklogModel(source: source)

        await model.refresh()

        XCTAssertEqual(model.tasks.count, 1)
        XCTAssertTrue(model.showsProjectFilter)
        XCTAssertNil(model.errorMessage)
    }

    func testRefreshSurfacesRpcError() async {
        let source = MockBacklogSource()
        source.listError = YccError.rpc(message: "boom")
        let model = BacklogModel(source: source)
        await model.refresh()
        XCTAssertEqual(model.errorMessage, "boom")
        XCTAssertFalse(model.unauthorized)
    }

    func testRefreshSurfacesUnauthorized() async {
        let source = MockBacklogSource()
        source.listError = YccError.unauthorized
        let model = BacklogModel(source: source)
        await model.refresh()
        XCTAssertTrue(model.unauthorized)
    }

    // MARK: - Quick capture

    func testCreateRejectsBlankTitleWithoutRoundTrip() async {
        let source = MockBacklogSource()
        let model = BacklogModel(source: source)
        let ok = await model.create(title: "   ", body: "x")
        XCTAssertFalse(ok)
        XCTAssertNil(source.createArgs)
    }

    func testCreateSendsTrimmedArgsAndRefreshes() async {
        let source = MockBacklogSource()
        source.tasksAfterCreate = [summary("0099", status: "todo")]
        let model = BacklogModel(source: source, selectedProject: "proj")

        let ok = await model.create(title: "  new idea  ", body: "  details  ")

        XCTAssertTrue(ok)
        XCTAssertEqual(source.createArgs?.project, "proj")
        XCTAssertEqual(source.createArgs?.title, "new idea")
        XCTAssertEqual(source.createArgs?.body, "details")
        // Refreshed after create: the new row is present.
        XCTAssertEqual(model.tasks.map(\.id), ["0099"])
        XCTAssertNil(model.createError)
    }

    func testCreateSurfacesError() async {
        let source = MockBacklogSource()
        source.createError = YccError.rpc(message: "nope")
        let model = BacklogModel(source: source)
        let ok = await model.create(title: "x", body: "")
        XCTAssertFalse(ok)
        XCTAssertEqual(model.createError, "nope")
    }

    func testCreateSurfacesUnauthorized() async {
        let source = MockBacklogSource()
        source.createError = YccError.unauthorized
        let model = BacklogModel(source: source)
        let ok = await model.create(title: "x", body: "")
        XCTAssertFalse(ok)
        XCTAssertTrue(model.unauthorized)
    }
}
