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
    var historyFailuresRemainingByProject: [String: Int] = [:]
    var projectsError: Error?
    var projectFailuresRemaining = 0
    var historyDelayNanoseconds: UInt64 = 0
    var removeError: Error?
    var renameError: Error?
    var loopsByProject: [String: Ycc_V1_WorkLoopInfo] = [:]
    var loopErrorsByProject: [String: Error] = [:]
    private(set) var requestedProjects: [String] = []
    private(set) var listProjectsRequestCount = 0
    private(set) var removedProjects: [String] = []
    private(set) var renamedProjects: [(from: String, to: String)] = []
    private let lock = NSLock()

    func listSessionHistory(project: String) async throws -> [Ycc_V1_SessionSummary] {
        lock.lock()
        requestedProjects.append(project)
        let shouldFailTransiently = (historyFailuresRemainingByProject[project] ?? 0) > 0
        if shouldFailTransiently {
            historyFailuresRemainingByProject[project, default: 0] -= 1
        }
        let projectError = historyErrorsByProject[project]
        let generalError = historyError
        let response = sessionsByProject[project] ?? sessions
        let delay = historyDelayNanoseconds
        lock.unlock()

        if delay > 0 { try await Task.sleep(nanoseconds: delay) }
        if shouldFailTransiently { throw YccError.rpc(message: "transient history failure") }
        if let projectError { throw projectError }
        if let generalError { throw generalError }
        return response
    }

    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        lock.lock()
        listProjectsRequestCount += 1
        let shouldFailTransiently = projectFailuresRemaining > 0
        if shouldFailTransiently { projectFailuresRemaining -= 1 }
        let error = projectsError
        let response = projects
        lock.unlock()

        if shouldFailTransiently { throw YccError.rpc(message: "transient project failure") }
        if let error { throw error }
        return response
    }

    func removeProject(name: String) async throws {
        if let removeError { throw removeError }
        lock.lock()
        removedProjects.append(name)
        projects.removeAll { $0.name == name }
        lock.unlock()
    }

    func renameProject(name: String, to newName: String) async throws -> Ycc_V1_ProjectInfo {
        if let renameError { throw renameError }
        lock.lock()
        defer { lock.unlock() }
        renamedProjects.append((from: name, to: newName))
        guard let index = projects.firstIndex(where: { $0.name == name }) else {
            throw YccError.notFound(message: "unknown project \(name)")
        }
        projects[index].name = newName
        // Re-key any scripted history so a post-rename refresh finds it.
        if let history = sessionsByProject.removeValue(forKey: name) {
            sessionsByProject[newName] = history
        }
        return projects[index]
    }

    func workLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo? {
        if let error = loopErrorsByProject[project] { throw error }
        return loopsByProject[project]
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
        focusTasks: [String] = [],
        modelUsage: [(String, Int64)] = [],
        totalTokens: Int64 = 0
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
        s.modelUsage = modelUsage.map { model, tokens in
            var usage = Ycc_V1_SessionModelUsage()
            usage.model = model
            usage.tokens = tokens
            return usage
        }
        s.totalTokens = totalTokens
        return s
    }

    private func project(_ name: String, path: String? = nil) -> Ycc_V1_ProjectInfo {
        var p = Ycc_V1_ProjectInfo()
        p.name = name
        p.path = path ?? "/tmp/\(name)"
        return p
    }

    // MARK: - Row presentation

    func testDisplayTitleSeparatesTasksAndConservativelyRemovesBoilerplate() {
        let bracketed = session(id: "a", title: "[0198] Repair event durability", focusTasks: ["0198"])
        XCTAssertEqual(SessionListModel.displayTitle(for: bracketed), "Repair event durability")
        XCTAssertEqual(SessionListModel.taskChipLabels(for: bracketed), ["0198"])
        let multiBracket = session(id: "ab", title: "[0198,0200] Repair event durability", focusTasks: ["0198", "0200"])
        XCTAssertEqual(SessionListModel.displayTitle(for: multiBracket), "Repair event durability")

        let workPrompt = session(id: "b", title: "Work on task 0198: Repair event durability", focusTasks: ["0198"])
        XCTAssertEqual(SessionListModel.displayTitle(for: workPrompt), "Repair event durability")

        let dashed = session(id: "c", title: "0198 — Repair event durability", focusTasks: ["0198"])
        XCTAssertEqual(SessionListModel.displayTitle(for: dashed), "Repair event durability")

        let unrelated = session(id: "d", title: "Compare 0198 with the new event flow", focusTasks: ["0198"])
        XCTAssertEqual(SessionListModel.displayTitle(for: unrelated), "Compare 0198 with the new event flow")
    }

    func testTaskIDsDeduplicateAndCompact() {
        let value = session(id: "a", focusTasks: [" 0198 ", "0198", "", "0200", "0201", "0202"])
        XCTAssertEqual(SessionListModel.taskIDs(for: value), ["0198", "0200", "0201", "0202"])
        XCTAssertEqual(SessionListModel.taskChipLabels(for: value), ["0198", "0200", "+2"])
    }

    func testModelAndTokenSummaries() {
        let multi = session(
            id: "a",
            modelUsage: [("gpt", 100), ("claude", 800), ("glm", 100)],
            totalTokens: 1_250_000
        )
        XCTAssertEqual(SessionListModel.modelSummary(for: multi), "claude +2")
        XCTAssertEqual(SessionListModel.tokenSummary(for: multi), "1.2M tok")
        XCTAssertEqual(
            SessionListModel.metadataItems(for: multi, isLoopOwned: true),
            ["pm", "claude +2", "1.2M tok", "via loop"]
        )

        XCTAssertEqual(
            SessionListModel.modelSummary(for: session(id: "sole", modelUsage: [("claude", 42)])),
            "claude"
        )
        XCTAssertEqual(SessionListModel.tokenSummary(for: session(id: "b", totalTokens: 999)), "999 tok")
        XCTAssertEqual(SessionListModel.tokenSummary(for: session(id: "c", totalTokens: 12_000)), "12K tok")
    }

    func testTokenSummaryPromotesRoundedUnitBoundaries() {
        XCTAssertEqual(SessionListModel.tokenSummary(for: session(id: "k-low", totalTokens: 999_499)), "999K tok")
        XCTAssertEqual(SessionListModel.tokenSummary(for: session(id: "k-carry", totalTokens: 999_500)), "1M tok")
        XCTAssertEqual(SessionListModel.tokenSummary(for: session(id: "k-max", totalTokens: 999_999)), "1M tok")
        XCTAssertEqual(SessionListModel.tokenSummary(for: session(id: "m-low", totalTokens: 999_499_999)), "999M tok")
        XCTAssertEqual(SessionListModel.tokenSummary(for: session(id: "m-carry", totalTokens: 999_500_000)), "1B tok")
        XCTAssertEqual(SessionListModel.tokenSummary(for: session(id: "m-max", totalTokens: 999_999_999)), "1B tok")
    }

    func testModelSummaryTieBreakAndMissingMetadata() {
        let tie = session(id: "a", mode: "", modelUsage: [("zeta", 50), ("alpha", 50)], totalTokens: 100)
        XCTAssertEqual(SessionListModel.modelSummary(for: tie), "alpha +1")

        let missing = session(id: "b", mode: "", modelUsage: [("", 100), ("ignored", 0)])
        XCTAssertNil(SessionListModel.modelSummary(for: missing))
        XCTAssertNil(SessionListModel.tokenSummary(for: missing))
        XCTAssertEqual(SessionListModel.metadataItems(for: missing, isLoopOwned: false), [])
    }

    func testLifecycleSuppressesIdleAndPreservesExceptionalStates() {
        XCTAssertNil(SessionListModel.lifecycleLabel(for: session(id: "idle", status: "idle")))
        XCTAssertNil(SessionListModel.lifecycleLabel(for: session(id: "unknown", status: "legacy")))
        XCTAssertEqual(SessionListModel.lifecycleLabel(for: session(id: "run", status: "running")), "running")
        XCTAssertEqual(SessionListModel.lifecycleLabel(for: session(id: "pause", status: "paused")), "paused")
        XCTAssertEqual(SessionListModel.lifecycleLabel(for: session(id: "error", status: "error")), "error")
        XCTAssertEqual(SessionListModel.lifecycleLabel(for: session(id: "stop", status: "stopped")), "stopped")
        XCTAssertEqual(
            SessionListModel.lifecycleLabel(for: session(
                id: "wait", status: "running", live: true, waitingInput: true
            )),
            "waiting"
        )
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

    // MARK: - Work-loop ownership

    func testWorkLoopSnapshotMarksCompletedAndCurrentSessions() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject["one"] = [session(id: "done"), session(id: "current"), session(id: "other")]
        var completed = Ycc_V1_WorkLoopSession()
        completed.sessionID = "done"
        var loop = Ycc_V1_WorkLoopInfo()
        loop.sessions = [completed]
        loop.currentSessionID = "current"
        source.loopsByProject["one"] = loop
        let model = SessionListModel(source: source)

        await model.refresh()

        XCTAssertTrue(model.isLoopSession(sessionID: "done"))
        XCTAssertTrue(model.isLoopSession(sessionID: "current"))
        XCTAssertFalse(model.isLoopSession(sessionID: "other"))
        XCTAssertTrue(model.isLoopSession(model.sessions.first { $0.sessionID == "done" }!))
    }

    func testThrowingWorkLoopDoesNotDegradeHistoryOrWarn() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject["one"] = [session(id: "available")]
        source.loopErrorsByProject["one"] = YccError.rpc(message: "loop unavailable")
        let model = SessionListModel(source: source)

        await model.refresh()

        XCTAssertEqual(model.sessions.map(\.sessionID), ["available"])
        XCTAssertTrue(model.loopSessionIDs.isEmpty)
        XCTAssertNil(model.partialWarning)
        XCTAssertNil(model.errorMessage)
    }

    func testLoopIDsAreRebuiltEachRefresh() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject["one"] = [session(id: "old")]
        var record = Ycc_V1_WorkLoopSession()
        record.sessionID = "old"
        var loop = Ycc_V1_WorkLoopInfo()
        loop.sessions = [record]
        source.loopsByProject["one"] = loop
        let model = SessionListModel(source: source)

        await model.refresh()
        XCTAssertTrue(model.isLoopSession(sessionID: "old"))

        source.loopsByProject.removeValue(forKey: "one")
        await model.refresh()
        XCTAssertTrue(model.loopSessionIDs.isEmpty)
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

    func testRefreshProjectsUpdatesGitSnapshotWithoutReloadingHistory() async {
        let source = MockListSource()
        var stale = project("one")
        var staleGit = Ycc_V1_GitStatus()
        staleGit.fetchError = "offline"
        stale.git = staleGit
        source.projects = [stale]
        let model = SessionListModel(source: source, retryDelays: [])

        await model.refreshProjects()
        XCTAssertEqual(model.projects.first?.git.fetchError, "offline")

        var fresh = stale
        fresh.git.fetchError = ""
        fresh.git.lastFetchUnix = 123
        source.projects = [fresh]
        await model.refreshProjects()

        XCTAssertEqual(model.projects.first?.git.lastFetchUnix, 123)
        XCTAssertEqual(source.listProjectsRequestCount, 2)
        XCTAssertTrue(source.requestedProjects.isEmpty)
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

    func testPerProjectTransientFailureRetriesAndSucceeds() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject["one"] = [session(id: "available")]
        source.historyFailuresRemainingByProject["one"] = 1
        let model = SessionListModel(source: source, retryDelays: [0, 0])

        await model.refresh()

        XCTAssertEqual(source.requestedProjects, ["one", "one"])
        XCTAssertEqual(model.sessions.map(\.sessionID), ["available"])
        XCTAssertNil(model.partialWarning)
        XCTAssertNil(model.errorMessage)
    }

    func testRecentFeedPreservesPartialResultsAndWarnsAfterRetries() async {
        let source = MockListSource()
        source.projects = [project("good"), project("bad")]
        source.sessionsByProject["good"] = [session(id: "available")]
        source.historyErrorsByProject["bad"] = YccError.rpc(message: "offline")
        let model = SessionListModel(source: source, retryDelays: [0, 0])

        await model.refresh()

        XCTAssertEqual(model.sessions.map(\.sessionID), ["available"])
        XCTAssertEqual(model.partialWarning, "Some projects couldn’t be loaded: bad.")
        XCTAssertNil(model.errorMessage)
        XCTAssertEqual(source.requestedProjects.filter { $0 == "bad" }.count, 3)
    }

    func testFailedProjectRetainsLastKnownRowsAndRouting() async {
        let source = MockListSource()
        source.projects = [project("good"), project("failing")]
        source.sessionsByProject = [
            "good": [session(id: "fresh")],
            "failing": [session(id: "cached")],
        ]
        let model = SessionListModel(source: source, retryDelays: [0, 0])
        await model.refresh()

        source.historyErrorsByProject["failing"] = YccError.rpc(message: "offline")
        source.sessionsByProject["good"] = [session(id: "new-fresh")]
        await model.refresh()

        XCTAssertEqual(Set(model.sessions.map(\.sessionID)), Set(["new-fresh", "cached"]))
        let cached = model.sessions.first { $0.sessionID == "cached" }!
        XCTAssertEqual(model.project(for: cached), "failing")
        XCTAssertEqual(model.partialWarning, "Some projects couldn’t be loaded: failing.")
        XCTAssertNotNil(model.activityByProject["failing"])
        XCTAssertEqual(model.activity(forProject: "failing"), ProjectActivity())
    }

    func testListProjectsTransientFailureRetriesAndSucceeds() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject["one"] = [session(id: "available")]
        source.projectFailuresRemaining = 1
        let model = SessionListModel(source: source, retryDelays: [0, 0])

        await model.refresh()

        XCTAssertEqual(source.listProjectsRequestCount, 2)
        XCTAssertEqual(model.projects.map(\.name), ["one"])
        XCTAssertEqual(model.sessions.map(\.sessionID), ["available"])
        XCTAssertNil(model.errorMessage)
    }

    func testOverlappingRefreshesShareOneLoad() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject["one"] = [session(id: "available")]
        source.historyDelayNanoseconds = 50_000_000
        let model = SessionListModel(source: source, retryDelays: [0, 0])

        async let first: Void = model.refresh()
        async let second: Void = model.refresh()
        _ = await (first, second)

        XCTAssertEqual(source.listProjectsRequestCount, 1)
        XCTAssertEqual(source.requestedProjects, ["one"])
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

    func testNoProjectsFallbackHistoryRetriesAndSucceeds() async {
        let source = MockListSource()
        source.sessionsByProject[""] = [session(id: "fallback")]
        source.historyFailuresRemainingByProject[""] = 1
        let model = SessionListModel(source: source, retryDelays: [0, 0])

        await model.refresh()

        XCTAssertEqual(source.requestedProjects, ["", ""])
        XCTAssertEqual(model.sessions.map(\.sessionID), ["fallback"])
        XCTAssertNil(model.errorMessage)
    }

    func testProjectFilterRoundTrips() async {
        let source = MockListSource()
        let model = SessionListModel(source: source, selectedProject: "")

        await model.refresh()
        model.selectedProject = "myproj"
        await model.refresh()

        XCTAssertEqual(source.requestedProjects, ["", "myproj"])
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

    func testMarkAnsweredClearsTheNeedsAnswerBadgeImmediately() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject = [
            "one": [session(id: "asking", status: "running", live: true, waitingInput: true)],
        ]
        let model = SessionListModel(source: source)
        await model.refresh()
        XCTAssertEqual(model.activity(forProject: "one").needsAnswer, 1)

        model.markAnswered(sessionID: "asking")

        // No refetch: the badge and the row both stop nagging right away.
        XCTAssertEqual(model.activity(forProject: "one").needsAnswer, 0)
        XCTAssertEqual(model.totalActivity.needsAnswer, 0)
        XCTAssertFalse(model.sessions.first { $0.sessionID == "asking" }!.waitingInput)
        XCTAssertEqual(source.requestedProjects.count, 1)
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

    // MARK: - Rename project

    func testRenameProjectFollowsSelection() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        source.sessionsByProject = ["one": [session(id: "s1")], "two": []]
        let model = SessionListModel(source: source, selectedProject: "one")
        await model.refresh()

        let renamed = await model.renameProject(named: "one", to: "uno")

        XCTAssertTrue(renamed)
        XCTAssertEqual(source.renamedProjects.map(\.to), ["uno"])
        XCTAssertEqual(model.selectedProject, "uno")
        XCTAssertEqual(model.projects.map(\.name).sorted(), ["two", "uno"])
        // The renamed project's history still shows under the new scope.
        XCTAssertEqual(model.sessions.map(\.sessionID), ["s1"])
    }

    func testRenameProjectFailureKeepsNameAndSurfacesError() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.renameError = YccError.rpc(message: "name taken")
        let model = SessionListModel(source: source, selectedProject: "one")

        let renamed = await model.renameProject(named: "one", to: "two")

        XCTAssertFalse(renamed)
        XCTAssertEqual(model.selectedProject, "one")
        XCTAssertEqual(model.errorMessage, "name taken")
    }

    func testRenameProjectUnauthorizedSurfacesFlag() async {
        let source = MockListSource()
        source.renameError = YccError.unauthorized
        let model = SessionListModel(source: source)

        let renamed = await model.renameProject(named: "one", to: "two")

        XCTAssertFalse(renamed)
        XCTAssertTrue(model.unauthorized)
    }

    func testUnauthorizedSurfacesFlagWithoutRetrying() async {
        let source = MockListSource()
        source.projectsError = YccError.unauthorized
        let model = SessionListModel(source: source, retryDelays: [0, 0])

        await model.refresh()

        XCTAssertTrue(model.unauthorized)
        XCTAssertEqual(source.listProjectsRequestCount, 1)
        XCTAssertTrue(source.requestedProjects.isEmpty)
    }

    func testRpcErrorSurfacesMessage() async {
        let source = MockListSource()
        source.historyError = YccError.rpc(message: "boom")
        let model = SessionListModel(source: source, retryDelays: [0, 0])

        await model.refresh()

        XCTAssertEqual(model.errorMessage, "boom")
        XCTAssertFalse(model.unauthorized)
    }

    // MARK: - Unread agent activity

    /// A fresh, isolated read-mark store so unread tests never share state.
    private func makeReadStore() -> SessionReadStore { .ephemeral() }

    func testFirstLoadReportsNothingUnread() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject = ["one": [
            session(id: "a", lastActivity: "2026-08-06T10:00:00Z"),
        ]]
        let model = SessionListModel(source: source, readMarks: makeReadStore())

        await model.refresh()

        XCTAssertFalse(model.isUnread(model.sessions[0]))
        XCTAssertEqual(model.unreadCount, 0)
        XCTAssertEqual(model.totalActivity.unread, 0)
    }

    func testActivityAfterALoadMarksTheRowUnread() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject = ["one": [
            session(id: "a", lastActivity: "2026-08-06T10:00:00Z"),
        ]]
        let model = SessionListModel(source: source, readMarks: makeReadStore())
        await model.refresh()

        // The agent worked (and finished) while the phone was away.
        source.sessionsByProject = ["one": [
            session(id: "a", status: "idle", lastActivity: "2026-08-06T10:30:00Z", turns: 4),
        ]]
        await model.refresh()

        XCTAssertTrue(model.isUnread(model.sessions[0]))
        XCTAssertEqual(model.unreadCount, 1)
        XCTAssertEqual(model.totalActivity.unread, 1)
        XCTAssertEqual(model.activity(forProject: "one").unread, 1)
    }

    func testMarkReadClearsTheRowAndTheProjectBadge() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject = ["one": [
            session(id: "a", lastActivity: "2026-08-06T10:00:00Z"),
        ]]
        let model = SessionListModel(source: source, readMarks: makeReadStore())
        await model.refresh()
        source.sessionsByProject = ["one": [
            session(id: "a", lastActivity: "2026-08-06T10:30:00Z"),
        ]]
        await model.refresh()

        model.markRead(model.sessions[0])

        XCTAssertFalse(model.isUnread(model.sessions[0]))
        XCTAssertEqual(model.activity(forProject: "one").unread, 0)
    }

    func testMarkAllReadOnlyClearsTheSelectedScope() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        source.sessionsByProject = [
            "one": [session(id: "a", lastActivity: "2026-08-06T10:00:00Z")],
            "two": [session(id: "b", lastActivity: "2026-08-06T10:00:00Z")],
        ]
        let model = SessionListModel(source: source, readMarks: makeReadStore())
        await model.refresh()
        source.sessionsByProject = [
            "one": [session(id: "a", lastActivity: "2026-08-06T11:00:00Z")],
            "two": [session(id: "b", lastActivity: "2026-08-06T11:00:00Z")],
        ]
        await model.refresh()
        XCTAssertEqual(model.totalActivity.unread, 2)

        model.selectedProject = "one"
        model.markAllRead()

        XCTAssertEqual(model.activity(forProject: "one").unread, 0)
        XCTAssertEqual(model.activity(forProject: "two").unread, 1)
        XCTAssertEqual(model.totalActivity.unread, 1)
    }

    func testSessionViewedThroughItsLatestEventIsRead() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject = ["one": [
            session(id: "a", lastActivity: "2026-08-06T10:00:00Z"),
        ]]
        let readMarks = makeReadStore()
        let model = SessionListModel(source: source, readMarks: readMarks)
        await model.refresh()
        source.sessionsByProject = ["one": [
            session(id: "a", lastActivity: "2026-08-06T10:30:00Z"),
        ]]
        await model.refresh()
        XCTAssertTrue(model.isUnread(model.sessions[0]))

        // What the session view does on leaving: record the newest folded event.
        readMarks.markRead(sessionID: "a", through: "2026-08-06T10:30:00Z")

        XCTAssertFalse(model.isUnread(model.sessions[0]))
    }
}
