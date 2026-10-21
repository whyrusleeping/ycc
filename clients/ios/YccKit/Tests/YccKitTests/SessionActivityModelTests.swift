import Foundation
import XCTest
import YccProto
@testable import YccKit

private func node(
    _ id: String, parent: String = "coordinator", kind: String = "generic",
    status: String = "done", start: String = "", end: String = "", lastActivity: String = "",
    spans: [(round: Int32, status: String, start: String, end: String)] = []
) -> Ycc_V1_ActivityNode {
    var n = Ycc_V1_ActivityNode()
    n.id = id
    n.parentID = parent
    n.kind = kind
    n.status = status
    n.startedAt = start
    n.finishedAt = end
    n.lastActivityAt = lastActivity
    n.spans = spans.map { s in
        var span = Ycc_V1_ActivitySpan()
        span.round = s.round
        span.status = s.status
        span.startedAt = s.start
        span.finishedAt = s.end
        return span
    }
    return n
}

private func output(
    _ bytes: [UInt8], start: Int64, retainedStart: Int64 = 0, retainedEnd: Int64? = nil,
    status: String = "running"
) -> Ycc_V1_GetJobOutputResponse {
    var r = Ycc_V1_GetJobOutputResponse()
    r.available = true
    r.data = Data(bytes)
    r.start = start
    r.end = start + Int64(bytes.count)
    r.retainedStart = retainedStart
    r.retainedEnd = retainedEnd ?? r.end
    r.status = status
    return r
}

private struct ScriptedFailure: Error {}

/// Serves scripted results in order (a `nil` step throws) and records the
/// output requests it saw as `(offset, tailLines)`.
private final class ScriptedSource: SessionActivitySource, @unchecked Sendable {
    private let lock = NSLock()
    private var activity: [Ycc_V1_GetSessionActivityResponse?]
    private var outputs: [Ycc_V1_GetJobOutputResponse?]
    private(set) var outputRequests: [(Int64, Int32)] = []

    init(activity: [Ycc_V1_GetSessionActivityResponse?] = [], outputs: [Ycc_V1_GetJobOutputResponse?] = []) {
        self.activity = activity
        self.outputs = outputs
    }

    func getSessionActivity(project: String, sessionId: String) async throws -> Ycc_V1_GetSessionActivityResponse {
        let next = lock.withLock { activity.isEmpty ? nil : activity.removeFirst() }
        guard let next else { throw ScriptedFailure() }
        return next
    }

    func getJobOutput(
        project: String, sessionId: String, jobId: String, offset: Int64, limit: Int32, tailLines: Int32
    ) async throws -> Ycc_V1_GetJobOutputResponse {
        let next = lock.withLock {
            outputRequests.append((offset, tailLines))
            return outputs.isEmpty ? nil : outputs.removeFirst()
        }
        guard let next else { throw ScriptedFailure() }
        return next
    }
}

final class SessionActivityModelTests: XCTestCase {
    private let graph: [Ycc_V1_ActivityNode] = [
        node("coordinator", parent: "", kind: "coordinator", status: "running", start: "2026-10-22T10:00:00Z"),
        node("agent:agent_1", status: "running", start: "2026-10-22T10:00:05Z",
             spans: [(1, "done", "2026-10-22T10:00:05Z", "2026-10-22T10:00:20Z"),
                     (2, "running", "2026-10-22T10:00:30Z", "")]),
        node("job_3", parent: "agent:agent_1", kind: "bash", status: "running", start: "2026-10-22T10:00:10Z"),
        node("job_4", parent: "agent:missing", kind: "bash", status: "failed",
             start: "2026-10-22T10:00:12Z", end: "2026-10-22T10:00:20Z"),
    ]

    func testRowsNestByParentAndReattachOrphansToRoot() {
        let rows = ActivityGraph.rows(nodes: graph, collapsed: [])
        XCTAssertEqual(rows.map(\.id), ["coordinator", "agent:agent_1", "job_3", "job_4"])
        XCTAssertEqual(rows.map(\.depth), [0, 1, 2, 1])
        XCTAssertEqual(rows.map(\.hasChildren), [true, true, false, false])
    }

    func testCollapsedNodeHidesDescendants() {
        let rows = ActivityGraph.rows(nodes: graph, collapsed: ["agent:agent_1"])
        XCTAssertEqual(rows.map(\.id), ["coordinator", "agent:agent_1", "job_4"])
        XCTAssertTrue(rows[1].hasChildren, "a collapsed node still advertises children")
    }

    func testTimelineBarsPerRoundAndRunningBarsReachNow() throws {
        let now = ActivityGraph.parseTime("2026-10-22T10:01:00Z")!
        let timeline = try XCTUnwrap(ActivityGraph.timeline(nodes: graph, now: now))
        XCTAssertEqual(timeline.origin, ActivityGraph.parseTime("2026-10-22T10:00:00Z"))
        XCTAssertEqual(timeline.duration, 60)
        let agent = timeline.lanes[1]
        XCTAssertEqual(agent.bars.count, 2)
        XCTAssertEqual(agent.bars[0].start, 5)
        XCTAssertEqual(agent.bars[0].end, 20)
        XCTAssertEqual(agent.bars[1].start, 30)
        XCTAssertEqual(agent.bars[1].end, 60, "a running round grows to now")
        let failedJob = timeline.lanes[3]
        XCTAssertEqual(failedJob.bars, [ActivityTimelineBar(start: 12, end: 20, status: .failed, label: "")])
    }

    func testTimelineOfStoppedSessionEndsAtLastEventNotNow() throws {
        let stopped: [Ycc_V1_ActivityNode] = [
            node("coordinator", parent: "", kind: "coordinator", status: "stopped",
                 start: "2026-10-22T10:00:00Z", lastActivity: "2026-10-22T10:00:35Z"),
            node("job_1", kind: "bash", status: "done", start: "2026-10-22T10:00:02Z", end: "2026-10-22T10:00:30Z"),
            node("agent:agent_1", status: "lost", start: "2026-10-22T10:00:05Z",
                 spans: [(1, "lost", "2026-10-22T10:00:05Z", "")]),
        ]
        let hourLater = ActivityGraph.parseTime("2026-10-22T11:00:00Z")!
        let timeline = try XCTUnwrap(ActivityGraph.timeline(nodes: stopped, now: hourLater))
        XCTAssertEqual(timeline.duration, 35)
        XCTAssertEqual(timeline.lanes[0].bars.first?.end, 35, "the root bar ends with the session's last event")
        XCTAssertEqual(timeline.lanes[2].bars.first?.end, 35, "lost work without a recorded end does not grow to now")
    }

    func testElapsedUsesNowOnlyWhileRunning() {
        let now = ActivityGraph.parseTime("2026-10-22T10:02:05Z")!
        XCTAssertEqual(ActivityGraph.elapsed(start: "2026-10-22T10:00:00Z", end: "", status: .running, now: now), "2m 05s")
        XCTAssertEqual(ActivityGraph.elapsed(start: "2026-10-22T10:00:00Z", end: "", status: .lost, now: now), "")
        XCTAssertEqual(ActivityGraph.elapsed(start: "2026-10-22T10:00:00.250Z", end: "2026-10-22T10:00:09.900Z", status: .done, now: now), "9s")
        XCTAssertEqual(ActivityGraph.formatDuration(3_725), "1h 02m")
    }

    func testFollowerAdvancesCursorAndJoinsSplitUTF8() {
        var follower = JobOutputFollower()
        // "héllo\n" split in the middle of é (0xC3 0xA9).
        follower.apply(output([0x68, 0xC3], start: 100))
        XCTAssertEqual(follower.text, "h")
        XCTAssertEqual(follower.cursor, 102)
        XCTAssertTrue(follower.omittedHead)
        follower.apply(output([0xA9, 0x6C, 0x6C, 0x6F, 0x0A], start: 102, status: "done"))
        XCTAssertEqual(follower.text, "héllo\n")
        XCTAssertEqual(follower.cursor, 107)
        XCTAssertFalse(follower.droppedEarlier)
        XCTAssertEqual(follower.status, "done")
    }

    func testFollowerFlagsGapWhenRetainedStartPassesCursor() {
        var follower = JobOutputFollower()
        follower.apply(output(Array("abc".utf8), start: 0))
        XCTAssertFalse(follower.omittedHead)
        follower.apply(output(Array("xyz".utf8), start: 900, retainedStart: 900))
        XCTAssertTrue(follower.droppedEarlier)
        XCTAssertEqual(follower.text, "abcxyz")
        XCTAssertEqual(follower.cursor, 903)
    }

    func testFollowerTrimsOldestHistoryAtLineOrCharacterBoundary() {
        var follower = JobOutputFollower(maxBytes: 16)
        follower.apply(output(Array("line one\nline two\nline three\n".utf8), start: 0))
        XCTAssertEqual(follower.text, "line three\n", "cuts after the newline inside the excess")
        XCTAssertTrue(follower.trimmedHistory)
        XCTAssertEqual(follower.cursor, 29, "the cursor still tracks the daemon's stream")

        var noNewline = JobOutputFollower(maxBytes: 15)
        noNewline.apply(output(Array(String(repeating: "é", count: 10).utf8), start: 0))
        XCTAssertEqual(noNewline.text, String(repeating: "é", count: 7), "an odd cut moves to the next character")
        XCTAssertLessThanOrEqual(noNewline.text.utf8.count, 15)
    }
}

@MainActor
final class SessionActivityPollingTests: XCTestCase {
    func testFollowDrainsRetainedOutputAfterJobFinished() async {
        // A finished job whose output exceeds one forward read: each response
        // ends short of retained_end until the last.
        let source = ScriptedSource(outputs: [
            output(Array("ab".utf8), start: 0, retainedEnd: 6, status: "done"),
            output(Array("cd".utf8), start: 2, retainedEnd: 6, status: "done"),
            output(Array("ef".utf8), start: 4, retainedEnd: 6, status: "done"),
        ])
        let model = JobOutputModel(source: source, project: "p", sessionID: "s", jobID: "job_1", followInterval: 60)

        await model.follow()

        XCTAssertEqual(model.text, "abcdef")
        XCTAssertFalse(model.follower.hasMore)
        XCTAssertEqual(source.outputRequests.map(\.0), [0, 2, 4])
        XCTAssertEqual(source.outputRequests.map(\.1), [JobOutputModel.tailLines, 0, 0], "only the first read is a tail")
    }

    func testFollowRetriesAfterFailureAndStillTailsFirst() async {
        let source = ScriptedSource(outputs: [
            nil,
            output(Array("ok\n".utf8), start: 0, status: "done"),
        ])
        let model = JobOutputModel(
            source: source, project: "p", sessionID: "s", jobID: "job_1", followInterval: 60, retryInterval: 0.01)

        await model.follow()

        XCTAssertEqual(model.text, "ok\n")
        XCTAssertNil(model.errorMessage)
        XCTAssertEqual(source.outputRequests.map(\.1), [JobOutputModel.tailLines, JobOutputModel.tailLines])
    }

    func testPollRetriesInitialFailureInsteadOfTreatingItAsNotLive() async {
        var response = Ycc_V1_GetSessionActivityResponse()
        response.live = false
        response.nodes = [node("coordinator", parent: "", kind: "coordinator", status: "stopped")]
        let source = ScriptedSource(activity: [nil, response])
        let model = SessionActivityModel(source: source, project: "p", sessionID: "s", retryInterval: 0.01)

        await model.poll()

        XCTAssertEqual(model.nodes.map(\.id), ["coordinator"])
        XCTAssertNil(model.errorMessage)
        XCTAssertTrue(model.hasLoaded)
    }
}
