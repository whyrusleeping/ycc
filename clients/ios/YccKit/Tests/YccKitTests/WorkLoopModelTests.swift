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
    var preflight: Ycc_V1_GetWorkLoopPreflightResponse?
    var preflightError: Error?
    var backlog: [Ycc_V1_BacklogTaskSummary]?
    var models: Ycc_V1_ListModelsResponse?
    var budget: Ycc_V1_GetBudgetResponse?
    var setImplementationError: Error?
    private(set) var startCalls = 0
    private(set) var startParallels: [Int32] = []
    /// Calls in order: "impl:<value>" and "start".
    private(set) var calls: [String] = []

    func listBacklog(project: String) async throws -> [Ycc_V1_BacklogTaskSummary] {
        guard let backlog else { throw YccError.rpc(message: "unimplemented") }
        return backlog
    }

    func listModels() async throws -> Ycc_V1_ListModelsResponse {
        guard let models else { throw YccError.rpc(message: "unimplemented") }
        return models
    }

    func getBudget() async throws -> Ycc_V1_GetBudgetResponse {
        guard let budget else { throw YccError.rpc(message: "unimplemented") }
        return budget
    }

    func setWorkImplementation(_ implementation: String) async throws {
        calls.append("impl:\(implementation)")
        if let setImplementationError { throw setImplementationError }
    }

    func getWorkLoopPreflight(project: String) async throws -> Ycc_V1_GetWorkLoopPreflightResponse? {
        if let preflightError { throw preflightError }
        return preflight
    }

    func getWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo? {
        if let refreshError { throw refreshError }
        return snapshot
    }

    func startWorkLoop(project: String, parallel: Int32) async throws -> Ycc_V1_WorkLoopInfo {
        calls.append("start")
        startParallels.append(parallel)
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
        unfinished: [Ycc_V1_WorkLoopDigestTask] = [],
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
        value.unfinished = unfinished
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

    func testRefreshRunningLoopExposesCurrentSessionAndControls() async {
        let source = MockWorkLoopSource()
        source.snapshot = loop(
            state: "running", current: "session-current", sessionsRun: 3,
            completed: [task("1"), task("2")], blocked: [task("3")])
        let model = WorkLoopModel(source: source, project: "demo")

        await model.refresh()

        XCTAssertEqual(model.state, .running)
        XCTAssertEqual(model.currentSessionID, "session-current")
        XCTAssertTrue(model.shouldPoll)
        XCTAssertTrue(model.canStop)
        XCTAssertFalse(model.canStart)
    }

    func testRefreshWaitingLoopPollsAndStopAppliesStoppingSnapshot() async {
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

    func testStartupFailureSurfacesFromStartOrFirstPollAndDoesNotRepeat() async {
        let outcome = "loop stopped: build coordinator backend: Refresh token expired; re-run ycc login anthropic"
        for finishesInStart in [true, false] {
            let source = MockWorkLoopSource()
            var failed = loop(state: "finished")
            failed.outcome = outcome
            source.startSnapshot = finishesInStart ? failed : loop(state: "running")
            source.snapshot = failed
            let model = WorkLoopModel(source: source, project: "demo")

            await model.start()
            if !finishesInStart {
                XCTAssertNil(model.completionMessage)
                await model.refresh()
            }
            XCTAssertEqual(model.completionMessage, outcome)
            XCTAssertEqual(WorkLoopModel.bannerLine(for: model.loop), outcome)
            XCTAssertFalse(model.shouldPoll)
            XCTAssertTrue(model.canStart)
            XCTAssertNil(model.actionError)

            model.completionMessage = nil
            await model.refresh()
            XCTAssertNil(model.completionMessage)

            // Reopening retains the reason without repeatedly alerting for history.
            let reopened = WorkLoopModel(source: source, project: "demo")
            await reopened.refresh()
            XCTAssertNil(reopened.completionMessage)
            XCTAssertEqual(WorkLoopModel.bannerLine(for: reopened.loop), outcome)

            source.startSnapshot = loop(state: "running")
            await model.start()
            XCTAssertNil(model.completionMessage)
            XCTAssertTrue(model.shouldPoll)
        }
    }

    func testEmptyBacklogCompletionShowsOutcomeWithoutActionError() async {
        let source = MockWorkLoopSource()
        source.startSnapshot = loop(state: "finished")
        source.startSnapshot.outcome = "loop complete: no ready tasks remain"
        let model = WorkLoopModel(source: source, project: "demo")
        await model.start()
        XCTAssertNil(model.actionError)
        XCTAssertEqual(model.completionMessage, source.startSnapshot.outcome)
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
            unfinished: [task("unfinished")],
            created: [task("new")])

        let sections = WorkLoopModel.digestSections(for: value)

        XCTAssertEqual(sections.flatMap(\.rows).map(\.id), ["done", "blocked", "unfinished", "new"])
        XCTAssertEqual(sections[1].rows[0].reason, "dependency missing")
    }

    func testStartSendsParallelOnlyWhenChangedFromTheProjectDefault() async {
        let source = MockWorkLoopSource()
        source.startSnapshot = loop(state: "finished")
        let model = WorkLoopModel(source: source, project: "demo")
        XCTAssertNil(model.configuredParallel)
        XCTAssertEqual(model.parallel, 1, "unknown configured count shows 1")

        var response = Ycc_V1_GetWorkLoopPreflightResponse()
        response.configuredParallel = 3
        source.preflight = response
        await model.loadStartOptions()
        XCTAssertEqual(model.parallel, 3)
        await model.start()

        model.parallel = 3
        await model.start()
        model.parallel = 1
        await model.start()
        XCTAssertNil(model.parallelChoice, "a start clears the pick")
        XCTAssertEqual(model.parallel, 3)
        model.parallel = 40
        XCTAssertEqual(model.parallel, WorkLoopModel.maxParallel)
        await model.start()
        await model.start()
        XCTAssertEqual(source.startParallels, [0, 0, 1, 16, 0])
    }

    func testStartOptionsListReadyTasksAndBudgetCaps() async {
        let source = MockWorkLoopSource()
        let model = WorkLoopModel(source: source, project: "demo")
        await model.loadStartOptions()
        XCTAssertNil(model.readyTasks, "an unreadable backlog shows no list")
        XCTAssertNil(model.workImplementation)
        XCTAssertNil(model.budget)

        func summary(_ id: String, status: String, ready: Bool, priority: Int32) -> Ycc_V1_BacklogTaskSummary {
            var value = Ycc_V1_BacklogTaskSummary()
            value.id = id
            value.status = status
            value.ready = ready
            value.priority = priority
            return value
        }
        source.backlog = [
            summary("0003", status: "todo", ready: true, priority: 3),
            summary("0001", status: "todo", ready: false, priority: 1),
            summary("0002", status: "proposed", ready: true, priority: 1),
            summary("0004", status: "in_progress", ready: true, priority: 2),
        ]
        var budget = Ycc_V1_GetBudgetResponse()
        budget.sessionCost = 5
        budget.sessionTokens = 2_000_000
        source.budget = budget
        await model.loadStartOptions()
        XCTAssertEqual(model.readyTasks?.map(\.id), ["0004", "0003"])
        XCTAssertEqual(WorkLoopModel.budgetLine(budget), "Caps: $5.00 or 2.0M tokens per session · none per loop.")
        XCTAssertEqual(WorkLoopModel.budgetLine(Ycc_V1_GetBudgetResponse()), "No budget caps set.")
    }

    func testStartAppliesAChangedImplementationBeforeStarting() async {
        let source = MockWorkLoopSource()
        source.startSnapshot = loop(state: "running")
        var models = Ycc_V1_ListModelsResponse()
        models.workImplementation = "direct"
        source.models = models
        let model = WorkLoopModel(source: source, project: "demo")
        await model.loadStartOptions()
        XCTAssertEqual(model.implementation, "direct")

        model.implementation = "direct"
        XCTAssertNil(model.implementationChoice, "picking the current value is no change")
        await model.start()
        XCTAssertEqual(source.calls, ["start"])

        model.implementation = "delegate"
        source.setImplementationError = YccError.failedPrecondition(message: "nope")
        await model.start()
        XCTAssertEqual(source.calls, ["start", "impl:delegate"], "a failed change does not start the loop")
        XCTAssertNotNil(model.actionError)

        source.setImplementationError = nil
        await model.start()
        XCTAssertEqual(source.calls, ["start", "impl:delegate", "impl:delegate", "start"])
        XCTAssertEqual(model.workImplementation, "delegate")
        XCTAssertNil(model.implementationChoice)
    }

    func testPreflightWarnsAboutUncommittedInReviewWork() async {
        let source = MockWorkLoopSource()
        let model = WorkLoopModel(source: source, project: "demo")

        await model.loadStartOptions()
        XCTAssertNil(model.preflightWarning, "no preflight, no warning")

        var response = Ycc_V1_GetWorkLoopPreflightResponse()
        response.uncommittedInReview = [task("0366", reason: "uncommitted: a.go"), task("0371")]
        response.uncommittedPaths = 5
        source.preflight = response
        await model.loadStartOptions()
        XCTAssertEqual(model.preflightWarning, "2 in-review tasks have uncommitted changes in 5 files (0366, 0371)")

        source.preflightError = YccError.failedPrecondition(message: "boom")
        await model.loadStartOptions()
        XCTAssertNil(model.preflightWarning, "a failed preflight never blocks Start")

        var finished = loop(state: "finished")
        finished.uncommittedInReview = [task("0366", reason: "uncommitted: a.go")]
        let sections = WorkLoopModel.digestSections(for: finished)
        XCTAssertEqual(sections.map(\.title), [WorkLoopModel.uncommittedSectionTitle])
        XCTAssertEqual(sections.first?.rows.first?.reason, "uncommitted: a.go")
        XCTAssertEqual(
            WorkLoopModel.uncommittedInReviewLine(tasks: finished.uncommittedInReview, paths: 1),
            "1 in-review task has uncommitted changes in 1 file (0366)")
    }

    func testResourceEnvelopeMakesBoundedAndUnboundedDimensionsExplicit() {
        var value = loop(state: "running")
        value.sessionTokenLimit = 12_000
        value.sessionCostLimit = 1.25
        value.loopTokenLimit = 100_000
        value.loopCostLimit = 8.5
        value.costLimitsPricedOnly = true
        value.resourceEnvelopeCaptured = true

        let bounded = WorkLoopModel.resourceEnvelopeLines(for: value)
        XCTAssertEqual(bounded.count, 4)
        for fragment in ["12.0k tokens", "$1.25", "wall time unbounded"] {
            XCTAssertTrue(bounded[0].contains(fragment), "session line: \(bounded[0])")
        }
        for fragment in ["100.0k tokens", "$8.50", "wall time unbounded"] {
            XCTAssertTrue(bounded[1].contains(fragment), "loop line: \(bounded[1])")
        }
        XCTAssertTrue(bounded[2].contains("priced models only"))

        value = loop(state: "running")
        XCTAssertEqual(WorkLoopModel.resourceEnvelopeLines(for: value).count, 1,
                       "a pre-upgrade snapshot claims no limits at all")
        value.costLimitsPricedOnly = true
        value.resourceEnvelopeCaptured = true
        let unbounded = WorkLoopModel.resourceEnvelopeLines(for: value)
        for line in unbounded.prefix(2) {
            XCTAssertTrue(line.contains("tokens unbounded") && line.contains("cost unbounded"), line)
        }
        XCTAssertNotNil(WorkLoopModel.unboundedEnvelopeLine(for: value), "every limit unbounded collapses to one line")
        value.loopTimeLimitSecs = 3600
        XCTAssertNil(WorkLoopModel.unboundedEnvelopeLine(for: value), "any limit keeps the full envelope")
        XCTAssertNil(WorkLoopModel.unboundedEnvelopeLine(for: loop(state: "running")),
                     "a pre-upgrade snapshot keeps its unavailable line")
    }

    func testTaskTitleLooksUpDigestGroupsThenSlots() {
        var l = loop(state: "running")
        var created = Ycc_V1_WorkLoopDigestTask()
        created.id = "0007"
        created.title = "Created task"
        l.created = [created]
        var slot = Ycc_V1_WorkLoopSlot()
        slot.taskID = "0009"
        slot.taskTitle = "Slot task"
        l.slots = [slot]
        XCTAssertEqual(WorkLoopModel.taskTitle("0007", in: l), "Created task")
        XCTAssertEqual(WorkLoopModel.taskTitle("7", in: l), "Created task", "a short-form focus finds the padded digest id")
        XCTAssertEqual(WorkLoopModel.taskTitle("0009", in: l), "Slot task")
        XCTAssertEqual(WorkLoopModel.taskTitle("0001", in: l), "")
        XCTAssertEqual(WorkLoopModel.taskTitle("", in: l), "")
    }

    func testAttemptLabelOnlyForRetriedTasks() {
        func session(_ id: String, focus: String, attempt: Int32) -> Ycc_V1_WorkLoopSession {
            var s = Ycc_V1_WorkLoopSession()
            s.sessionID = id
            s.focus = focus
            s.attempt = attempt
            return s
        }
        var l = loop(state: "running")
        l.sessions = [
            session("s_a", focus: "0007", attempt: 1),
            session("s_b", focus: "0003", attempt: 1),
            session("s_c", focus: "7", attempt: 1),
            session("s_d", focus: "", attempt: 0),
        ]
        XCTAssertEqual(WorkLoopModel.attemptLabel(for: l.sessions[0], in: l), "attempt 1")
        XCTAssertNil(WorkLoopModel.attemptLabel(for: l.sessions[1], in: l), "a task's only attempt is unlabeled")
        XCTAssertEqual(WorkLoopModel.attemptLabel(for: l.sessions[2], in: l), "attempt 2", "ids compare canonically")
        XCTAssertNil(WorkLoopModel.attemptLabel(for: l.sessions[3], in: l))
    }

    func testWaitingLineNamesEachWaitKind() {
        XCTAssertEqual(WorkLoopModel.waitingLine(for: loop(state: "waiting", waitKind: "rate_limit")),
                       "Waiting for provider (rate_limit)")
        XCTAssertEqual(WorkLoopModel.waitingLine(for: loop(state: "waiting", waitKind: "no_progress")),
                       "Waiting: last session made no progress")
        XCTAssertEqual(WorkLoopModel.waitingLine(for: loop(state: "waiting", waitKind: "sessions_waiting")),
                       "Waiting: loop sessions are waiting on background jobs")
    }

    func testSlotsLineCountsWorkingOutOfParallelAndWaiting() {
        var l = loop(state: "running")
        XCTAssertEqual(WorkLoopModel.slotsLine(for: l), "")
        l.parallel = 3
        l.slots = ["working", "working", "waiting"].enumerated().map { index, state in
            var slot = Ycc_V1_WorkLoopSlot()
            slot.state = state
            slot.sessionID = "s_\(index)"
            return slot
        }
        XCTAssertEqual(WorkLoopModel.slotsLine(for: l), "2/3 working, 1 waiting")
        XCTAssertEqual(WorkLoopModel.bannerLine(for: l), "Running · 2/3 working, 1 waiting")
    }

    func testSlotStateTextTimesTheSlotInItsState() {
        let now = ISO8601DateFormatter().date(from: "2026-11-25T10:00:00Z")!
        func slot(_ state: String, _ since: String) -> Ycc_V1_WorkLoopSlot {
            var s = Ycc_V1_WorkLoopSlot()
            s.state = state
            s.since = since
            return s
        }
        XCTAssertEqual(WorkLoopModel.slotStateText(slot("waiting", "2026-11-25T06:40:00Z"), now: now), "waiting 3h 20m")
        XCTAssertEqual(WorkLoopModel.slotStateText(slot("working", "2026-11-25T09:58:30Z"), now: now), "working 1m 30s")
        XCTAssertEqual(WorkLoopModel.slotStateText(slot("working", ""), now: now), "working")
        var single = loop(state: "running")
        single.parallel = 1
        single.slots = [slot("working", "")]
        XCTAssertFalse(WorkLoopModel.isParallel(single))
        single.parallel = 3
        XCTAssertTrue(WorkLoopModel.isParallel(single))
    }

    func testStopConfirmationCountsWorkingAndWaitingSessions() {
        func slots(_ states: [String]) -> [Ycc_V1_WorkLoopSlot] {
            states.map { state in
                var s = Ycc_V1_WorkLoopSlot()
                s.state = state
                s.sessionID = "s"
                return s
            }
        }
        var l = loop(state: "running")
        XCTAssertEqual(WorkLoopModel.stopConfirmMessage(for: l),
                       "The current session finishes its work; the daemon will not pick another backlog task.")
        l.parallel = 4
        l.slots = slots(["working", "working", "working", "waiting"])
        XCTAssertEqual(WorkLoopModel.stopConfirmMessage(for: l),
                       "3 working sessions finish their work. 1 session waiting on its background jobs keeps running. The daemon will not pick another backlog task.")
        var waiting = loop(state: "waiting", waitKind: "sessions_waiting")
        waiting.parallel = 2
        waiting.slots = slots(["waiting", "waiting"])
        XCTAssertEqual(WorkLoopModel.stopConfirmMessage(for: waiting),
                       "2 sessions waiting on their background jobs keep running. The daemon will not pick another backlog task.")
        var mixed = loop(state: "running")
        mixed.parallel = 2
        mixed.slots = slots(["working", "working", "waiting"])
        mixed.slots[1].detached = true
        XCTAssertEqual(WorkLoopModel.stopConfirmMessage(for: mixed),
                       "1 working session finishes its work. 2 sessions let wait on background jobs keep running (1 working again). The daemon will not pick another backlog task.")
        var provider = loop(state: "waiting", waitKind: "rate_limit")
        provider.parallel = 2
        XCTAssertEqual(WorkLoopModel.stopConfirmMessage(for: provider),
                       "The loop is waiting for the provider; stopping ends the wait at once. The daemon will not pick another backlog task.")
    }

    func testSummaryCountsEveryDoneTaskLiveSessionsAndOtherCommits() {
        func status(_ id: String, _ status: String) -> Ycc_V1_WorkLoopDigestTask {
            var value = task(id)
            value.status = status
            return value
        }
        func slot(_ state: String, _ sessionID: String = "s_live") -> Ycc_V1_WorkLoopSlot {
            var value = Ycc_V1_WorkLoopSlot()
            value.state = state
            value.sessionID = sessionID
            return value
        }
        var l = loop(
            state: "running", sessionsRun: 11,
            completed: [status("0001", "done"), status("0002", "done")],
            blocked: [status("0003", "blocked")],
            created: [status("0010", "done"), status("0011", "todo"), status("0012", "done"), status("0013", "proposed")])
        l.parallel = 4
        l.slots = [slot("working"), slot("waiting")]
        var landing = Ycc_V1_WorkLoopLanding()
        landing.sha = "abc"
        l.manualLandings = [landing]

        XCTAssertEqual(
            WorkLoopModel.summaryLine(for: l),
            "11 sessions finished, 2 live · 4 completed, 1 blocked, 2 created still open, 1 other commit")
        // The slots line beside it already names the live sessions.
        XCTAssertEqual(
            WorkLoopModel.bannerLine(for: l),
            "Running · 1/4 working, 1 waiting · 11 sessions finished · 4 completed, 1 blocked, 2 created still open, 1 other commit")
        let sections = WorkLoopModel.digestSections(for: l)
        XCTAssertEqual(sections[0].rows.map(\.id), ["0001", "0002", "0010", "0012"])
        XCTAssertEqual(sections.last?.title, "Created, still open")
        XCTAssertEqual(sections.last?.rows.map(\.id), ["0011", "0013"])

        XCTAssertEqual(WorkLoopModel.summaryLine(for: loop(state: "finished", sessionsRun: 1)), "1 session")
        var onlyLive = loop(state: "running")
        onlyLive.slots = [slot("working")]
        XCTAssertEqual(WorkLoopModel.summaryLine(for: onlyLive), "1 live session")
        // A slot still starting has no session yet.
        var starting = loop(state: "running", sessionsRun: 11)
        starting.slots = [slot("working"), slot("working", "")]
        XCTAssertEqual(WorkLoopModel.summaryLine(for: starting), "11 sessions finished, 1 live")
        // A finished (restored) loop's slots are history, not live sessions.
        var restored = loop(state: "finished", sessionsRun: 11)
        restored.slots = [slot("waiting", "s_old")]
        XCTAssertEqual(WorkLoopModel.summaryLine(for: restored), "11 sessions")
    }

    func testDigestTotalsOmitTasksNoSessionWorked() {
        XCTAssertNil(WorkLoopModel.digestTotalsLine(for: task("0011")))
        var worked = task("0001")
        worked.attempts = 1
        XCTAssertEqual(WorkLoopModel.digestTotalsLine(for: worked), "0 tokens · unpriced")
    }

    func testUnknownStateDoesNotCrash() {
        XCTAssertEqual(WorkLoopState(state: "future-state"), .unknown)
        XCTAssertEqual(WorkLoopModel.state(for: loop(state: "future-state")), .unknown)
    }

    func testWorkLoopDatesParseRFC3339Variants() {
        var value = loop(state: "waiting", resumeAt: "2026-08-06T10:20:30.123Z")
        value.startedAt = "2026-08-06T10:20:30Z"
        XCTAssertNotNil(WorkLoopModel.resumeAtDate(for: value))
        XCTAssertNotNil(WorkLoopModel.startedAtDate(for: value))

        value.resumeAt = ""
        XCTAssertNil(WorkLoopModel.resumeAtDate(for: value))
    }
}
