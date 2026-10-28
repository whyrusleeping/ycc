import Foundation
import XCTest
import YccProto
@testable import YccKit

final class ActivityStepsTests: XCTestCase {
    private func step(
        _ kind: String, _ name: String, status: String = "ok",
        start: String, end: String = "", ms: Int64 = 0
    ) -> Ycc_V1_ActivityStep {
        var s = Ycc_V1_ActivityStep()
        s.kind = kind
        s.name = name
        s.status = status
        s.startedAt = start
        s.finishedAt = end
        s.durationMs = ms
        return s
    }

    private let t0 = ActivityGraph.parseTime("2026-10-22T10:00:00Z")!

    func testRunningRoundInfersTheModelRequestAfterTheLastStep() {
        let steps = [
            step("model", "opus", start: "2026-10-22T10:00:00Z", end: "2026-10-22T10:00:04Z", ms: 4000),
            step("tool", "Bash", start: "2026-10-22T10:00:04Z", end: "2026-10-22T10:00:34Z", ms: 30000),
        ]
        let now = t0.addingTimeInterval(40)
        let items = ActivitySteps.items(steps: steps, rangeStart: t0, running: true, model: "opus", now: now)
        XCTAssertEqual(items.count, 3)
        XCTAssertTrue(items[2].isInferred)
        XCTAssertEqual(items[2].duration, 6, accuracy: 0.001, "the model has been working since the tool finished")

        let breakdown = ActivitySteps.breakdown(items: items, totals: [], complete: true, wall: 40)
        XCTAssertEqual(breakdown.model, 10, accuracy: 0.001)
        XCTAssertEqual(breakdown.modelCount, 2)
        XCTAssertEqual(breakdown.tools, 30, accuracy: 0.001)
        XCTAssertEqual(breakdown.other, 0, accuracy: 0.001)
        XCTAssertEqual(breakdown.totals.first?.name, "Bash", "longest first")

        // An open tool call ticks live and suppresses the inferred model step.
        let open = [step("tool", "wait", status: "running", start: "2026-10-22T10:00:10Z")]
        let running = ActivitySteps.items(steps: open, rangeStart: t0, running: true, model: "opus", now: now)
        XCTAssertEqual(running.map(\.isInferred), [false])
        XCTAssertEqual(running[0].duration, 30, accuracy: 0.001)

        // A finished round infers nothing.
        XCTAssertEqual(ActivitySteps.items(steps: steps, rangeStart: t0, running: false, model: "", now: now).count, 2)
    }

    func testTruncatedListTakesTotalsFromTheDaemon() {
        let steps = [step("tool", "Read", start: "2026-10-22T10:00:00Z", end: "2026-10-22T10:00:01Z", ms: 1000)]
        var bash = Ycc_V1_ActivityStepTotal()
        bash.kind = "tool"
        bash.name = "Bash"
        bash.count = 3
        bash.durationMs = 90_000
        var read = Ycc_V1_ActivityStepTotal()
        read.kind = "tool"
        read.name = "Read"
        read.count = 5
        read.durationMs = 5_000
        let items = ActivitySteps.items(steps: steps, rangeStart: t0, running: false, model: "", now: t0)
        let breakdown = ActivitySteps.breakdown(items: items, totals: [read, bash], complete: false, wall: 120)
        XCTAssertEqual(breakdown.toolCount, 8)
        XCTAssertEqual(breakdown.tools, 95, accuracy: 0.001)
        XCTAssertEqual(breakdown.other, 25, accuracy: 0.001)
        XCTAssertEqual(breakdown.totals.map(\.name), ["Bash", "Read"])
    }
}
