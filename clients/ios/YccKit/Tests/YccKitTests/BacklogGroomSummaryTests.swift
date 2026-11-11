import Foundation
import XCTest
import YccProto
@testable import YccKit

final class BacklogGroomSummaryTests: XCTestCase {
    private func status(
        auto: Bool = true, running: String = "", run: ((inout Ycc_V1_BacklogGroomRun) -> Void)? = nil
    ) -> Ycc_V1_GetBacklogGroomResponse {
        var s = Ycc_V1_GetBacklogGroomResponse()
        s.autoGroom = auto
        s.groomSessionID = running
        if let run {
            var r = Ycc_V1_BacklogGroomRun()
            r.sessionID = "s_g"
            r.startedUnix = 1000
            r.changed = 12
            r.batch = ["0001", "0002", "0003"]
            run(&r)
            s.lastGroom = r
        }
        return s
    }

    /// Renders a date as its unix time so lines are locale-independent.
    private let relative: (Date) -> String = { "@\(Int($0.timeIntervalSince1970))" }

    func testNoRunYet() {
        XCTAssertEqual(
            BacklogGroomSummary.line(status(), relative: relative),
            "Not run yet")
        XCTAssertEqual(
            BacklogGroomSummary.line(status(auto: false), relative: relative),
            "Automatic grooming off")
        XCTAssertFalse(BacklogGroomSummary.failed(status()))
        XCTAssertFalse(BacklogGroomSummary.isRunning(status(running: "s_other")))
    }

    func testProgressTowardNextGroom() {
        let now = Date(timeIntervalSince1970: 5000)
        func progress(_ pending: Int32, next: Int64 = 0, auto: Bool = true) -> Ycc_V1_GetBacklogGroomResponse {
            var s = status(auto: auto) { $0.finishedUnix = 1400; $0.outcome = "finished" }
            s.pendingChanged = pending
            s.churnThreshold = 10
            s.nextEligibleUnix = next
            return s
        }
        func line(_ s: Ycc_V1_GetBacklogGroomResponse) -> String {
            BacklogGroomSummary.line(s, now: now, relative: relative)
        }
        XCTAssertEqual(line(progress(4)), "4 of 10 tasks changed")
        XCTAssertEqual(line(progress(12)), "Due · 12 changed · starts with the next session")
        XCTAssertEqual(line(progress(12, next: 4000)), "Due · 12 changed · starts with the next session")
        XCTAssertEqual(line(progress(12, next: 9000)), "Due @9000 · 12 changed")
        XCTAssertEqual(line(progress(12, auto: false)), "Automatic grooming off · 12 changed")
        XCTAssertEqual(
            BacklogGroomSummary.lastRunLine(progress(4), relative: relative),
            "Finished @1400 · reviewed 3 changed tasks")

        // A failed run outranks progress.
        var failed = progress(12)
        failed.lastGroom.outcome = "error"
        XCTAssertEqual(line(failed), "Error @1400 · 12 changed tasks still to review")
    }

    func testRunningFinishedFailedInterrupted() {
        let running = status(running: "s_g") { $0.deferred = 2 }
        XCTAssertTrue(BacklogGroomSummary.isRunning(running))
        XCTAssertFalse(BacklogGroomSummary.failed(running))
        XCTAssertEqual(
            BacklogGroomSummary.line(running, relative: relative),
            "Running · started @1000 · reviewing 3 changed tasks, 2 left for the next run")

        let finished = status { $0.finishedUnix = 1400; $0.outcome = "finished" }
        XCTAssertFalse(BacklogGroomSummary.failed(finished))
        XCTAssertEqual(
            BacklogGroomSummary.line(finished, relative: relative),
            "Finished @1400 · reviewed 3 changed tasks")

        let timedOut = status { $0.finishedUnix = 1500; $0.outcome = "timed out" }
        XCTAssertTrue(BacklogGroomSummary.failed(timedOut))
        XCTAssertEqual(
            BacklogGroomSummary.line(timedOut, relative: relative),
            "Timed out @1500 · 12 changed tasks still to review")

        // Not running and never recorded an end: the daemon restarted mid-run.
        let lost = status { $0.batch = ["0001"] }
        XCTAssertTrue(BacklogGroomSummary.failed(lost))
        XCTAssertEqual(
            BacklogGroomSummary.line(lost, relative: relative),
            "Interrupted · started @1000 · 1 changed task not reviewed")
    }
}
