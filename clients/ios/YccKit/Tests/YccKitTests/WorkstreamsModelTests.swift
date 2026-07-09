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

    var discardError: Error?

    private(set) var listCount = 0
    private(set) var lastMergeArgs: (workstreamId: String, accept: Bool)?
    private(set) var lastPreviewId: String?
    private(set) var lastDiscardId: String?

    func listWorkstreams(project: String) async throws -> [Ycc_V1_WorkstreamInfo] {
        listCount += 1
        if let listError { throw listError }
        return workstreams
    }

    func listProjects() async throws -> [Ycc_V1_ProjectInfo] { projects }

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
        if let mergeError { throw mergeError }
        return mergeResult
    }

    func discardWorkstream(workstreamId: String) async throws {
        lastDiscardId = workstreamId
        if let discardError { throw discardError }
    }
}

private func workstream(
    id: String = "ws_abcdef01", project: String = "proj", branch: String = "ycc/ws/x",
    sessionId: String = "sess-1", taskId: String = "", status: String = "active",
    commitCount: Int64 = 0, sessionStatus: String = ""
) -> Ycc_V1_WorkstreamInfo {
    var w = Ycc_V1_WorkstreamInfo()
    w.id = id
    w.project = project
    w.branch = branch
    w.sessionID = sessionId
    w.taskID = taskId
    w.status = status
    w.commitCount = commitCount
    w.sessionStatus = sessionStatus
    return w
}

@MainActor
final class WorkstreamsModelTests: XCTestCase {
    // MARK: - Status mapping

    func testStatusMapping() {
        XCTAssertEqual(WorkstreamStatus(status: "active"), .active)
        XCTAssertEqual(WorkstreamStatus(status: "MERGED"), .merged)
        XCTAssertEqual(WorkstreamStatus(status: "discarded"), .discarded)
        XCTAssertEqual(WorkstreamStatus(status: "stale"), .stale)
        XCTAssertEqual(WorkstreamStatus(status: "weird"), .unknown)
    }

    func testActionableOnlyForActiveOrStale() {
        XCTAssertTrue(WorkstreamStatus.active.isActionable)
        XCTAssertTrue(WorkstreamStatus.stale.isActionable)
        XCTAssertFalse(WorkstreamStatus.merged.isActionable)
        XCTAssertFalse(WorkstreamStatus.discarded.isActionable)
        XCTAssertFalse(WorkstreamStatus.unknown.isActionable)
    }

    func testCommitSummary() {
        XCTAssertEqual(WorkstreamsModel.commitSummary(for: workstream(commitCount: 0)), "no commits")
        XCTAssertEqual(WorkstreamsModel.commitSummary(for: workstream(commitCount: 1)), "1 commit")
        XCTAssertEqual(WorkstreamsModel.commitSummary(for: workstream(commitCount: 3)), "3 commits")
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
        // Merged refreshes the list.
        XCTAssertGreaterThanOrEqual(source.listCount, 1)
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
}
