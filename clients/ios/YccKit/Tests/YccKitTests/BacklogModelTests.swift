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
    var updateError: Error?
    /// When set, replaces `tasks` on the next successful create (simulates the
    /// new row appearing after the create's implicit refresh).
    var tasksAfterCreate: [Ycc_V1_BacklogTaskSummary]?

    private(set) var createArgs: (project: String, title: String, body: String, priority: Int)?
    private(set) var updateArgs: (project: String, id: String, status: String)?
    private(set) var listCount = 0

    func listBacklog(project: String) async throws -> [Ycc_V1_BacklogTaskSummary] {
        listCount += 1
        if let listError { throw listError }
        return tasks
    }

    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        projects
    }

    func createTask(
        project: String, title: String, body: String, priority: Int
    ) async throws -> Ycc_V1_TaskDetail {
        createArgs = (project, title, body, priority)
        if let createError { throw createError }
        if let after = tasksAfterCreate { tasks = after }
        var detail = Ycc_V1_TaskDetail()
        detail.id = "0099"
        detail.title = title
        detail.status = "todo"
        return detail
    }

    func updateTaskStatus(
        project: String, id: String, status: String
    ) async throws -> Ycc_V1_TaskDetail {
        updateArgs = (project, id, status)
        if let updateError { throw updateError }
        // Reflect the change in the list, as the daemon would on the next load.
        if let index = tasks.firstIndex(where: { $0.id == id }) {
            tasks[index].status = status
        }
        var detail = Ycc_V1_TaskDetail()
        detail.id = id
        detail.title = "Task \(id)"
        detail.status = status
        detail.priority = 3
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
    // MARK: - Board lanes

    func testBoardLanesFollowWorkflowOrder() {
        let lanes = BacklogModel.board(from: [summary("0001", status: "todo")])
        XCTAssertEqual(
            lanes.map(\.status),
            [.proposed, .todo, .inProgress, .inReview, .blocked, .done])
    }

    func testBoardKeepsEmptyLanes() {
        // A board with lanes that vanish when empty is not a board — and an
        // empty lane is exactly the column you want to move a card into.
        let lanes = BacklogModel.board(from: [summary("0001", status: "in_progress")])
        XCTAssertEqual(lanes.count, 6)
        XCTAssertEqual(lanes.first(where: { $0.status == .inProgress })?.tasks.count, 1)
        XCTAssertEqual(lanes.first(where: { $0.status == .todo })?.tasks.count, 0)
    }

    func testBoardAppendsAnUnknownLaneOnlyWhenUsed() {
        XCTAssertNil(
            BacklogModel.board(from: [summary("0001", status: "todo")])
                .first(where: { $0.status == .unknown }))
        let withStray = BacklogModel.board(from: [summary("0002", status: "weird")])
        XCTAssertEqual(withStray.last?.status, .unknown)
        XCTAssertEqual(withStray.last?.tasks.map(\.id), ["0002"])
    }

    func testBoardGroupsTasksIntoTheirLane() {
        let lanes = BacklogModel.board(from: [
            summary("0001", status: "todo"),
            summary("0002", status: "todo"),
            summary("0003", status: "done"),
        ])
        XCTAssertEqual(lanes.first(where: { $0.status == .todo })?.tasks.map(\.id), ["0002", "0001"])
        XCTAssertEqual(lanes.first(where: { $0.status == .done })?.tasks.map(\.id), ["0003"])
    }

    func testBoardCanSortOldestFirst() {
        let lanes = BacklogModel.board(
            from: [
                summary("0010", status: "todo"),
                summary("0002", status: "todo"),
                summary("0007", status: "todo"),
            ],
            sort: .oldestFirst)
        XCTAssertEqual(
            lanes.first(where: { $0.status == .todo })?.tasks.map(\.id),
            ["0002", "0007", "0010"])
    }

    func testAdjacentBoardColumns() {
        XCTAssertNil(TaskStatus.proposed.previousBoardColumn)
        XCTAssertEqual(TaskStatus.proposed.nextBoardColumn, .todo)
        XCTAssertEqual(TaskStatus.inProgress.previousBoardColumn, .todo)
        XCTAssertEqual(TaskStatus.inProgress.nextBoardColumn, .inReview)
        XCTAssertNil(TaskStatus.done.nextBoardColumn)
        XCTAssertEqual(TaskStatus.done.previousBoardColumn, .blocked)
        // `unknown` is off-board and has no neighbours.
        XCTAssertNil(TaskStatus.unknown.previousBoardColumn)
        XCTAssertNil(TaskStatus.unknown.nextBoardColumn)
    }

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
        XCTAssertEqual(sections.last?.tasks.map(\.id), ["0002", "0001"])
    }

    func testSectionsCanSortOldestFirst() {
        let sections = BacklogModel.sections(
            from: [
                summary("12", status: "todo"),
                summary("2", status: "todo"),
                summary("10", status: "todo"),
            ],
            sort: .oldestFirst)
        XCTAssertEqual(sections.first?.tasks.map(\.id), ["2", "10", "12"])
    }

    func testPrioritySortPutsUnsetLastAndBreaksTiesNewestFirst() {
        let tasks = [
            summary("0002", status: "todo", priority: 0),
            summary("0004", status: "todo", priority: 2),
            summary("0001", status: "todo", priority: 1),
            summary("0005", status: "todo", priority: 2),
            summary("0003", status: "todo", priority: 0),
        ]
        XCTAssertEqual(
            BacklogModel.sorted(tasks, by: .priority).map(\.id),
            ["0001", "0005", "0004", "0003", "0002"])
    }

    func testOddIDsSortDeterministicallyWithoutCrashing() {
        let tasks = [
            summary("alpha", status: "todo"),
            summary("2", status: "todo"),
            summary("0010", status: "todo"),
            summary("beta", status: "todo"),
            summary("10", status: "todo"),
        ]
        XCTAssertEqual(
            BacklogModel.sorted(tasks, by: .oldestFirst).map(\.id),
            ["2", "0010", "10", "alpha", "beta"])
        XCTAssertEqual(
            BacklogModel.sorted(tasks, by: .newestFirst).map(\.id),
            ["beta", "alpha", "10", "0010", "2"])
    }

    func testMixedWidthAndNonNumericIDsHaveATotalOrder() {
        let tasks = [
            summary("1a", status: "todo"),
            summary("10", status: "todo"),
            summary("9", status: "todo"),
        ]
        XCTAssertEqual(
            BacklogModel.sorted(tasks, by: .oldestFirst).map(\.id),
            ["9", "10", "1a"])
        XCTAssertEqual(
            BacklogModel.sorted(tasks, by: .newestFirst).map(\.id),
            ["1a", "10", "9"])
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

        let ok = await model.create(title: "  new idea  ", body: "  details  ", priority: 1)

        XCTAssertTrue(ok)
        XCTAssertEqual(source.createArgs?.project, "proj")
        XCTAssertEqual(source.createArgs?.title, "new idea")
        XCTAssertEqual(source.createArgs?.body, "details")
        XCTAssertEqual(source.createArgs?.priority, 1)
        // Refreshed after create: the new row is present.
        XCTAssertEqual(model.tasks.map(\.id), ["0099"])
        XCTAssertNil(model.createError)
    }

    func testCreateDefaultsToP3() async {
        let source = MockBacklogSource()
        let model = BacklogModel(source: source)

        let ok = await model.create(title: "idea", body: "")
        XCTAssertTrue(ok)
        XCTAssertEqual(source.createArgs?.priority, 3)
    }

    func testCreateRejectsInvalidPriorityWithoutRoundTrip() async {
        let source = MockBacklogSource()
        let model = BacklogModel(source: source)

        let ok = await model.create(title: "idea", body: "", priority: 6)
        XCTAssertFalse(ok)
        XCTAssertNil(source.createArgs)
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

    // MARK: - Status change

    func testSetStatusSendsArgsAndMovesRow() async {
        let source = MockBacklogSource()
        source.tasks = [summary("0001", status: "todo")]
        let model = BacklogModel(source: source, selectedProject: "proj")
        await model.refresh()

        let ok = await model.setStatus(taskID: "0001", to: .done)

        XCTAssertTrue(ok)
        XCTAssertEqual(source.updateArgs?.project, "proj")
        XCTAssertEqual(source.updateArgs?.id, "0001")
        XCTAssertEqual(source.updateArgs?.status, "done")
        XCTAssertEqual(model.sections.map(\.status), [.done])
        XCTAssertNil(model.updateError)
        XCTAssertNil(model.updatingTaskID)
    }

    func testSetStatusNoOpWhenUnchanged() async {
        let source = MockBacklogSource()
        source.tasks = [summary("0001", status: "todo")]
        let model = BacklogModel(source: source)
        await model.refresh()

        let ok = await model.setStatus(taskID: "0001", to: .todo)

        XCTAssertTrue(ok)
        XCTAssertNil(source.updateArgs)
    }

    func testSetStatusRejectsUnknownWithoutRoundTrip() async {
        let source = MockBacklogSource()
        source.tasks = [summary("0001", status: "todo")]
        let model = BacklogModel(source: source)
        await model.refresh()

        let ok = await model.setStatus(taskID: "0001", to: .unknown)

        XCTAssertFalse(ok)
        XCTAssertNil(source.updateArgs)
    }

    func testSetStatusSurfacesError() async {
        let source = MockBacklogSource()
        source.tasks = [summary("0001", status: "todo")]
        source.updateError = YccError.rpc(message: "nope")
        let model = BacklogModel(source: source)
        await model.refresh()

        let ok = await model.setStatus(taskID: "0001", to: .done)

        XCTAssertFalse(ok)
        XCTAssertEqual(model.updateError, "nope")
        // The row is untouched on failure.
        XCTAssertEqual(model.tasks.first?.status, "todo")
    }

    func testSetStatusSurfacesUnauthorized() async {
        let source = MockBacklogSource()
        source.tasks = [summary("0001", status: "todo")]
        source.updateError = YccError.unauthorized
        let model = BacklogModel(source: source)
        await model.refresh()

        let ok = await model.setStatus(taskID: "0001", to: .done)

        XCTAssertFalse(ok)
        XCTAssertTrue(model.unauthorized)
    }
}
