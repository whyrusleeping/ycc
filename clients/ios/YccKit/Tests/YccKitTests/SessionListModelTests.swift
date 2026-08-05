import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``SessionListSource`` for headless model tests. Records
/// the project passed to each history query so the filter round-trip is testable.
private final class MockListSource: SessionListSource, @unchecked Sendable {
    var sessions: [Ycc_V1_SessionSummary] = []
    var sessionsByProject: [String: [Ycc_V1_SessionSummary]] = [:]
    var projects: [Ycc_V1_ProjectInfo] = []
    var historyError: Error?
    var historyErrorsByProject: [String: Error] = [:]
    var removeError: Error?
    private(set) var requestedProjects: [String] = []
    private(set) var removedProjects: [String] = []
    private let lock = NSLock()

    func listSessionHistory(project: String) async throws -> [Ycc_V1_SessionSummary] {
        lock.lock()
        requestedProjects.append(project)
        lock.unlock()
        if let error = historyErrorsByProject[project] { throw error }
        if let historyError { throw historyError }
        return sessionsByProject[project] ?? sessions
    }

    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        projects
    }

    func removeProject(name: String) async throws {
        if let removeError { throw removeError }
        lock.lock()
        removedProjects.append(name)
        projects.removeAll { $0.name == name }
        lock.unlock()
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

    private func project(_ name: String, path: String? = nil) -> Ycc_V1_ProjectInfo {
        var p = Ycc_V1_ProjectInfo()
        p.name = name
        p.path = path ?? "/tmp/\(name)"
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
        XCTAssertEqual(Set(source.requestedProjects), Set(["one", "two"]))
    }

    func testRecentFeedAggregatesProjectsAndSortsGloballyByRecency() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        source.sessionsByProject = [
            "one": [session(id: "old", lastActivity: "2026-07-08T08:00:00Z")],
            "two": [session(id: "new", lastActivity: "2026-07-08T12:00:00Z")],
        ]
        let model = SessionListModel(source: source)

        await model.refresh()

        XCTAssertNil(model.selectedProject)
        XCTAssertEqual(model.sections.flatMap(\.sessions).map(\.sessionID), ["new", "old"])
        XCTAssertEqual(model.project(for: model.sessions.first { $0.sessionID == "new" }!), "two")
        XCTAssertEqual(model.project(for: model.sessions.first { $0.sessionID == "old" }!), "one")
        XCTAssertNil(model.partialWarning)
    }

    func testRecentFeedDeduplicatesWorkspaceAliasesAndSessions() async {
        let source = MockListSource()
        source.projects = [
            project("primary", path: "/same/workspace"),
            project("alias", path: "/same/workspace"),
        ]
        source.sessionsByProject["primary"] = [session(id: "same")]
        let model = SessionListModel(source: source)

        await model.refresh()

        XCTAssertEqual(source.requestedProjects, ["primary"])
        XCTAssertEqual(model.sessions.map(\.sessionID), ["same"])
        XCTAssertEqual(model.project(for: model.sessions[0]), "primary")
    }

    func testRecentFeedPreservesPartialResultsAndWarns() async {
        let source = MockListSource()
        source.projects = [project("good"), project("bad")]
        source.sessionsByProject["good"] = [session(id: "available")]
        source.historyErrorsByProject["bad"] = YccError.rpc(message: "offline")
        let model = SessionListModel(source: source)

        await model.refresh()

        XCTAssertEqual(model.sessions.map(\.sessionID), ["available"])
        XCTAssertEqual(model.partialWarning, "Some projects couldn’t be loaded: bad.")
        XCTAssertNil(model.errorMessage)
    }

    func testOneShotFeedQueriesItsNamedProject() async {
        let source = MockListSource()
        source.projects = [project("only")]
        source.sessionsByProject["only"] = [session(id: "named")]
        let model = SessionListModel(source: source)

        await model.refresh()

        XCTAssertEqual(source.requestedProjects, ["only"])
        XCTAssertEqual(model.sessions.map(\.sessionID), ["named"])
        XCTAssertEqual(model.project(for: model.sessions[0]), "only")
    }

    func testProjectFilterRoundTrips() async {
        let source = MockListSource()
        let model = SessionListModel(source: source, selectedProject: "")

        await model.refresh()
        model.selectedProject = "myproj"
        await model.refresh()

        XCTAssertEqual(source.requestedProjects, ["", "myproj"])
    }

    func testProjectFilterShownWithOneProjectHiddenWithZero() async {
        // The filter includes All projects plus the named project.
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

    func testRecentFeedRequiresProjectChoiceForNewSession() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        let model = SessionListModel(source: source)

        await model.refresh()

        XCTAssertTrue(model.requiresProjectChoiceForNewSession)
        XCTAssertEqual(model.newSessionProjectChoices, ["one", "two"])
    }

    func testScopedListStartsNewSessionInSelectedProject() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        let model = SessionListModel(source: source, selectedProject: "two")

        await model.refresh()

        XCTAssertFalse(model.requiresProjectChoiceForNewSession)
    }

    func testOneShotRecentFeedOffersItsNamedProject() async {
        let source = MockListSource()
        source.projects = [project("only")]
        let model = SessionListModel(source: source)

        await model.refresh()

        XCTAssertTrue(model.requiresProjectChoiceForNewSession)
        XCTAssertEqual(model.newSessionProjectChoices, ["only"])
    }

    func testRemoveSelectedProjectFallsBackToRecentFeedAndRefreshes() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        let model = SessionListModel(source: source, selectedProject: "two")
        await model.refresh()

        let removed = await model.removeProject(named: "two")

        XCTAssertTrue(removed)
        XCTAssertEqual(source.removedProjects, ["two"])
        XCTAssertNil(model.selectedProject)
        XCTAssertEqual(model.projects.map(\.name), ["one"])
        // Every load fans out over the registered projects (the drawer's badges
        // need all of them); the post-removal reload queries the survivor only.
        // Fan-out order is a task group's, so compare as a multiset.
        XCTAssertEqual(source.requestedProjects.count, 3)
        XCTAssertEqual(Set(source.requestedProjects.prefix(2)), Set(["one", "two"]))
        XCTAssertEqual(source.requestedProjects.last, "one")
    }

    func testSelectingAProjectFiltersClientSideWithoutRefetching() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        source.sessionsByProject = [
            "one": [session(id: "a", lastActivity: "2026-07-08T08:00:00Z")],
            "two": [session(id: "b", lastActivity: "2026-07-08T12:00:00Z")],
        ]
        let model = SessionListModel(source: source)
        await model.refresh()

        XCTAssertEqual(model.sessions.map(\.sessionID), ["b", "a"])

        model.selectedProject = "one"

        XCTAssertEqual(model.sessions.map(\.sessionID), ["a"])
        XCTAssertEqual(model.allSessions.count, 2)
        // No extra history query: the aggregate load already holds every project.
        XCTAssertEqual(source.requestedProjects.count, 2)
    }

    func testActivityCountsAreTrackedPerProjectAndGlobally() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        source.sessionsByProject = [
            "one": [
                session(id: "running", status: "running", live: true),
                session(id: "asking", status: "running", live: true, waitingInput: true),
                // Not live: a persisted log's last status is history, not activity.
                session(id: "old", status: "running", live: false),
            ],
            "two": [session(id: "paused", status: "paused", live: true)],
        ]
        let model = SessionListModel(source: source)
        await model.refresh()

        XCTAssertEqual(model.activity(forProject: "one"), ProjectActivity(active: 2, needsAnswer: 1))
        XCTAssertEqual(model.activity(forProject: "two"), ProjectActivity(active: 1, needsAnswer: 0))
        XCTAssertEqual(model.activity(forProject: "missing"), ProjectActivity())
        XCTAssertEqual(model.totalActivity, ProjectActivity(active: 3, needsAnswer: 1))
        XCTAssertFalse(model.totalActivity.isEmpty)
    }

    func testIdleAndStoppedLiveSessionsAreNotCountedActive() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject = [
            "one": [
                session(id: "idle", status: "idle", live: true),
                session(id: "stopped", status: "stopped", live: true),
            ],
        ]
        let model = SessionListModel(source: source)
        await model.refresh()

        XCTAssertEqual(model.activity(forProject: "one"), ProjectActivity())
        XCTAssertTrue(model.totalActivity.isEmpty)
    }

    func testRemoveProjectFailureKeepsSelectionAndSurfacesError() async {
        let source = MockListSource()
        source.removeError = YccError.rpc(message: "cannot remove")
        let model = SessionListModel(source: source, selectedProject: "one")

        let removed = await model.removeProject(named: "one")

        XCTAssertFalse(removed)
        XCTAssertEqual(model.selectedProject, "one")
        XCTAssertEqual(model.errorMessage, "cannot remove")
    }

    func testRemoveProjectUnauthorizedSurfacesFlag() async {
        let source = MockListSource()
        source.removeError = YccError.unauthorized
        let model = SessionListModel(source: source)

        let removed = await model.removeProject(named: "one")

        XCTAssertFalse(removed)
        XCTAssertTrue(model.unauthorized)
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
