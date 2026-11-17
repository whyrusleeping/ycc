import Foundation
import XCTest
import YccProto
@testable import YccKit

private func commit(_ sha: String, files: Int32 = 0, add: Int64 = 0, del: Int64 = 0, parents: Int = 1) -> Ycc_V1_CommitSummary {
    var c = Ycc_V1_CommitSummary()
    c.sha = sha
    c.subject = "commit \(sha)"
    c.filesChanged = files
    c.additions = add
    c.deletions = del
    c.parents = (0..<parents).map { "p\($0)" }
    return c
}

private func branch(_ name: String, current: Bool = false, workstream: String = "", task: String = "") -> Ycc_V1_BranchRef {
    var b = Ycc_V1_BranchRef()
    b.name = name
    b.current = current
    b.workstreamID = workstream
    b.taskID = task
    return b
}

private func page(_ shas: [String], next: String = "", branches: [Ycc_V1_BranchRef] = []) -> Ycc_V1_ListCommitsResponse {
    var p = Ycc_V1_ListCommitsResponse()
    p.commits = shas.map { commit($0) }
    p.nextCursor = next
    p.branches = branches
    return p
}

private final class MockHistorySource: CommitHistorySource, @unchecked Sendable {
    struct Key: Hashable {
        let branch: String
        let cursor: String
    }

    private let lock = NSLock()
    var pages: [Key: Ycc_V1_ListCommitsResponse] = [:]
    var gates: [Key: AsyncGate] = [:]
    var errors: [Key: Error] = [:]
    private(set) var calls: [Key] = []

    func set(_ branch: String, _ response: Ycc_V1_ListCommitsResponse, cursor: String = "") {
        lock.withLock { pages[Key(branch: branch, cursor: cursor)] = response }
    }

    func listCommits(project: String, branch: String, limit: Int32, cursor: String) async throws -> Ycc_V1_ListCommitsResponse {
        let key = Key(branch: branch, cursor: cursor)
        let (gate, error, response) = lock.withLock { () -> (AsyncGate?, Error?, Ycc_V1_ListCommitsResponse?) in
            calls.append(key)
            return (gates[key], errors[key], pages[key])
        }
        await gate?.wait()
        if let error { throw error }
        return response ?? Ycc_V1_ListCommitsResponse()
    }
}

private let mainBranches = [
    branch("main", current: true),
    branch("ycc/ws/abc-0559", workstream: "abc", task: "0559"),
    branch("old"),
]

@MainActor
final class CommitHistoryModelTests: XCTestCase {
    func testFormatting() {
        XCTAssertEqual(CommitFormat.shortSha("0123456789abcdef"), "0123456")
        XCTAssertNil(CommitFormat.stats(commit("a")))
        XCTAssertEqual(
            CommitFormat.stats(commit("a", files: 1, add: 12, del: 3)),
            CommitFormat.Stats(additions: "+12", deletions: "\u{2212}3", files: "1 file"))
        XCTAssertEqual(CommitFormat.stats(commit("a", files: 4, add: 0, del: 9))?.files, "4 files")
        XCTAssertTrue(CommitFormat.isMerge(commit("m", parents: 2)))
        XCTAssertFalse(CommitFormat.isMerge(commit("m")))

        var dated = commit("d")
        dated.committedAt = "2026-11-17T10:00:00Z"
        XCTAssertEqual(CommitFormat.date(dated), Date(timeIntervalSince1970: 1_794_909_600))
        dated.authoredAt = "2026-11-17T09:00:00Z"
        XCTAssertEqual(CommitFormat.date(dated), Date(timeIntervalSince1970: 1_794_906_000))
    }

    func testBranchGroupsAndNotes() {
        let refs = [
            branch("feature"),
            branch("ycc/ws/abc-0559", workstream: "abc", task: "0559"),
            branch("ycc/ws/def", current: true, workstream: "def"),
            branch("main"),
        ]
        let groups = CommitFormat.groupBranches(refs)
        XCTAssertEqual(groups.local.map(\.name), ["ycc/ws/def", "feature", "main"])
        XCTAssertEqual(groups.workstreams.map(\.name), ["ycc/ws/abc-0559"])
        XCTAssertEqual(CommitFormat.branchNote(refs[0]), "")
        XCTAssertEqual(CommitFormat.branchNote(refs[1]), "workstream · 0559")
        XCTAssertEqual(CommitFormat.branchNote(refs[2]), "current · workstream")
    }

    func testPagesUntilTheLastCursorAndDedupes() async {
        let source = MockHistorySource()
        source.set("", page(["c3", "c2"], next: "k1", branches: mainBranches))
        source.set("", page(["c2", "c1"], next: "k2"), cursor: "k1")
        source.set("", page(["c0"]), cursor: "k2")
        let model = CommitHistoryModel(project: "p", source: source)

        await model.refresh()
        XCTAssertTrue(model.hasLoaded)
        XCTAssertEqual(model.commits.map(\.sha), ["c3", "c2"])
        XCTAssertEqual(model.selectedBranch, "main")
        XCTAssertTrue(model.hasMore)

        // Only the last row triggers the next page.
        await model.loadMoreIfNeeded(after: model.commits[0])
        XCTAssertEqual(source.calls.count, 1)
        await model.loadMoreIfNeeded(after: model.commits[1])
        XCTAssertEqual(model.commits.map(\.sha), ["c3", "c2", "c1"])
        await model.loadMoreIfNeeded(after: model.commits[2])
        XCTAssertEqual(model.commits.map(\.sha), ["c3", "c2", "c1", "c0"])
        XCTAssertFalse(model.hasMore)
        await model.loadMore()
        XCTAssertEqual(source.calls.map(\.cursor), ["", "k1", "k2"])

        // A refresh starts over from the first page.
        await model.refresh()
        XCTAssertEqual(model.commits.map(\.sha), ["c3", "c2"])
        XCTAssertTrue(model.hasMore)
    }

    func testFailedPageStopsScrollRetriesUntilRetried() async {
        let source = MockHistorySource()
        source.set("", page(["c2"], next: "k1", branches: mainBranches))
        source.errors[.init(branch: "", cursor: "k1")] = YccError.rpc(message: "boom")
        let model = CommitHistoryModel(project: "p", source: source)
        await model.refresh()

        await model.loadMoreIfNeeded(after: model.commits[0])
        XCTAssertEqual(model.loadMoreError, "boom")
        await model.loadMoreIfNeeded(after: model.commits[0])
        XCTAssertEqual(source.calls.count, 2)

        source.errors = [:]
        source.set("", page(["c1"]), cursor: "k1")
        await model.loadMore()
        XCTAssertNil(model.loadMoreError)
        XCTAssertEqual(model.commits.map(\.sha), ["c2", "c1"])
    }

    func testSelectBranchRequestsCurrentAsHeadAndResets() async {
        let source = MockHistorySource()
        source.set("", page(["c2"], next: "k1", branches: mainBranches))
        source.set("ycc/ws/abc-0559", page(["w1"], branches: mainBranches))
        let model = CommitHistoryModel(project: "p", source: source)
        await model.refresh()

        await model.selectBranch("ycc/ws/abc-0559")
        XCTAssertEqual(model.branch, "ycc/ws/abc-0559")
        XCTAssertEqual(model.selectedRef?.taskID, "0559")
        XCTAssertEqual(model.commits.map(\.sha), ["w1"])
        XCTAssertFalse(model.hasMore)

        // The checked-out branch goes back to HEAD rather than pinning its name.
        await model.selectBranch("main")
        XCTAssertEqual(model.branch, "")
        XCTAssertEqual(model.selectedBranch, "main")
        XCTAssertEqual(model.commits.map(\.sha), ["c2"])
        XCTAssertEqual(source.calls.map(\.branch), ["", "ycc/ws/abc-0559", ""])

        // Re-selecting the shown branch does nothing.
        await model.selectBranch("main")
        XCTAssertEqual(source.calls.count, 3)
    }

    func testStaleResponsesAfterABranchSwitchAreDropped() async {
        let source = MockHistorySource()
        source.set("", page(["c2"], next: "k1", branches: mainBranches))
        source.set("", page(["c1"]), cursor: "k1")
        source.set("old", page(["o1"], branches: mainBranches))
        let model = CommitHistoryModel(project: "p", source: source)
        await model.refresh()

        // An older page in flight when the branch changes never lands.
        let gate = AsyncGate()
        source.gates[.init(branch: "", cursor: "k1")] = gate
        let more = Task { await model.loadMoreIfNeeded(after: model.commits[0]) }
        await eventually { gate.waiters == 1 }
        XCTAssertTrue(model.isLoadingMore)
        await model.selectBranch("old")
        XCTAssertFalse(model.isLoadingMore)
        gate.open()
        await more.value
        XCTAssertEqual(model.commits.map(\.sha), ["o1"])
        XCTAssertFalse(model.isLoadingMore)

        // A slow first page for a branch the user already left is dropped too.
        let slow = AsyncGate()
        source.gates[.init(branch: "", cursor: "")] = slow
        let back = Task { await model.selectBranch("main") }
        await eventually { slow.waiters == 1 }
        XCTAssertFalse(model.hasLoaded)
        let forward = Task { await model.selectBranch("old") }
        await forward.value
        slow.open()
        await back.value
        XCTAssertEqual(model.branch, "old")
        XCTAssertEqual(model.commits.map(\.sha), ["o1"])
        XCTAssertFalse(model.isLoading)
    }

    func testNotGitAndErrors() async {
        let source = MockHistorySource()
        source.errors[.init(branch: "", cursor: "")] = YccError.failedPrecondition(message: "project is not a git repository")
        let model = CommitHistoryModel(project: "p", source: source)
        await model.refresh()
        XCTAssertTrue(model.notGit)
        XCTAssertFalse(model.hasLoaded)

        source.errors[.init(branch: "", cursor: "")] = YccError.unauthorized
        await model.refresh()
        XCTAssertTrue(model.unauthorized)

        // A failed refresh keeps the rows already shown.
        source.errors = [:]
        source.set("", page(["c1"], branches: mainBranches))
        await model.refresh()
        XCTAssertFalse(model.notGit)
        XCTAssertNil(model.errorMessage)
        source.errors[.init(branch: "", cursor: "")] = YccError.rpc(message: "offline")
        await model.refresh()
        XCTAssertEqual(model.errorMessage, "offline")
        XCTAssertEqual(model.commits.map(\.sha), ["c1"])
        XCTAssertTrue(model.hasLoaded)
    }
}
