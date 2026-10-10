import Foundation
import XCTest
import YccProto
@testable import YccKit

/// Explicit gates keep slow-request tests independent of network/timer ordering.
private actor ListLoadGate {
    private var opened = false
    private var waiters: [CheckedContinuation<Void, Never>] = []

    func wait() async {
        if opened { return }
        await withCheckedContinuation { waiters.append($0) }
    }

    func open() {
        opened = true
        for waiter in waiters { waiter.resume() }
        waiters.removeAll()
    }
}

/// A scripted in-memory ``SessionListSource`` for headless model tests. Records
/// the project passed to each history query so the filter round-trip is testable.
private final class MockListSource: SessionListSource, @unchecked Sendable {
    var sessions: [Ycc_V1_SessionSummary] = []
    var sessionsByProject: [String: [Ycc_V1_SessionSummary]] = [:]
    var pagesByProject: [String: [String: SessionHistoryPage]] = [:]
    var projects: [Ycc_V1_ProjectInfo] = []
    var historyError: Error?
    var historyErrorsByProject: [String: Error] = [:]
    var historyFailuresRemainingByProject: [String: Int] = [:]
    var projectsError: Error?
    var projectFailuresRemaining = 0
    var historyDelayNanoseconds: UInt64 = 0
    var historyGates: [String: ListLoadGate] = [:]
    var historyPageGates: [String: [String: ListLoadGate]] = [:]
    private var requestedHistoryPages: [String] = []
    func didRequestHistoryPage(_ cursor: String) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return requestedHistoryPages.contains(cursor)
    }
    var loopGates: [String: ListLoadGate] = [:]
    var projectsGate: ListLoadGate?
    var removeError: Error?
    var renameError: Error?
    var followUpError: Error?
    var followUpGate: ListLoadGate?
    var followUpTimestamp = "2026-01-01T12:00:00Z"
    private var followUpRequests: [(project: String, sessionID: String, flagged: Bool)] = []
    func recordedFollowUpRequests() -> [(project: String, sessionID: String, flagged: Bool)] {
        lock.lock()
        defer { lock.unlock() }
        return followUpRequests
    }
    var loopsByProject: [String: Ycc_V1_WorkLoopInfo] = [:]
    var loopErrorsByProject: [String: Error] = [:]
    private(set) var requestedProjects: [String] = []
    func requested(_ project: String) -> Int {
        lock.lock()
        defer { lock.unlock() }
        return requestedProjects.filter { $0 == project }.count
    }
    func setProjects(_ value: [Ycc_V1_ProjectInfo]) {
        lock.lock()
        projects = value
        lock.unlock()
    }
    private(set) var listProjectsRequestCount = 0
    private(set) var removedProjects: [String] = []
    private(set) var renamedProjects: [(from: String, to: String)] = []
    private let lock = NSLock()

    func listSessionHistory(project: String, limit: Int32, cursor: String) async throws -> SessionHistoryPage {
        lock.lock()
        requestedProjects.append(project)
        requestedHistoryPages.append(cursor)
        let shouldFailTransiently = (historyFailuresRemainingByProject[project] ?? 0) > 0
        if shouldFailTransiently {
            historyFailuresRemainingByProject[project, default: 0] -= 1
        }
        let projectError = historyErrorsByProject[project]
        let generalError = historyError
        let response = pagesByProject[project]?[cursor] ?? SessionHistoryPage(
            sessions: sessionsByProject[project] ?? sessions, pinned: [], nextCursor: "")
        let delay = historyDelayNanoseconds
        let gate = historyPageGates[project]?[cursor] ?? historyGates[project]
        lock.unlock()

        await gate?.wait()
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
        let gate = projectsGate
        lock.unlock()

        await gate?.wait()
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

    func setSessionFollowUp(project: String, sessionID: String, followUp: Bool) async throws -> Ycc_V1_SetSessionFollowUpResponse {
        lock.lock()
        followUpRequests.append((project, sessionID, followUp))
        let gate = followUpGate
        let error = followUpError
        let timestamp = followUpTimestamp
        lock.unlock()
        await gate?.wait()
        if let error { throw error }
        var response = Ycc_V1_SetSessionFollowUpResponse()
        response.followUp = followUp
        response.followUpAt = followUp ? timestamp : ""
        return response
    }

    func workLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo? {
        lock.lock()
        let error = loopErrorsByProject[project]
        let loop = loopsByProject[project]
        let gate = loopGates[project]
        lock.unlock()
        await gate?.wait()
        if let error { throw error }
        return loop
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
        awaitingJobs: Bool = false,
        focusTasks: [String] = [],
        modelUsage: [(String, Int64)] = [],
        totalTokens: Int64 = 0,
        contextTokens: Int64 = 0,
        workspace: String = ""
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
        s.awaitingJobs = awaitingJobs
        s.focusTasks = focusTasks
        s.modelUsage = modelUsage.map { model, tokens in
            var usage = Ycc_V1_SessionModelUsage()
            usage.model = model
            usage.tokens = tokens
            return usage
        }
        s.totalTokens = totalTokens
        s.contextTokens = contextTokens
        s.workspace = workspace
        return s
    }

    private func project(_ name: String, path: String? = nil) -> Ycc_V1_ProjectInfo {
        var p = Ycc_V1_ProjectInfo()
        p.name = name
        p.path = path ?? "/tmp/\(name)"
        return p
    }

    func testFollowUpOptimisticFlagAndManualClearUpdateLoadedCopies() async {
        let source = MockListSource()
        source.projects = [project("one")]
        let row = session(id: "flag", live: true, waitingInput: true)
        source.pagesByProject["one"] = [
            "": SessionHistoryPage(sessions: [row], pinned: [row], nextCursor: "older"),
            "older": SessionHistoryPage(sessions: [session(id: "older")], pinned: [], nextCursor: "")]
        let model = SessionListModel(source: source, retryDelays: [])
        await model.refresh()
        model.showsFollowUpOnly = true
        XCTAssertTrue(model.sessions.isEmpty)
        let gate = ListLoadGate()
        source.followUpGate = gate
        let flag = Task { await model.setFollowUp(sessionID: "flag", followUp: true) }
        while source.recordedFollowUpRequests().isEmpty { await Task.yield() }
        XCTAssertEqual(model.sessions.map(\.sessionID), ["flag"])
        XCTAssertTrue(model.allSessions[0].followUp)
        XCTAssertEqual(model.followUpCount, 1)
        model.selectedProject = "one"
        XCTAssertTrue(model.sessions[0].followUp)
        await gate.open()
        let flagged = await flag.value
        XCTAssertTrue(flagged)
        XCTAssertEqual(model.sessions[0].followUpAt, source.followUpTimestamp)
        XCTAssertEqual(source.recordedFollowUpRequests().first?.project, "one")
        model.markRead(model.sessions[0])
        model.markAnswered(sessionID: "flag")
        XCTAssertTrue(model.sessions[0].followUp, "Read and answer must not clear follow-up")
        await model.loadMoreHistory()
        XCTAssertEqual(model.sessions[0].followUpAt, source.followUpTimestamp)
        let cleared = await model.setFollowUp(sessionID: "flag", followUp: false)
        XCTAssertTrue(cleared)
        XCTAssertTrue(model.sessions.isEmpty)
        XCTAssertEqual(model.followUpCount, 0)
        XCTAssertFalse(model.session(sessionID: "flag")!.followUp)
        XCTAssertEqual(model.session(sessionID: "flag")!.followUpAt, "")
        XCTAssertEqual(source.recordedFollowUpRequests().last?.flagged, false)
    }

    func testFollowUpFailureRollsBackFlagAndTimestamp() async {
        let source = MockListSource()
        var row = session(id: "flag")
        row.followUp = true
        row.followUpAt = "2025-12-01T00:00:00Z"
        source.sessions = [row]
        let model = SessionListModel(source: source, retryDelays: [])
        await model.refresh()
        let gate = ListLoadGate()
        source.followUpGate = gate
        source.followUpError = YccError.rpc(message: "offline")
        let clear = Task { await model.setFollowUp(sessionID: "flag", followUp: false) }
        while source.recordedFollowUpRequests().isEmpty { await Task.yield() }
        XCTAssertFalse(model.allSessions[0].followUp)
        XCTAssertEqual(model.allSessions[0].followUpAt, "")
        await gate.open()
        let cleared = await clear.value
        XCTAssertFalse(cleared)
        XCTAssertEqual(model.allSessions[0].followUp, row.followUp)
        XCTAssertEqual(model.allSessions[0].followUpAt, row.followUpAt)
        XCTAssertEqual(model.followUpErrorMessage, "offline")
        // A flag failure is not a list-load failure.
        XCTAssertNil(model.errorMessage)
        XCTAssertTrue(model.followUpUpdatingIDs.isEmpty)
    }

    func testFollowUpUnauthorizedRollsBackAndRoutes() async {
        let source = MockListSource()
        source.sessions = [session(id: "flag")]
        source.followUpError = YccError.unauthorized
        let model = SessionListModel(source: source, retryDelays: [])
        await model.refresh()
        let flagged = await model.setFollowUp(sessionID: "flag", followUp: true)
        XCTAssertFalse(flagged)
        XCTAssertFalse(model.allSessions[0].followUp)
        XCTAssertTrue(model.unauthorized)
        XCTAssertNil(model.followUpErrorMessage)
    }

    func testFollowUpFilterAndCountTrackScopeAndCachedSections() async {
        let source = MockListSource()
        source.projects = [project("a"), project("b")]
        var flagged = session(id: "a-flag", live: true, waitingInput: true)
        flagged.followUp = true
        var other = session(id: "b-flag")
        other.followUp = true
        source.sessionsByProject = ["a": [flagged, session(id: "plain")], "b": [other]]
        let model = SessionListModel(source: source, retryDelays: [])
        await model.refresh()
        XCTAssertEqual(model.sessions.count, 3)
        XCTAssertEqual(model.followUpCount, 2)
        model.showsFollowUpOnly = true
        XCTAssertEqual(model.sessions.map(\.sessionID), ["a-flag", "b-flag"])
        XCTAssertEqual(model.sections.flatMap(\.sessions).map(\.sessionID), ["a-flag", "b-flag"])
        model.selectedProject = "a"
        XCTAssertEqual(model.followUpCount, 1)
        XCTAssertEqual(model.sections.map(\.kind), [.needsAnswer])
        XCTAssertEqual(model.sessions.map(\.sessionID), ["a-flag"])
        model.showsFollowUpOnly = false
        XCTAssertEqual(model.sessions.count, 2)
        XCTAssertEqual(model.followUpCount, 1)
        model.selectedProject = "b"
        XCTAssertEqual(model.followUpCount, 1)
        XCTAssertEqual(model.sessions.map(\.sessionID), ["b-flag"])
    }

    func testFollowUpForUnloadedSessionUsesExplicitProject() async {
        let source = MockListSource()
        let model = SessionListModel(source: source)
        let flagged = await model.setFollowUp(sessionID: "unloaded", project: "deep-link", followUp: true)
        XCTAssertTrue(flagged)
        XCTAssertEqual(source.recordedFollowUpRequests().first?.project, "deep-link")
        XCTAssertEqual(source.recordedFollowUpRequests().first?.sessionID, "unloaded")
        XCTAssertTrue(model.allSessions.isEmpty)
    }

    func testPagedAggregateFrontierAndPinnedLiveRows() async {
        let source = MockListSource()
        source.projects = [project("a"), project("b")]
        func row(_ id: String, _ seconds: Int, live: Bool = false) -> Ycc_V1_SessionSummary {
            session(id: id, startedAt: "2026-01-01T00:00:00Z",
                lastActivity: String(format: "2026-01-01T00:00:%02dZ", seconds), live: live)
        }
        var updatedLive = row("live", 1, live: true)
        updatedLive.title = "newer copy"
        source.pagesByProject = [
            "a": [
                "": SessionHistoryPage(sessions: [row("a10", 10), row("a8", 8)],
                    pinned: [row("live", 1, live: true)], nextCursor: "a1"),
                "a1": SessionHistoryPage(sessions: [row("a6", 6), updatedLive],
                    pinned: [], nextCursor: "")],
            "b": [
                "": SessionHistoryPage(sessions: [row("b9", 9), row("b7", 7)],
                    pinned: [], nextCursor: "b1"),
                "b1": SessionHistoryPage(sessions: [row("b5", 5)], pinned: [], nextCursor: "")],
        ]
        let model = SessionListModel(source: source, retryDelays: [])
        await model.refresh()
        XCTAssertEqual(model.allSessions.map(\.sessionID), ["a10", "b9", "a8", "live"])
        XCTAssertTrue(model.hasMoreHistory)
        XCTAssertEqual(model.project(for: model.allSessions.last!), "a")
        model.selectedProject = "b"
        XCTAssertEqual(model.sessions.map(\.sessionID), ["b9", "b7"])
        model.selectedProject = nil
        await model.loadMoreHistory()
        XCTAssertEqual(model.allSessions.map(\.sessionID), ["a10", "b9", "a8", "b7", "a6", "b5", "live"])
        XCTAssertFalse(model.hasMoreHistory)
        XCTAssertEqual(model.allSessions.filter { $0.sessionID == "live" }.count, 1)
        XCTAssertEqual(model.allSessions.last?.title, "newer copy")
    }

    func testAggregateFrontierUsesMillisecondWireTimestampsAndIDTies() async {
        let source = MockListSource()
        source.projects = [project("a"), project("b")]
        let started = "2026-01-01T00:00:00.456Z"
        let sameMS = "2026-01-01T00:00:10.123Z"
        let parsed = SessionListModel.parseTimestamp(sameMS)
        XCTAssertNotNil(parsed)
        XCTAssertEqual(parsed!.timeIntervalSince1970, 1_767_225_610.123, accuracy: 0.000_001)
        XCTAssertEqual(SessionListModel.recencyDate(session(id: "x", lastActivity: sameMS)), parsed)
        source.pagesByProject = [
            "a": ["": SessionHistoryPage(sessions: [
                session(id: "head", startedAt: started, lastActivity: "2026-01-01T00:00:10.124Z"),
                session(id: "b", startedAt: started, lastActivity: sameMS)],
                pinned: [], nextCursor: "older")],
            "b": ["": SessionHistoryPage(sessions: [
                session(id: "a", startedAt: started, lastActivity: sameMS),
                session(id: "c", startedAt: started, lastActivity: sameMS)],
                pinned: [], nextCursor: "")],
        ]
        let model = SessionListModel(source: source, retryDelays: [])
        await model.refresh()
        XCTAssertEqual(model.allSessions.map(\.sessionID), ["head", "a", "b"])
        XCTAssertTrue(model.hasMoreHistory)
    }

    func testRefreshDuringOlderPageDiscardsStaleResult() async {
        let source = MockListSource()
        source.projects = [project("one")]
        let old = session(id: "old", lastActivity: "2026-01-01T00:00:02.000Z")
        let fresh = session(id: "fresh", status: "running", lastActivity: "2026-01-01T00:00:03.000Z",
            live: true, waitingInput: true)
        source.pagesByProject["one"] = [
            "": SessionHistoryPage(sessions: [old], pinned: [], nextCursor: "old-cursor"),
            "old-cursor": SessionHistoryPage(sessions: [session(id: "stale")], pinned: [], nextCursor: ""),
        ]
        let model = SessionListModel(source: source, retryDelays: [])
        await model.refresh()
        let gate = ListLoadGate()
        source.historyPageGates["one"] = ["old-cursor": gate]
        let older = Task { await model.loadMoreHistory() }
        await eventually { source.didRequestHistoryPage("old-cursor") }
        source.pagesByProject["one"]?[""] = SessionHistoryPage(
            sessions: [fresh], pinned: [], nextCursor: "fresh-cursor")
        await model.refresh() // finishes while the older page is still suspended
        await gate.open()
        await older.value
        XCTAssertEqual(model.allSessions.map(\.sessionID), ["fresh"])
        XCTAssertTrue(model.allSessions[0].waitingInput)
        XCTAssertTrue(model.allSessions[0].live)
        XCTAssertTrue(model.hasMoreHistory)
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

    func testModelAndContextSummaries() {
        let multi = session(
            id: "a",
            modelUsage: [("gpt", 100), ("claude", 800), ("glm", 100)],
            totalTokens: 9_999_999,
            contextTokens: 1_250_000
        )
        XCTAssertEqual(SessionListModel.modelSummary(for: multi), "claude +2")
        // The metadata line reports how full the active context is, not the
        // session's cumulative token spend.
        XCTAssertEqual(SessionListModel.contextSummary(for: multi), "1.2M ctx")
        XCTAssertEqual(
            SessionListModel.metadataItems(for: multi, isLoopOwned: true),
            ["pm", "claude +2", "1.2M ctx", "via loop"]
        )

        XCTAssertEqual(
            SessionListModel.modelSummary(for: session(id: "sole", modelUsage: [("claude", 42)])),
            "claude"
        )
        XCTAssertEqual(SessionListModel.contextSummary(for: session(id: "b", contextTokens: 999)), "999 ctx")
        XCTAssertEqual(SessionListModel.contextSummary(for: session(id: "c", contextTokens: 12_000)), "12K ctx")
    }

    func testContextSummaryPromotesRoundedUnitBoundaries() {
        XCTAssertEqual(SessionListModel.contextSummary(for: session(id: "k-low", contextTokens: 999_499)), "999K ctx")
        XCTAssertEqual(SessionListModel.contextSummary(for: session(id: "k-carry", contextTokens: 999_500)), "1M ctx")
        XCTAssertEqual(SessionListModel.contextSummary(for: session(id: "k-max", contextTokens: 999_999)), "1M ctx")
        XCTAssertEqual(SessionListModel.contextSummary(for: session(id: "m-low", contextTokens: 999_499_999)), "999M ctx")
        XCTAssertEqual(SessionListModel.contextSummary(for: session(id: "m-carry", contextTokens: 999_500_000)), "1B ctx")
        XCTAssertEqual(SessionListModel.contextSummary(for: session(id: "m-max", contextTokens: 999_999_999)), "1B ctx")
    }

    func testModelSummaryTieBreakAndMissingMetadata() {
        let tie = session(id: "a", mode: "", modelUsage: [("zeta", 50), ("alpha", 50)], totalTokens: 100)
        XCTAssertEqual(SessionListModel.modelSummary(for: tie), "alpha +1")

        let missing = session(id: "b", mode: "", modelUsage: [("", 100), ("ignored", 0)])
        XCTAssertNil(SessionListModel.modelSummary(for: missing))
        // Cumulative spend without context telemetry (older daemon) shows no
        // context item rather than a misleading zero.
        XCTAssertNil(SessionListModel.contextSummary(for: session(id: "c", totalTokens: 5_000)))
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

    func testIdleAwaitingDelegatedWorkReadsAsActive() {
        let awaiting = session(id: "bg", status: "idle", live: true, awaitingJobs: true)
        XCTAssertEqual(SessionListModel.lifecycleLabel(for: awaiting), "background")
        var counts = ProjectActivity()
        SessionListModel.accumulate(awaiting, into: &counts)
        SessionListModel.accumulate(session(id: "idle", status: "idle", live: true), into: &counts)
        XCTAssertEqual(counts.active, 1, "only the session still waiting on delegated work is active")
        // A persisted (non-live) row never carries live job state.
        XCTAssertNil(SessionListModel.lifecycleLabel(for: session(id: "old", status: "idle", awaitingJobs: true)))
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

    func testEmptyProjectListStillCountsAsLoaded() async {
        let source = MockListSource()
        let model = SessionListModel(source: source, retryDelays: [])
        XCTAssertFalse(model.hasLoadedProjects, "the initial empty list is not authoritative")

        await model.refresh()
        XCTAssertTrue(model.hasLoadedProjects, "an empty ListProjects is still a real answer")
        XCTAssertTrue(model.projects.isEmpty)

        let failing = MockListSource()
        failing.projectsError = YccError.rpc(message: "offline")
        let unloaded = SessionListModel(source: failing, retryDelays: [])
        await unloaded.refreshProjects()
        XCTAssertFalse(unloaded.hasLoadedProjects)
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

    /// Wait only for model publication; the simulated RPCs themselves remain
    /// gated until the test explicitly releases them.
    private func eventually(_ condition: () -> Bool, file: StaticString = #filePath, line: UInt = #line) async {
        for _ in 0..<200 {
            if condition() { return }
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        XCTFail("Model did not publish the expected state", file: file, line: line)
    }

    func testHistoryPublishesBeforeOtherProjectsAndLoopBadges() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        source.sessionsByProject = ["one": [session(id: "one")], "two": [session(id: "two")]]
        let slowHistory = ListLoadGate()
        let slowLoop = ListLoadGate()
        source.historyGates["one"] = slowHistory
        source.loopGates = ["one": slowLoop, "two": slowLoop]
        var loop = Ycc_V1_WorkLoopInfo()
        loop.currentSessionID = "two"
        source.loopsByProject["two"] = loop
        // An immediate partial-publication valve: a slow project must not hold
        // back projects that already answered.
        let model = SessionListModel(source: source, retryDelays: [], partialPublishDelay: 0)
        let refresh = Task { await model.refresh() }

        await eventually { model.allSessions.map(\.sessionID) == ["two"] }
        XCTAssertTrue(model.isLoading)
        XCTAssertEqual(model.sessionProjects["two"], "two")
        XCTAssertEqual(model.unreadCount, 0)
        XCTAssertFalse(model.isLoopSession(sessionID: "two"))
        XCTAssertNil(model.partialWarning)

        await slowHistory.open()
        await eventually { !model.isLoading }
        // Equal dates use session ID, not asynchronous completion order.
        XCTAssertEqual(model.allSessions.map(\.sessionID), ["one", "two"])
        await slowLoop.open()
        await refresh.value
        XCTAssertTrue(model.isLoopSession(sessionID: "two"))
    }

    func testProgressiveRefreshRetainsPendingAndFailedProjectRows() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        source.sessionsByProject = [
            "one": [session(id: "old", lastActivity: "2026-07-08T08:00:00Z")],
            "two": [session(id: "cached", lastActivity: "2026-07-08T09:00:00Z")],
        ]
        let model = SessionListModel(source: source, retryDelays: [], partialPublishDelay: 0)
        await model.refresh()
        let slowHistory = ListLoadGate()
        source.historyGates["two"] = slowHistory
        source.historyErrorsByProject["two"] = YccError.rpc(message: "offline")
        source.sessionsByProject["one"] = [session(id: "fresh", lastActivity: "2026-07-08T10:00:00Z")]
        let refresh = Task { await model.refresh() }

        await eventually { model.allSessions.contains { $0.sessionID == "fresh" } }
        XCTAssertEqual(model.allSessions.map(\.sessionID), ["fresh", "cached"])
        XCTAssertEqual(model.sessionProjects["cached"], "two")
        XCTAssertNil(model.partialWarning)
        await slowHistory.open()
        await refresh.value
        XCTAssertEqual(model.allSessions.map(\.sessionID), ["fresh", "cached"])
        XCTAssertEqual(model.partialWarning, "Some projects couldn’t be loaded: two.")
    }

    func testDuplicateRoutingConvergesRegardlessOfCompletionOrder() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        source.sessionsByProject = [
            "one": [session(id: "same", title: "primary")],
            "two": [session(id: "same", title: "secondary")],
        ]
        let slowHistory = ListLoadGate()
        source.historyGates["one"] = slowHistory
        let model = SessionListModel(source: source, retryDelays: [], partialPublishDelay: 0)
        let refresh = Task { await model.refresh() }
        await eventually { model.sessionProjects["same"] == "two" }
        await slowHistory.open()
        await refresh.value
        XCTAssertEqual(model.allSessions.map(\.title), ["primary"])
        XCTAssertEqual(model.sessionProjects["same"], "one")
    }

    func testProgressiveUnreadBaselineUsesOneAggregateWatermark() async {
        // With no prior watermark the whole first list is read. With an existing
        // watermark both newly discovered sessions are unread, regardless of which
        // one completes first (the earlier response must not advance the baseline).
        for hasPriorWatermark in [false, true] {
            for reverseOrder in [false, true] {
                let marks = SessionReadStore.ephemeral()
                if hasPriorWatermark {
                    marks.noteSeen([session(id: "known", lastActivity: "2026-07-08T09:00:00Z")])
                }
                let source = MockListSource()
                source.projects = [project("one"), project("two")]
                source.sessionsByProject = [
                    "one": [session(id: "one", lastActivity: "2026-07-08T10:00:00Z")],
                    "two": [session(id: "two", lastActivity: "2026-07-08T11:00:00Z")],
                ]
                let gate = ListLoadGate()
                source.historyGates[reverseOrder ? "one" : "two"] = gate
                let model = SessionListModel(
                    source: source, readMarks: marks, retryDelays: [], partialPublishDelay: 0)
                let refresh = Task { await model.refresh() }
                await eventually { model.allSessions.count == 1 }
                await gate.open()
                await refresh.value
                XCTAssertEqual(model.unreadCount, hasPriorWatermark ? 2 : 0)
            }
        }
    }

    func testFailedSupplementalRequestRetainsBadgesButSuccessfulNilClearsThem() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject["one"] = [session(id: "owned")]
        var loop = Ycc_V1_WorkLoopInfo()
        loop.currentSessionID = "owned"
        source.loopsByProject["one"] = loop
        let model = SessionListModel(source: source, retryDelays: [])
        await model.refresh()
        XCTAssertTrue(model.isLoopSession(sessionID: "owned"))

        source.historyErrorsByProject["one"] = YccError.rpc(message: "offline")
        source.loopErrorsByProject["one"] = YccError.rpc(message: "offline")
        await model.refresh()
        XCTAssertEqual(model.allSessions.map(\.sessionID), ["owned"])
        XCTAssertTrue(model.isLoopSession(sessionID: "owned"))

        source.loopErrorsByProject = [:]
        source.loopsByProject = [:]
        await model.refresh()
        XCTAssertFalse(model.isLoopSession(sessionID: "owned"))
    }

    func testNoProjectFallbackDoesNotWaitForLoop() async {
        let source = MockListSource()
        source.sessions = [session(id: "fallback")]
        let slowLoop = ListLoadGate()
        source.loopGates[""] = slowLoop
        let model = SessionListModel(source: source, retryDelays: [])
        let refresh = Task { await model.refresh() }
        await eventually { !model.allSessions.isEmpty }
        XCTAssertFalse(model.isLoading)
        XCTAssertEqual(model.sessionProjects["fallback"], "")
        await slowLoop.open()
        await refresh.value
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

    func testDisplayProjectFallsBackToWorkspaceFolderName() async {
        let source = MockListSource()
        // A daemon with no registered projects: rows route under "" (resolve
        // server-side), so the display name must come from the workspace path.
        source.sessionsByProject[""] = [
            session(id: "routed-empty", workspace: "/home/me/code/oldgrowth"),
            session(id: "no-workspace"),
        ]
        let model = SessionListModel(source: source, retryDelays: [0, 0])

        await model.refresh()

        let routed = model.sessions.first { $0.sessionID == "routed-empty" }!
        XCTAssertEqual(model.project(for: routed), "", "routing name must stay empty for server-side resolution")
        XCTAssertEqual(model.displayProject(for: routed), "oldgrowth")
        let bare = model.sessions.first { $0.sessionID == "no-workspace" }!
        XCTAssertEqual(model.displayProject(for: bare), "")
    }

    func testDisplayProjectPrefersRegisteredName() async {
        let source = MockListSource()
        source.projects = [project("pretty")]
        source.sessionsByProject["pretty"] = [
            session(id: "named", workspace: "/srv/ugly-dir-name")
        ]
        let model = SessionListModel(source: source)

        await model.refresh()

        XCTAssertEqual(model.displayProject(for: model.sessions[0]), "pretty")
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

    // MARK: - Refresh cost on a remote link (0402)

    func testPassiveRefreshIsThrottledAfterASuccessfulRefresh() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject["one"] = [session(id: "a")]
        let clock = TestClock()
        let model = SessionListModel(
            source: source, retryDelays: [], staleAfter: 10, clock: { clock.now })

        await model.refreshIfStale() // never refreshed: loads
        XCTAssertEqual(source.listProjectsRequestCount, 1)

        clock.now = 5
        await model.refreshIfStale() // fresh: skipped
        XCTAssertEqual(source.listProjectsRequestCount, 1)
        XCTAssertEqual(source.requested("one"), 1)

        await model.refresh() // explicit (pull-to-refresh / post-mutation): forced
        XCTAssertEqual(source.listProjectsRequestCount, 2)

        clock.now = 14 // 9 s after the forced refresh
        await model.refreshIfStale()
        XCTAssertEqual(source.listProjectsRequestCount, 2)

        clock.now = 16
        await model.refreshIfStale()
        XCTAssertEqual(source.listProjectsRequestCount, 3)
    }

    func testFailedRefreshDoesNotThrottleTheNextPassiveRefresh() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.historyErrorsByProject["one"] = YccError.rpc(message: "offline")
        let clock = TestClock()
        let model = SessionListModel(source: source, retryDelays: [], clock: { clock.now })

        await model.refreshIfStale()
        XCTAssertEqual(model.errorMessage, "offline")
        clock.now = 1
        source.historyErrorsByProject = [:]
        source.sessionsByProject["one"] = [session(id: "back")]
        await model.refreshIfStale()

        XCTAssertEqual(source.listProjectsRequestCount, 2)
        XCTAssertEqual(model.sessions.map(\.sessionID), ["back"])
        XCTAssertNil(model.errorMessage)
    }

    func testRefreshPublishesOnceWhenEveryProjectHasAnswered() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two"), project("three")]
        source.sessionsByProject = [
            "one": [session(id: "1", lastActivity: "2026-07-08T08:00:00Z")],
            "two": [session(id: "2", lastActivity: "2026-07-08T09:00:00Z")],
            "three": [session(id: "3", lastActivity: "2026-07-08T10:00:00Z")],
        ]
        let slow = ListLoadGate()
        source.historyGates["three"] = slow
        let model = SessionListModel(source: source, retryDelays: [], partialPublishDelay: nil)
        let refresh = Task { await model.refresh() }

        await eventually { source.requestedProjects.count == 3 }
        try? await Task.sleep(nanoseconds: 50_000_000) // let "one"/"two" land
        XCTAssertEqual(model.publicationCount, 0, "no per-project publication")
        XCTAssertTrue(model.allSessions.isEmpty)
        XCTAssertTrue(model.isLoading)

        await slow.open()
        await refresh.value
        XCTAssertEqual(model.publicationCount, 1)
        XCTAssertEqual(model.allSessions.map(\.sessionID), ["3", "2", "1"])
        XCTAssertFalse(model.isLoading)

        // A cached-project refresh is also a single publication.
        await model.refresh()
        XCTAssertEqual(model.publicationCount, 2)
    }

    func testPartialPublicationValveReleasesRowsBehindASlowProject() async {
        let source = MockListSource()
        source.projects = [project("fast"), project("slow")]
        source.sessionsByProject = ["fast": [session(id: "f")], "slow": [session(id: "s")]]
        let slow = ListLoadGate()
        source.historyGates["slow"] = slow
        let model = SessionListModel(source: source, retryDelays: [], partialPublishDelay: 0.05)
        let refresh = Task { await model.refresh() }

        await eventually { model.allSessions.map(\.sessionID) == ["f"] }
        XCTAssertTrue(model.isLoading)
        await slow.open()
        await refresh.value
        XCTAssertEqual(Set(model.allSessions.map(\.sessionID)), ["f", "s"])
    }

    func testCachedProjectsFanOutConcurrentlyWithListProjects() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        source.sessionsByProject = [
            "one": [session(id: "one-row")],
            "two": [session(id: "two-row")],
            "three": [session(id: "three-row")],
        ]
        let model = SessionListModel(source: source, retryDelays: [], partialPublishDelay: nil)
        await model.refresh() // first load: no cache, ListProjects first
        XCTAssertEqual(source.requested("one"), 1)

        // Hold ListProjects: the history fan-out for the cached projects must
        // not wait for it (one round trip, not two).
        let projectsGate = ListLoadGate()
        source.projectsGate = projectsGate
        // The project set changes daemon-side meanwhile: "one" removed,
        // "three" registered.
        source.setProjects([project("two"), project("three")])
        let refresh = Task { await model.refresh() }
        await eventually { source.requested("one") == 2 && source.requested("two") == 2 }
        XCTAssertEqual(source.requested("three"), 0)
        XCTAssertEqual(model.publicationCount, 1, "nothing published before ListProjects answers")

        await projectsGate.open()
        await refresh.value

        XCTAssertEqual(source.requested("three"), 1, "a newly registered project is fetched")
        XCTAssertEqual(model.projects.map(\.name), ["two", "three"])
        XCTAssertEqual(Set(model.allSessions.map(\.sessionID)), ["two-row", "three-row"],
            "a removed project's rows are dropped")
        XCTAssertNil(model.sessionProjects["one-row"])
        XCTAssertEqual(model.sessionProjects["three-row"], "three")
        XCTAssertNil(model.activityByProject["one"])
        XCTAssertNotNil(model.activityByProject["three"])
        XCTAssertEqual(model.publicationCount, 2, "one publication for the whole refresh")
    }

    func testListProjectsFailureKeepsCachedProjectHistoriesAndReportsError() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject["one"] = [session(id: "old")]
        let model = SessionListModel(source: source, retryDelays: [])
        await model.refresh()

        source.projectsError = YccError.rpc(message: "registry unavailable")
        source.sessionsByProject["one"] = [session(id: "new")]
        await model.refresh()

        XCTAssertEqual(model.sessions.map(\.sessionID), ["new"])
        XCTAssertEqual(model.projects.map(\.name), ["one"])
        XCTAssertEqual(model.errorMessage, "registry unavailable")
    }

    func testIdempotentRefreshDoesNotInvalidateDerivedRows() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject["one"] = [session(id: "a", lastActivity: "2026-07-08T08:00:00Z")]
        let model = SessionListModel(source: source, retryDelays: [])
        await model.refresh()
        let revision = model.dataRevision
        await model.refresh()
        XCTAssertEqual(model.dataRevision, revision, "unchanged rows must not re-render the list")
        source.sessionsByProject["one"] = [session(id: "a", lastActivity: "2026-07-08T09:00:00Z")]
        await model.refresh()
        XCTAssertNotEqual(model.dataRevision, revision)
    }

    func testLoadMoreHistoryPagesProjectsConcurrently() async {
        let source = MockListSource()
        source.projects = [project("a"), project("b")]
        source.pagesByProject = [
            "a": ["": SessionHistoryPage(sessions: [session(id: "a1")], pinned: [], nextCursor: "a-next"),
                  "a-next": SessionHistoryPage(sessions: [session(id: "a2")], pinned: [], nextCursor: "")],
            "b": ["": SessionHistoryPage(sessions: [session(id: "b1")], pinned: [], nextCursor: "b-next"),
                  "b-next": SessionHistoryPage(sessions: [session(id: "b2")], pinned: [], nextCursor: "")],
        ]
        let model = SessionListModel(source: source, retryDelays: [])
        await model.refresh()
        let gate = ListLoadGate()
        source.historyPageGates["a"] = ["a-next": gate]
        let more = Task { await model.loadMoreHistory() }
        // b's page is requested while a's is still outstanding.
        await eventually { source.didRequestHistoryPage("a-next") && source.didRequestHistoryPage("b-next") }
        await gate.open()
        await more.value
        XCTAssertEqual(Set(model.allSessions.map(\.sessionID)), ["a1", "a2", "b1", "b2"])
        XCTAssertFalse(model.hasMoreHistory)
    }

    func testCachedActivityInvalidatesOnEveryKindOfMarkRead() async {
        let source = MockListSource()
        source.projects = [project("one")]
        source.sessionsByProject = ["one": [
            session(id: "a", lastActivity: "2026-08-06T10:00:00Z"),
            session(id: "b", lastActivity: "2026-08-06T10:00:00Z"),
            session(id: "c", lastActivity: "2026-08-06T10:00:00Z"),
        ]]
        let readMarks = makeReadStore()
        let model = SessionListModel(source: source, readMarks: readMarks)
        await model.refresh()
        source.sessionsByProject = ["one": [
            session(id: "a", lastActivity: "2026-08-06T11:00:00Z"),
            session(id: "b", lastActivity: "2026-08-06T11:00:00Z"),
            session(id: "c", lastActivity: "2026-08-06T11:00:00Z"),
        ]]
        await model.refresh()
        // Prime the caches.
        XCTAssertEqual(model.activity(forProject: "one").unread, 3)
        XCTAssertEqual(model.totalActivity.unread, 3)

        // Through the model (row swipe).
        model.markRead(model.sessions.first { $0.sessionID == "a" }!)
        XCTAssertEqual(model.activity(forProject: "one").unread, 2)
        XCTAssertEqual(model.totalActivity.unread, 2)
        XCTAssertEqual(model.activityByProject["one"]?.unread, 2)

        // Directly on the shared store (the session view marking on exit).
        readMarks.markRead(sessionID: "b", through: "2026-08-06T11:00:00Z")
        XCTAssertEqual(model.activity(forProject: "one").unread, 1)
        XCTAssertEqual(model.totalActivity.unread, 1)
        XCTAssertEqual(model.unreadCount, 1)

        // A no-op mark does not change anything.
        let revision = readMarks.revision
        readMarks.markRead(sessionID: "b", through: "2026-08-06T11:00:00Z")
        XCTAssertEqual(readMarks.revision, revision)

        model.markAllRead()
        XCTAssertEqual(model.totalActivity.unread, 0)
    }

    func testScopedSessionsAreCachedUntilDataOrScopeChanges() async {
        let source = MockListSource()
        source.projects = [project("one"), project("two")]
        source.sessionsByProject = [
            "one": [session(id: "old", lastActivity: "2026-07-08T08:00:00Z"),
                    session(id: "new", lastActivity: "2026-07-08T12:00:00Z", live: true, waitingInput: true)],
            "two": [session(id: "other", lastActivity: "2026-07-08T10:00:00Z")],
        ]
        let model = SessionListModel(source: source)
        await model.refresh()
        model.selectedProject = "one"
        XCTAssertEqual(model.sessions.map(\.sessionID), ["new", "old"])
        XCTAssertEqual(model.sections.map(\.kind), [.needsAnswer, .all])
        model.markAnswered(sessionID: "new")
        XCTAssertEqual(model.sections.map(\.kind), [.all], "a local correction invalidates the cache")
        model.selectedProject = nil
        XCTAssertEqual(model.sessions.map(\.sessionID), ["new", "other", "old"])
        XCTAssertEqual(model.sections.count, 1)
    }

    func testDisplayTitleCachedExpressionsStayCorrectAcrossCalls() {
        for _ in 0..<3 {
            let upper = session(id: "u", title: "WORK ON TASK 0402: Perf", focusTasks: ["0402"])
            XCTAssertEqual(SessionListModel.displayTitle(for: upper), "Perf")
            let colon = session(id: "c", title: "0402: Perf", focusTasks: ["0402"])
            XCTAssertEqual(SessionListModel.displayTitle(for: colon), "Perf")
            let plain = session(id: "p", title: "Perf 0402", focusTasks: ["0402"])
            XCTAssertEqual(SessionListModel.displayTitle(for: plain), "Perf 0402")
        }
    }
}

/// A manually advanced clock for the refresh throttle.
private final class TestClock: @unchecked Sendable {
    private let lock = NSLock()
    private var value: TimeInterval = 0
    var now: TimeInterval {
        get { lock.lock(); defer { lock.unlock() }; return value }
        set { lock.lock(); value = newValue; lock.unlock() }
    }
}
