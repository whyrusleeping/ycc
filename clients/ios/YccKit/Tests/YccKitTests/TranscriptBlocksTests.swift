import Foundation
import XCTest
import YccProto
@testable import YccKit

/// Folding long runs of agent activity into one summarized run (the iOS twin
/// of the web client's `blocks.ts`, with the same thresholds).
final class TranscriptBlocksTests: XCTestCase {
    private func tool(_ id: String, _ name: String = "Bash", status: TranscriptRow.ToolStatus = .ok,
                      ts: String = "") -> TranscriptRow {
        TranscriptRow(id: id, kind: .tool(name: name, status: status, args: "", output: ""),
                      seq: 0, actor: "coordinator", ts: ts)
    }

    private func thinking(_ id: String) -> TranscriptRow {
        TranscriptRow(id: id, kind: .thinking(text: "hm"), seq: 0, actor: "coordinator", ts: "")
    }

    private func message(_ id: String) -> TranscriptRow {
        TranscriptRow(id: id, kind: .modelMessage(text: "hi"), seq: 0, actor: "coordinator", ts: "")
    }

    private func ids(_ items: [TranscriptBlocks.Item]) -> [String] { items.map(\.id) }

    func testShortRunShowsEveryRow() {
        let rows = [message("m")] + (0..<7).map { tool("t\($0)") }
        let items = TranscriptBlocks.items(rows, expanded: [])
        XCTAssertEqual(ids(items), rows.map(\.id))
    }

    func testLongRunFoldsToHeaderAndNewestSteps() {
        let run = (0..<6).map { tool("t\($0)") } + [thinking("k0"), thinking("k1")]
        let rows = [message("before")] + run + [message("after")]
        let items = TranscriptBlocks.items(rows, expanded: [])
        XCTAssertEqual(ids(items), ["before", "activity:t0", "t5", "k0", "k1", "after"])
        guard case .runHeader(let header) = items[1] else { return XCTFail("expected a run header") }
        XCTAssertEqual(header.range, 1..<9)
        XCTAssertEqual(header.hiddenCount, 5)
        XCTAssertEqual(header.summary.steps, 8)
        XCTAssertEqual(header.summary.reasoning, 2)
        guard case .row(_, _, _, let inRun) = items[2] else { return XCTFail("expected a row") }
        XCTAssertTrue(inRun)
    }

    func testExpandedRunShowsAllRowsUnderItsHeader() {
        let rows = (0..<9).map { tool("t\($0)") }
        let items = TranscriptBlocks.items(rows, expanded: ["t0"])
        XCTAssertEqual(ids(items), ["activity:t0"] + rows.map(\.id))
    }

    func testFailedToolBreaksTheRunAndStaysVisible() {
        let rows = (0..<5).map { tool("a\($0)") } + [tool("err", status: .error)]
            + (0..<5).map { tool("b\($0)") }
        XCTAssertEqual(ids(TranscriptBlocks.items(rows, expanded: [])), rows.map(\.id))
    }

    func testRoutineNoticesJoinTheRunButOtherNoticesSplitIt() {
        let routine = TranscriptRow(id: "job", kind: .system(text: "job started"), seq: 0,
                                    actor: "coordinator", ts: "", isRoutineActivity: true)
        let rows = (0..<4).map { tool("a\($0)") } + [routine] + (0..<4).map { tool("b\($0)") }
        let items = TranscriptBlocks.items(rows, expanded: [])
        XCTAssertEqual(items.first?.id, "activity:a0")
        guard case .runHeader(let header) = items[0] else { return XCTFail("expected a run header") }
        XCTAssertEqual(header.summary.steps, 8, "notices are not steps")

        var plain = rows
        plain[4] = TranscriptRow(id: "job", kind: .system(text: "Session paused"), seq: 0,
                                 actor: "coordinator", ts: "")
        XCTAssertEqual(ids(TranscriptBlocks.items(plain, expanded: [])), plain.map(\.id))
    }

    func testSummaryRanksToolsAndMeasuresDuration() {
        let rows = [
            tool("1", "Read", ts: "2026-10-01T10:00:00Z"), tool("2", "Bash"), tool("3", "Edit"),
            tool("4", "Bash"), tool("5", "Edit", status: .running, ts: "2026-10-01T10:04:12.5Z"),
        ]
        let summary = ActivitySummary(rows)
        XCTAssertEqual(summary.tools.map(\.name), ["Bash", "Edit", "Read"])
        XCTAssertEqual(summary.tools.map(\.count), [2, 2, 1])
        XCTAssertEqual(ActivitySummary.compactDuration(summary.duration), "4m 12s")
        XCTAssertNil(ActivitySummary.compactDuration(0.4))
        XCTAssertEqual(ActivitySummary.compactDuration(45), "45s")
        XCTAssertEqual(ActivitySummary.compactDuration(3_780), "1h 03m")
    }

    func testSubagentHeadingRepeatsAfterAFoldedStretch() {
        let sub = (0..<9).map {
            TranscriptRow(id: "s\($0)", kind: .tool(name: "Read", status: .ok, args: "", output: ""),
                          seq: 0, actor: "impl-1", ts: "", actorEmoji: "🌻")
        }
        let headings = TranscriptBlocks.items(sub, expanded: []).compactMap { item -> Bool? in
            if case .row(_, _, let heading, _) = item { return heading }
            return nil
        }
        XCTAssertEqual(headings, [true, false, false])
    }

    func testDisplayedIDMapsAHiddenRowToItsHeader() {
        let rows = [message("m")] + (0..<9).map { tool("t\($0)") }
        let items = TranscriptBlocks.items(rows, expanded: [])
        XCTAssertEqual(TranscriptBlocks.displayedID(of: "m", in: items, rows: rows), "m")
        XCTAssertEqual(TranscriptBlocks.displayedID(of: "t2", in: items, rows: rows), "activity:t0")
        XCTAssertEqual(TranscriptBlocks.displayedID(of: "t8", in: items, rows: rows), "t8")
        XCTAssertNil(TranscriptBlocks.displayedID(of: "nope", in: items, rows: rows))
    }

    func testSliceIndicesAreHonoured() {
        let all = [message("old")] + (0..<9).map { tool("t\($0)") }
        let items = TranscriptBlocks.items(all.dropFirst(), expanded: [])
        guard case .runHeader(let header) = items[0] else { return XCTFail("expected a run header") }
        XCTAssertEqual(header.range, 1..<10)
        XCTAssertEqual(ids(items), ["activity:t0", "t6", "t7", "t8"])
    }

    func testProjectionMarksRoutineLifecycleNotices() {
        func event(_ seq: Int64, _ type: String, _ json: String) -> Ycc_V1_Event {
            var e = Ycc_V1_Event()
            e.seq = seq
            e.type = type
            e.actor = "coordinator"
            e.dataJson = json
            return e
        }
        var projection = SessionProjection()
        projection.apply([
            event(1, "job_started", #"{"job_id":"job_1","command":"make"}"#),
            event(2, "job_finished", #"{"job_id":"job_1","status":"done"}"#),
            event(3, "job_finished", #"{"job_id":"job_2","status":"failed"}"#),
            event(4, "session_paused", "{}"),
        ])
        let flags = Dictionary(uniqueKeysWithValues: projection.durableRows.map { ($0.seq, $0.isRoutineActivity) })
        XCTAssertEqual(flags[1], true)
        XCTAssertEqual(flags[2], true)
        XCTAssertEqual(flags[3], false)
        XCTAssertNotEqual(flags[4], true)
    }
}
