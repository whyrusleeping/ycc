import Foundation
import XCTest
import YccProto
@testable import YccKit

private final class MockWorkLoopSource: WorkLoopSource, @unchecked Sendable {
    var snapshot: Ycc_V1_WorkLoopInfo?
    var startSnapshot = Ycc_V1_WorkLoopInfo()
    var stopSnapshot: Ycc_V1_WorkLoopInfo?
    var refreshError: Error?
    var startErrors: [Error] = []
    var stopError: Error?
    private(set) var startCalls = 0

    func getWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo? {
        if let refreshError { throw refreshError }
        return snapshot
    }

    func startWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo {
        let call = startCalls
        startCalls += 1
        if call < startErrors.count { throw startErrors[call] }
        return startSnapshot
    }

    func stopWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo? {
        if let stopError { throw stopError }
        return stopSnapshot
    }
}

@MainActor
final class WorkLoopModelTests: XCTestCase {
    private func loop(
        state: String,
        current: String = "",
        sessionsRun: Int32 = 0,
        completed: [Ycc_V1_WorkLoopDigestTask] = [],
        blocked: [Ycc_V1_WorkLoopDigestTask] = [],
        inReview: [Ycc_V1_WorkLoopDigestTask] = [],
        created: [Ycc_V1_WorkLoopDigestTask] = [],
        resumeAt: String = "",
        waitKind: String = ""
    ) -> Ycc_V1_WorkLoopInfo {
        var value = Ycc_V1_WorkLoopInfo()
        value.project = "demo"
        value.state = state
        value.currentSessionID = current
        value.sessionsRun = sessionsRun
        value.completed = completed
        value.blocked = blocked
        value.inReview = inReview
        value.created = created
        value.resumeAt = resumeAt
        value.waitKind = waitKind
        return value
    }

    private func task(_ id: String, reason: String = "") -> Ycc_V1_WorkLoopDigestTask {
        var value = Ycc_V1_WorkLoopDigestTask()
        value.id = id
        value.title = "Task \(id)"
        value.reason = reason
        return value
    }

    func testRefreshWithNoLoopCanStartAndDoesNotPoll() async {
        let source = MockWorkLoopSource()
        let model = WorkLoopModel(source: source, project: "demo")

        await model.refresh()

        XCTAssertEqual(model.state, .none)
        XCTAssertTrue(model.canStart)
        XCTAssertFalse(model.canStop)
        XCTAssertFalse(model.shouldPoll)
        XCTAssertNil(model.loop)
    }

    func testRefreshRunningLoopExposesCurrentSessionAndSummary() async {
        let source = MockWorkLoopSource()
        source.snapshot = loop(
            state: "running", current: "session-current", sessionsRun: 3,
            completed: [task("1"), task("2")], blocked: [task("3")])
        let model = WorkLoopModel(source: source, project: "demo")

        await model.refresh()

        XCTAssertEqual(model.state, .running)
        XCTAssertEqual(model.currentSessionID, "session-current")
        XCTAssertEqual(WorkLoopModel.summaryLine(for: model.loop!), "3 sessions · 2 completed, 1 blocked")
        XCTAssertTrue(model.shouldPoll)
        XCTAssertTrue(model.canStop)
        XCTAssertFalse(model.canStart)
    }

    func testRefreshWaitingLoopPollsAndStopAppliesStoppingSnapshot() async {
        XCTAssertEqual(WorkLoopState(state: "waiting"), .waiting)
        XCTAssertEqual(WorkLoopState.waiting.title, "Waiting")
        XCTAssertTrue(WorkLoopState.waiting.isActive)
        XCTAssertTrue(WorkLoopState.waiting.shouldPoll)
        XCTAssertTrue(WorkLoopState.waiting.canStop)
        XCTAssertFalse(WorkLoopState.waiting.canStart)

        let source = MockWorkLoopSource()
        source.snapshot = loop(
            state: "waiting",
            resumeAt: "2026-08-06T10:20:30Z",
            waitKind: "rate_limit")
        let model = WorkLoopModel(source: source, project: "demo")

        await model.refresh()

        XCTAssertEqual(model.state, .waiting)
        XCTAssertTrue(model.shouldPoll)
        XCTAssertTrue(model.canStop)
        XCTAssertFalse(model.canStart)

        source.stopSnapshot = loop(state: "stopping")
        await model.stop()
        XCTAssertEqual(model.state, .stopping)
    }

    func testStartAppliesSnapshotAndFailedSecondStartPreservesIt() async {
        let source = MockWorkLoopSource()
        source.startSnapshot = loop(state: "running", current: "new-session", sessionsRun: 1)
        // First call succeeds because error list has no value at index zero; set
        // the second failure after the first call to keep the mock explicit.
        let model = WorkLoopModel(source: source, project: "demo")

        await model.start()
        source.startErrors = [
            YccError.rpc(message: "unused"),
            YccError.failedPrecondition(message: "a loop is already running"),
        ]
        await model.start()

        XCTAssertEqual(model.state, .running)
        XCTAssertEqual(model.currentSessionID, "new-session")
        XCTAssertEqual(model.actionError, "a loop is already running")
    }

    func testStopAppliesStoppingAndFinishedSnapshots() async {
        let source = MockWorkLoopSource()
        source.snapshot = loop(state: "running", current: "active")
        let model = WorkLoopModel(source: source, project: "demo")
        await model.refresh()

        source.stopSnapshot = loop(state: "stopping", current: "active")
        await model.stop()
        XCTAssertEqual(model.state, .stopping)
        XCTAssertFalse(model.canStop)
        XCTAssertTrue(model.shouldPoll)

        source.stopSnapshot = loop(state: "finished", sessionsRun: 1)
        await model.stop()
        XCTAssertEqual(model.state, .finished)
        XCTAssertTrue(model.canStart)
        XCTAssertFalse(model.shouldPoll)
    }

    func testUnauthorizedMapsForRefreshAndActions() async {
        let refreshSource = MockWorkLoopSource()
        refreshSource.refreshError = YccError.unauthorized
        let refreshModel = WorkLoopModel(source: refreshSource, project: "demo")
        await refreshModel.refresh()
        XCTAssertTrue(refreshModel.unauthorized)

        let startSource = MockWorkLoopSource()
        startSource.startErrors = [YccError.unauthorized]
        let startModel = WorkLoopModel(source: startSource, project: "demo")
        await startModel.start()
        XCTAssertTrue(startModel.unauthorized)
        XCTAssertNil(startModel.actionError)

        let stopSource = MockWorkLoopSource()
        stopSource.snapshot = loop(state: "running")
        stopSource.stopError = YccError.unauthorized
        let stopModel = WorkLoopModel(source: stopSource, project: "demo")
        await stopModel.refresh()
        await stopModel.stop()
        XCTAssertTrue(stopModel.unauthorized)
    }

    func testDigestSectionsAreOrderedSkipEmptyAndPreserveReason() {
        let value = loop(
            state: "finished",
            completed: [task("done")],
            blocked: [task("blocked", reason: "dependency missing")],
            created: [task("new")])

        let sections = WorkLoopModel.digestSections(for: value)

        XCTAssertEqual(sections.map(\.title), ["Completed", "Blocked", "Created"])
        XCTAssertEqual(sections[1].rows[0].reason, "dependency missing")
        XCTAssertTrue(WorkLoopModel(source: MockWorkLoopSource(), project: "demo").state == .none)
    }

    func testCostFormattingHonorsPricingStatus() {
        XCTAssertEqual(WorkLoopModel.formatCost(1.25, status: "priced"), "$1.2500")
        XCTAssertEqual(WorkLoopModel.formatCost(1.25, status: "partial"), "≈$1.2500 (partial)")
        XCTAssertEqual(WorkLoopModel.formatCost(1.25, status: "unpriced"), "unpriced")
        XCTAssertEqual(WorkLoopModel.formatCost(0, status: ""), "unpriced")

        var value = loop(state: "finished")
        value.totalTokens = 12_300
        value.totalCost = 1.25
        value.costStatus = "partial"
        XCTAssertEqual(WorkLoopModel.totalsLine(for: value), "12.3k tokens · ≈$1.2500 (partial)")
    }

    func testDurationText() {
        XCTAssertNil(WorkLoopModel.durationText(secs: 0))
        XCTAssertNil(WorkLoopModel.durationText(secs: -1))
        XCTAssertEqual(WorkLoopModel.durationText(secs: 45), "45s")
        XCTAssertEqual(WorkLoopModel.durationText(secs: 272), "4m 32s")
        XCTAssertEqual(WorkLoopModel.durationText(secs: 3_900), "1h 5m")
    }

    func testUnknownStateDoesNotCrash() {
        XCTAssertEqual(WorkLoopState(state: "future-state"), .unknown)
        XCTAssertEqual(WorkLoopModel.state(for: loop(state: "future-state")), .unknown)
    }

    func testBannerLineCoversWorkLoopStates() {
        XCTAssertEqual(WorkLoopModel.bannerLine(for: nil), "Not running")

        let running = loop(
            state: "running", sessionsRun: 3,
            completed: [task("done")], blocked: [task("blocked")])
        XCTAssertEqual(
            WorkLoopModel.bannerLine(for: running),
            "Running · 3 sessions · 1 completed, 1 blocked")

        let waiting = loop(
            state: "waiting",
            resumeAt: "2026-08-06T10:20:30Z",
            waitKind: "rate_limit")
        XCTAssertEqual(
            WorkLoopModel.bannerLine(for: waiting),
            WorkLoopModel.waitingLine(for: waiting))
        XCTAssertTrue(WorkLoopModel.bannerLine(for: waiting).contains("resumes"))

        XCTAssertEqual(
            WorkLoopModel.bannerLine(for: loop(state: "stopping")),
            "Stopping…")

        var finished = loop(
            state: "finished", sessionsRun: 2,
            completed: [task("done")], created: [task("new")])
        finished.outcome = "no ready tasks"
        XCTAssertEqual(
            WorkLoopModel.bannerLine(for: finished),
            "Finished · 2 sessions · 1 completed, 1 created")
    }

    func testWaitingLineComposition() {
        let complete = loop(
            state: "waiting",
            resumeAt: "2026-08-06T10:20:30Z",
            waitKind: "rate_limit")
        let completeLine = WorkLoopModel.waitingLine(for: complete)
        XCTAssertTrue(completeLine.hasPrefix("Waiting for provider (rate_limit)"))
        XCTAssertTrue(completeLine.contains("resumes"))

        let kindOnly = loop(state: "waiting", waitKind: "overloaded")
        XCTAssertEqual(WorkLoopModel.waitingLine(for: kindOnly), "Waiting for provider (overloaded)")
        XCTAssertFalse(WorkLoopModel.waitingLine(for: kindOnly).contains("resumes"))

        let bare = loop(state: "waiting")
        XCTAssertEqual(WorkLoopModel.waitingLine(for: bare), "Waiting for provider")

        let invalidResume = loop(state: "waiting", resumeAt: "not-a-timestamp")
        XCTAssertEqual(WorkLoopModel.waitingLine(for: invalidResume), "Waiting for provider")
        XCTAssertFalse(WorkLoopModel.waitingLine(for: invalidResume).contains("resumes"))
    }

    func testResumeAtParsesFractionalAndPlainRFC3339() {
        var value = loop(state: "waiting", resumeAt: "2026-08-06T10:20:30.123Z")
        XCTAssertNotNil(WorkLoopModel.resumeAtDate(for: value))
        value.resumeAt = "2026-08-06T10:20:30Z"
        XCTAssertNotNil(WorkLoopModel.resumeAtDate(for: value))
        value.resumeAt = ""
        XCTAssertNil(WorkLoopModel.resumeAtDate(for: value))
    }

    func testStartedAtParsesFractionalAndPlainRFC3339() {
        var value = loop(state: "running")
        value.startedAt = "2026-08-06T10:20:30.123Z"
        XCTAssertNotNil(WorkLoopModel.startedAtDate(for: value))
        value.startedAt = "2026-08-06T10:20:30Z"
        XCTAssertNotNil(WorkLoopModel.startedAtDate(for: value))
    }
}
