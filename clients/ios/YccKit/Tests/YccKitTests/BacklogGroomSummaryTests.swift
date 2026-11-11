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
