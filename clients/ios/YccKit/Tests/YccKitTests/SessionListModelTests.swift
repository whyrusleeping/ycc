import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``SessionListSource`` for headless model tests. Records
/// the project passed to each history query so the filter round-trip is testable.
private final class MockListSource: SessionListSource, @unchecked Sendable {
    var sessions: [Ycc_V1_SessionSummary] = []
    var projects: [Ycc_V1_ProjectInfo] = []
    var historyError: Error?
    private(set) var requestedProjects: [String] = []
    private let lock = NSLock()

    func listSessionHistory(project: String) async throws -> [Ycc_V1_SessionSummary] {
        lock.lock()
        requestedProjects.append(project)
        lock.unlock()
        if let historyError { throw historyError }
        return sessions
    }

    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        projects
    }
}

@MainActor
final class SessionListModelTests: XCTestCase {
    private func session(
        id: String,
        status: String = "idle",
        title: String = "",
        mode: String = "pm",
        startedAt: String = "",
        lastActivity: String = "",
        turns: Int64 = 0,
        live: Bool = false,
        waitingInput: Bool = false,
        focusTasks: [String] = []
    ) -> Ycc_V1_SessionSummary {
        var s = Ycc_V1_SessionSummary()
        s.sessionID = id
        s.status = status
        s.title = title
        s.mode = mode
        s.startedAt = startedAt
        s.lastActivity = lastActivity
        s.turns = turns
        s.live = live
        s.waitingInput = waitingInput
        s.focusTasks = focusTasks
        return s
    }

    private func project(_ name: String) -> Ycc_V1_ProjectInfo {
        var p = Ycc_V1_ProjectInfo()
        p.name = name
        p.path = "/tmp/\(name)"
        return p
    }

    // MARK: - Sectioning / sorting

    func testNeedsAnswerSessionsPinnedToTopSection() {
        let sessions = [
            session(id: "a", lastActivity: "2026-07-08T10:00:00Z"),
            session(id: "b", lastActivity: "2026-07-08T09:00:00Z", live: true, waitingInput: true),
            session(id: "c", lastActivity: "2026-07-08T11:00:00Z"),
        ]
        let sections = SessionListModel.sections(from: sessions)
        XCTAssertEqual(sections.count, 2)
        XCTAssertEqual(sections[0].kind, .needsAnswer)
        XCTAssertEqual(sections[0].sessions.map(\.sessionID), ["b"])
        XCTAssertEqual(sections[1].kind, .all)
        // Remainder most-recent-first.
        XCTAssertEqual(sections[1].sessions.map(\.sessionID), ["c", "a"])
    }

    func testWaitingInputWithoutLiveIsNotNeedsAnswer() {
        // waitingInput is only meaningful on live rows.
        let sessions = [session(id: "a", live: false, waitingInput: true)]
        let sections = SessionListModel.sections(from: sessions)
        XCTAssertEqual(sections.count, 1)
        XCTAssertEqual(sections[0].kind, .all)
    }

    func testNoNeedsAnswerSectionOmitted() {
        let sessions = [
            session(id: "a", lastActivity: "2026-07-08T10:00:00Z"),
            session(id: "b", lastActivity: "2026-07-08T11:00:00Z"),
        ]
        let sections = SessionListModel.sections(from: sessions)
        XCTAssertEqual(sections.count, 1)
        XCTAssertEqual(sections[0].kind, .all)
        // No header title when it's the only section.
        XCTAssertNil(sections[0].title)
        XCTAssertEqual(sections[0].sessions.map(\.sessionID), ["b", "a"])
    }

    func testSortsByLastActivityDescending() {
        let sessions = [
            session(id: "old", lastActivity: "2026-07-08T08:00:00Z"),
            session(id: "new", lastActivity: "2026-07-08T12:00:00Z"),
            session(id: "mid", lastActivity: "2026-07-08T10:00:00Z"),
        ]
        let sorted = SessionListModel.sortedByRecency(sessions)
        XCTAssertEqual(sorted.map(\.sessionID), ["new", "mid", "old"])
    }

    func testFallsBackToStartedAtWhenNoLastActivity() {
        let sessions = [
            session(id: "a", startedAt: "2026-07-08T08:00:00Z"),
            session(id: "b", startedAt: "2026-07-08T12:00:00Z"),
        ]
        let sorted = SessionListModel.sortedByRecency(sessions)
        XCTAssertEqual(sorted.map(\.sessionID), ["b", "a"])
    }

    func testUnparseableTimestampsKeepStableOrder() {
        let sessions = [
            session(id: "a", lastActivity: "not-a-date"),
            session(id: "b", lastActivity: ""),
            session(id: "c", lastActivity: "garbage"),
        ]
        let sorted = SessionListModel.sortedByRecency(sessions)
        XCTAssertEqual(sorted.map(\.sessionID), ["a", "b", "c"])
    }

    func testDatedSessionsSortAheadOfUndated() {
        let sessions = [
            session(id: "undated", lastActivity: ""),
            session(id: "dated", lastActivity: "2026-07-08T10:00:00Z"),
        ]
        let sorted = SessionListModel.sortedByRecency(sessions)
        XCTAssertEqual(sorted.map(\.sessionID), ["dated", "undated"])
    }

    func testFractionalSecondsAndOffsetTimestampsParse() {
        XCTAssertNotNil(SessionListModel.parseTimestamp("2026-07-08T10:00:00.123456Z"))
        XCTAssertNotNil(SessionListModel.parseTimestamp("2026-07-08T10:00:00-07:00"))
        XCTAssertNotNil(SessionListModel.parseTimestamp("2026-07-08T10:00:00.5-07:00"))
        XCTAssertNotNil(SessionListModel.parseTimestamp("2026-07-08T10:00:00Z"))
        XCTAssertNil(SessionListModel.parseTimestamp(""))
        XCTAssertNil(SessionListModel.parseTimestamp("nonsense"))
    }

    // MARK: - Title fallback

    func testDisplayTitleUsesTitleWhenPresent() {
        let s = session(id: "abcdef12345", title: "Fix the bug")
        XCTAssertEqual(SessionListModel.displayTitle(for: s), "Fix the bug")
    }

    func testDisplayTitleFallsBackToModeAndShortID() {
        let s = session(id: "abcdef1234567890", title: "   ", mode: "pm")
        XCTAssertEqual(SessionListModel.displayTitle(for: s), "pm · abcdef12")
    }

    func testDisplayTitleReferencesFocusedTask() {
        let s = session(
            id: "abcdef12345", title: "Implement the widget", focusTasks: ["0214"])
        XCTAssertEqual(
            SessionListModel.displayTitle(for: s),
            "[0214] Implement the widget")
    }

    func testDisplayTitleReferencesDistinctFocusedTasksInOrder() {
        let s = session(
            id: "abcdef1234567890", title: " ", mode: "work",
            focusTasks: [" 0214 ", "", "0215", "0214"])
        XCTAssertEqual(
            SessionListModel.displayTitle(for: s),
            "[0214,0215] work · abcdef12")
    }

    // MARK: - Status mapping

    func testStatusKindMapping() {
        XCTAssertEqual(SessionStatusKind(status: "running"), .running)
        XCTAssertEqual(SessionStatusKind(status: "IDLE"), .idle)
        XCTAssertEqual(SessionStatusKind(status: "error"), .error)
        XCTAssertEqual(SessionStatusKind(status: "paused"), .paused)
        XCTAssertEqual(SessionStatusKind(status: "stopped"), .stopped)
        XCTAssertEqual(SessionStatusKind(status: "weird"), .unknown)
        XCTAssertEqual(SessionStatusKind(status: ""), .unknown)
    }

    // MARK: - Refresh / project filter round-trip

    func testRefreshLoadsSessionsAndProjects() async {
        let source = MockListSource()
        source.sessions = [session(id: "a")]
        source.projects = [project("one"), project("two")]
        let model = SessionListModel(source: source)

        await model.refresh()

        XCTAssertEqual(model.sessions.map(\.sessionID), ["a"])
        XCTAssertEqual(model.projects.count, 2)
        XCTAssertTrue(model.showsProjectFilter)
        XCTAssertNil(model.errorMessage)
        XCTAssertEqual(source.requestedProjects, [""])
    }

    func testProjectFilterRoundTrips() async {
        let source = MockListSource()
        let model = SessionListModel(source: source)

        await model.refresh()
        model.selectedProject = "myproj"
        await model.refresh()

        XCTAssertEqual(source.requestedProjects, ["", "myproj"])
    }

    func testProjectFilterShownWithOneProjectHiddenWithZero() async {
        // Even one registered project means two choices (Default + it), so the
        // filter shows; with nothing registered there's only Default, so it hides.
        let source = MockListSource()
        source.projects = [project("only")]
        let model = SessionListModel(source: source)
        await model.refresh()
        XCTAssertTrue(model.showsProjectFilter)

        let emptySource = MockListSource()
        let emptyModel = SessionListModel(source: emptySource)
        await emptyModel.refresh()
        XCTAssertFalse(emptyModel.showsProjectFilter)
    }

    func testUnauthorizedSurfacesFlag() async {
        let source = MockListSource()
        source.historyError = YccError.unauthorized
        let model = SessionListModel(source: source)

        await model.refresh()

        XCTAssertTrue(model.unauthorized)
    }

    func testRpcErrorSurfacesMessage() async {
        let source = MockListSource()
        source.historyError = YccError.rpc(message: "boom")
        let model = SessionListModel(source: source)

        await model.refresh()

        XCTAssertEqual(model.errorMessage, "boom")
        XCTAssertFalse(model.unauthorized)
    }
}
