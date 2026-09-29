import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``WorkstreamsSource`` for headless model tests. Records
/// action args and lets each RPC be stubbed or made to throw.
private final class MockWorkstreamsSource: WorkstreamsSource, @unchecked Sendable {
    var workstreams: [Ycc_V1_WorkstreamInfo] = []
    var projects: [Ycc_V1_ProjectInfo] = []
    var listError: Error?

    var previewResult: (clean: Bool, conflicts: [String], diff: String) = (true, [], "")
    var previewError: Error?

    var mergeResult: (merged: Bool, commit: String, needsAccept: Bool, diff: String, conflicts: [String])
        = (false, "", false, "", [])
    var mergeError: Error?
    var mergeErrorsByID: [String: Error] = [:]

    var discardError: Error?
    var retryError: Error?

    private(set) var listCount = 0
    private(set) var lastMergeArgs: (workstreamId: String, accept: Bool)?
    private(set) var mergeCalls: [(workstreamId: String, accept: Bool)] = []
    private(set) var lastPreviewId: String?
    private(set) var lastDiscardId: String?
    private(set) var lastRetryId: String?

    func listWorkstreams(project: String) async throws -> [Ycc_V1_WorkstreamInfo] {
        listCount += 1
        if let listError { throw listError }
        return workstreams
    }

    private(set) var listProjectsCount = 0
    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        listProjectsCount += 1
        return projects
    }

    func previewMerge(workstreamId: String) async throws
        -> (clean: Bool, conflicts: [String], diff: String)
    {
        lastPreviewId = workstreamId
        if let previewError { throw previewError }
        return previewResult
    }

    func mergeWorkstream(workstreamId: String, accept: Bool) async throws
        -> (merged: Bool, commit: String, needsAccept: Bool, diff: String, conflicts: [String])
    {
        lastMergeArgs = (workstreamId, accept)
        mergeCalls.append((workstreamId, accept))
        if let error = mergeErrorsByID[workstreamId] { throw error }
        if let mergeError { throw mergeError }
        return mergeResult
    }

    func discardWorkstream(workstreamId: String) async throws {
        lastDiscardId = workstreamId
        if let discardError { throw discardError }
    }

    func retryIntegration(workstreamId: String) async throws -> Ycc_V1_WorkstreamInfo {
        lastRetryId = workstreamId
        if let retryError { throw retryError }
        if let existing = workstreams.first(where: { $0.id == workstreamId }) { return existing }
        var retried = Ycc_V1_WorkstreamInfo()
        retried.id = workstreamId
        retried.status = "ready"
        return retried
    }
}

private func workstream(
    id: String = "ws_abcdef01", project: String = "proj", branch: String = "ycc/ws/x",
    sessionId: String = "sess-1", taskId: String = "", status: String = "active",
    sessionStatus: String = "", statusReason: String = "",
    integrateSessionId: String = "", integrationState: String = "", integrationMode: String = ""
) -> Ycc_V1_WorkstreamInfo {
    var w = Ycc_V1_WorkstreamInfo()
    w.id = id
    w.project = project
    w.branch = branch
    w.sessionID = sessionId
    w.taskID = taskId
    w.status = status
    w.sessionStatus = sessionStatus
    w.statusReason = statusReason
    w.integrateSessionID = integrateSessionId
    w.integrationState = integrationState
    w.integrationMode = integrationMode
    return w
}

@MainActor
final class WorkstreamsModelTests: XCTestCase {
    // MARK: - Action eligibility

    func testMergeableOnlyForInFlightStatuses() {
        XCTAssertTrue(WorkstreamStatus.active.isMergeable)
        XCTAssertTrue(WorkstreamStatus.ready.isMergeable)
        XCTAssertTrue(WorkstreamStatus.needsAttention.isMergeable)
        XCTAssertFalse(WorkstreamStatus.stale.isMergeable)
        XCTAssertFalse(WorkstreamStatus.merged.isMergeable)
        XCTAssertFalse(WorkstreamStatus.discarded.isMergeable)
        XCTAssertFalse(WorkstreamStatus.unknown.isMergeable)
    }

    func testDiscardableForInFlightOrStaleStatuses() {
        XCTAssertTrue(WorkstreamStatus.active.isDiscardable)
        XCTAssertTrue(WorkstreamStatus.ready.isDiscardable)
        XCTAssertTrue(WorkstreamStatus.needsAttention.isDiscardable)
        XCTAssertTrue(WorkstreamStatus.stale.isDiscardable)
        XCTAssertFalse(WorkstreamStatus.merged.isDiscardable)
        XCTAssertFalse(WorkstreamStatus.discarded.isDiscardable)
        XCTAssertFalse(WorkstreamStatus.unknown.isDiscardable)
    }

    func testIntegrationStateAndAttentionDetails() {
        let integrating = workstream(integrationState: "integrating")
        let queued = workstream(status: "ready", integrationState: "queued", integrationMode: "auto")
        let gated = workstream(status: "ready", integrationMode: "gate")
        let attention = workstream(
            status: "needs_attention", statusReason: "verification failed",
            integrateSessionId: "s-integrate")

        XCTAssertEqual(WorkstreamsModel.integrationState(for: integrating), .integrating)
        XCTAssertEqual(WorkstreamsModel.integrationState(for: queued), .queued)
        XCTAssertTrue(WorkstreamsModel.isGateEligible(gated))
        XCTAssertEqual(WorkstreamsModel.statusReason(for: attention), "verification failed")
        XCTAssertEqual(WorkstreamsModel.integrateSessionID(for: attention), "s-integrate")
    }

    // MARK: - Refresh

    func testRefreshLoadsWorkstreamsAndProjects() async {
        let source = MockWorkstreamsSource()
        source.workstreams = [workstream(id: "ws_1"), workstream(id: "ws_2")]
        source.projects = [ {
            var p = Ycc_V1_ProjectInfo(); p.name = "a"; return p
        }(), {
            var p = Ycc_V1_ProjectInfo(); p.name = "b"; return p
        }() ]
        let model = WorkstreamsModel(source: source)

        await model.refresh()

        XCTAssertEqual(model.workstreams.count, 2)
        XCTAssertTrue(model.hasWorkstreams)
        XCTAssertTrue(model.showsProjectFilter)
        XCTAssertNil(model.errorMessage)
    }

    func testRefreshSurfacesRpcError() async {
        let source = MockWorkstreamsSource()
        source.listError = YccError.rpc(message: "boom")
        let model = WorkstreamsModel(source: source)
        await model.refresh()
        XCTAssertEqual(model.errorMessage, "boom")
        XCTAssertFalse(model.unauthorized)
    }

    func testRefreshSurfacesUnauthorized() async {
        let source = MockWorkstreamsSource()
        source.listError = YccError.unauthorized
        let model = WorkstreamsModel(source: source)
        await model.refresh()
        XCTAssertTrue(model.unauthorized)
    }

    // MARK: - Preview

    func testPreviewCleanReturnsDiff() async {
        let source = MockWorkstreamsSource()
        source.previewResult = (true, [], "diff-body")
        let model = WorkstreamsModel(source: source)
        let outcome = await model.preview(workstream(id: "ws_x"))
        XCTAssertEqual(outcome, .clean(diff: "diff-body"))
        XCTAssertEqual(source.lastPreviewId, "ws_x")
        XCTAssertNil(model.busyWorkstreamID)
    }

    func testPreviewConflictsReturnsPaths() async {
        let source = MockWorkstreamsSource()
        source.previewResult = (false, ["a.swift", "b.swift"], "")
        let model = WorkstreamsModel(source: source)
        let outcome = await model.preview(workstream())
        XCTAssertEqual(outcome, .conflicts(["a.swift", "b.swift"]))
    }

    // MARK: - Merge accept-gate state machine

    func testMergeNeedsAcceptThenAcceptMerges() async {
        let source = MockWorkstreamsSource()
        source.workstreams = [workstream(id: "ws_x")]
        // First pass: clean but review-gated.
        source.mergeResult = (false, "", true, "gated-diff", [])
        let model = WorkstreamsModel(source: source)

        let first = await model.merge(workstream(id: "ws_x"), accept: false)
        XCTAssertEqual(first, .needsAccept(diff: "gated-diff"))
        XCTAssertEqual(source.lastMergeArgs?.accept, false)

        // Second pass with accept=true: integrated.
        source.mergeResult = (true, "deadbeef", false, "", [])
        let second = await model.merge(workstream(id: "ws_x"), accept: true)
        XCTAssertEqual(second, .merged(commit: "deadbeef"))
        XCTAssertEqual(source.lastMergeArgs?.accept, true)
        // Merged revalidates the list in the background.
        await model.backgroundRefreshTask?.value
        XCTAssertGreaterThanOrEqual(source.listCount, 1)
    }

    func testMergedRowFlipsImmediatelyWithoutWaitingForTheList() async {
        let source = MockWorkstreamsSource()
        source.workstreams = [workstream(id: "ws_x", status: "ready")]
        let model = WorkstreamsModel(source: source)
        await model.refresh()
        let before = source.listCount
        source.mergeResult = (true, "deadbeef", false, "", [])

        let outcome = await model.merge(workstream(id: "ws_x"), accept: true)

        XCTAssertEqual(outcome, .merged(commit: "deadbeef"))
        XCTAssertNil(model.busyWorkstreamID, "the row is released after the merge RPC alone")
        XCTAssertEqual(model.workstreams.first?.status, "merged")
        XCTAssertEqual(source.listCount, before, "no list reload is awaited")
        await model.backgroundRefreshTask?.value
        XCTAssertEqual(source.listCount, before + 1)
    }

    func testMergeConflictSurfacesPaths() async {
        let source = MockWorkstreamsSource()
        source.mergeResult = (false, "", false, "", ["x.go", "y.go"])
        let model = WorkstreamsModel(source: source)
        let outcome = await model.merge(workstream(), accept: false)
        XCTAssertEqual(outcome, .conflicts(["x.go", "y.go"]))
    }

    func testMergeUnauthorizedRoutes() async {
        let source = MockWorkstreamsSource()
        source.mergeError = YccError.unauthorized
        let model = WorkstreamsModel(source: source)
        let outcome = await model.merge(workstream(), accept: false)
        XCTAssertNil(outcome)
        XCTAssertTrue(model.unauthorized)
    }

    func testMergeRpcErrorSurfacesActionError() async {
        let source = MockWorkstreamsSource()
        source.mergeError = YccError.rpc(message: "merge failed")
        let model = WorkstreamsModel(source: source)
        let outcome = await model.merge(workstream(), accept: false)
        XCTAssertNil(outcome)
        XCTAssertEqual(model.actionError, "merge failed")
    }

    // MARK: - Discard

    func testDiscardRefreshesList() async {
        let source = MockWorkstreamsSource()
        source.workstreams = [workstream(id: "ws_x")]
        let model = WorkstreamsModel(source: source)
        await model.refresh()
        let countBefore = source.listCount

        let ok = await model.discard(workstream(id: "ws_x"))
        XCTAssertTrue(ok)
        XCTAssertEqual(source.lastDiscardId, "ws_x")
        XCTAssertEqual(model.workstreams.first?.status, "discarded", "applied from the confirmation")
        XCTAssertEqual(source.listCount, countBefore, "the reload is not awaited")
        await model.backgroundRefreshTask?.value
        XCTAssertGreaterThan(source.listCount, countBefore)
    }

    func testDiscardErrorSurfacesActionError() async {
        let source = MockWorkstreamsSource()
        source.discardError = YccError.rpc(message: "nope")
        let model = WorkstreamsModel(source: source)
        let ok = await model.discard(workstream())
        XCTAssertFalse(ok)
        XCTAssertEqual(model.actionError, "nope")
    }

    // MARK: - Integration retry / gate merge-all

    func testRetryCallsThroughAndRefreshes() async {
        let source = MockWorkstreamsSource()
        source.workstreams = [workstream(id: "ws_attention", status: "needs_attention")]
        let model = WorkstreamsModel(source: source)
        await model.refresh()
        let before = source.listCount

        let retried = await model.retry(source.workstreams[0])
        XCTAssertTrue(retried)
        XCTAssertEqual(source.lastRetryId, "ws_attention")
        XCTAssertNil(model.actionError)
        await model.backgroundRefreshTask?.value
        XCTAssertGreaterThan(source.listCount, before)
    }

    func testRetryAppliesReturnedRowImmediately() async {
        let source = MockWorkstreamsSource()
        source.workstreams = [workstream(id: "ws_attention", status: "needs_attention")]
        let model = WorkstreamsModel(source: source)
        await model.refresh()
        // The daemon answers with the re-queued row.
        source.workstreams = [workstream(id: "ws_attention", status: "ready", integrationState: "queued")]

        _ = await model.retry(model.workstreams[0])

        XCTAssertEqual(model.workstreams.first?.status, "ready")
        XCTAssertEqual(model.workstreams.first?.integrationState, "queued")
    }

    func testCachedRowsRenderImmediatelyAndRevalidate() async {
        let cache = AppDataCache()
        var project = Ycc_V1_ProjectInfo()
        project.name = "proj"
        cache.updateProjects([project, Ycc_V1_ProjectInfo()])
        let source = MockWorkstreamsSource()
        source.workstreams = [workstream(id: "ws_1")]
        let first = WorkstreamsModel(source: source, selectedProject: "proj", cache: cache)
        await first.refresh()
        XCTAssertEqual(source.listProjectsCount, 0, "projects come from the app-level cache")

        source.workstreams = [workstream(id: "ws_1"), workstream(id: "ws_2")]
        let revisit = WorkstreamsModel(source: source, selectedProject: "proj", cache: cache)
        XCTAssertEqual(revisit.workstreams.map(\.id), ["ws_1"], "last rows render before any RPC")
        XCTAssertEqual(revisit.projects.count, 2)
        await revisit.refresh()
        XCTAssertEqual(revisit.workstreams.map(\.id), ["ws_1", "ws_2"])

        cache.clear()
        let afterSwitch = WorkstreamsModel(source: source, selectedProject: "proj", cache: cache)
        XCTAssertTrue(afterSwitch.workstreams.isEmpty, "a connection switch forgets cached rows")
    }

    func testMergeAllReadyOnlyMergesGateEligibleRows() async {
        let source = MockWorkstreamsSource()
        source.workstreams = [
            workstream(id: "ws_gate_1", status: "ready", integrationMode: "gate"),
            workstream(id: "ws_manual", status: "ready", integrationMode: "manual"),
            workstream(id: "ws_auto_queued", status: "ready", integrationState: "queued", integrationMode: "auto"),
            workstream(id: "ws_auto_integrating", status: "ready", integrationState: "integrating", integrationMode: "auto"),
            workstream(id: "ws_active", status: "active", integrationMode: "gate"),
            workstream(id: "ws_gate_2", status: "ready", integrationMode: "gate"),
        ]
        source.mergeResult = (true, "abc", false, "", [])
        let model = WorkstreamsModel(source: source)
        await model.refresh()

        let summary = await model.mergeAllReady()

        XCTAssertEqual(summary, MergeAllReadySummary(mergedCount: 2))
        XCTAssertEqual(source.mergeCalls.map { $0.workstreamId }, ["ws_gate_1", "ws_gate_2"])
        XCTAssertTrue(source.mergeCalls.allSatisfy { $0.accept })
    }

    func testMergeAllReadyStopsAtFirstError() async {
        let source = MockWorkstreamsSource()
        source.workstreams = [
            workstream(id: "ws_1", status: "ready", integrationMode: "gate"),
            workstream(id: "ws_2", status: "ready", integrationMode: "gate"),
            workstream(id: "ws_3", status: "ready", integrationMode: "gate"),
        ]
        source.mergeResult = (true, "abc", false, "", [])
        source.mergeErrorsByID["ws_2"] = YccError.rpc(message: "base moved")
        let model = WorkstreamsModel(source: source)
        await model.refresh()

        let summary = await model.mergeAllReady()

        XCTAssertEqual(summary.mergedCount, 1)
        XCTAssertEqual(summary.firstError, "base moved")
        XCTAssertEqual(source.mergeCalls.map { $0.workstreamId }, ["ws_1", "ws_2"])
    }
}
