import Foundation
import XCTest
import YccProto
@testable import YccKit

private func report(_ kind: String, _ status: String = "", at: String = "") -> Ycc_V1_WatchReport {
    var r = Ycc_V1_WatchReport()
    r.kind = kind
    r.status = status
    r.at = at
    return r
}

private func watch(
    _ name: String, last: Ycc_V1_WatchReport? = nil, paused: Bool = false, pausedReason: String = "",
    running: Bool = false, parked: [String] = [], nextDue: String = ""
) -> Ycc_V1_Watch {
    var w = Ycc_V1_Watch()
    w.definition.name = name
    w.definition.intervalSecs = 1_800
    if let last { w.last = last }
    w.paused = paused
    w.pausedReason = pausedReason
    w.running = running
    w.parkedSessionIds = parked
    w.nextDue = nextDue
    return w
}

private final class MockWatchesSource: WatchesSource, @unchecked Sendable {
    private let lock = NSLock()
    var watches: [Ycc_V1_Watch] = []
    var reportsByLimit: [Int32: [Ycc_V1_WatchReport]] = [:]
    var listGate: AsyncGate?
    var error: Error?
    private(set) var reportLimits: [Int32] = []
    private(set) var ran: [String] = []

    func listWatches(project: String) async throws -> [Ycc_V1_Watch] {
        // The snapshot is taken before the gate: a poll that read old state.
        let (snapshot, gate) = lock.withLock { (watches, listGate) }
        await gate?.wait()
        if let error { throw error }
        return snapshot
    }

    func getWatch(project: String, name: String, reportLimit: Int32) async throws -> Ycc_V1_GetWatchResponse {
        if let error { throw error }
        return try lock.withLock {
            reportLimits.append(reportLimit)
            guard let found = watches.first(where: { $0.definition.name == name }) else {
                throw YccError.notFound(message: "unknown watch")
            }
            var response = Ycc_V1_GetWatchResponse()
            response.watch = found
            response.reports = reportsByLimit[reportLimit] ?? []
            return response
        }
    }

    func setWatchPaused(project: String, name: String, paused: Bool) async throws -> Ycc_V1_Watch {
        if let error { throw error }
        return try lock.withLock {
            guard let index = watches.firstIndex(where: { $0.definition.name == name }) else {
                throw YccError.notFound(message: "unknown watch")
            }
            watches[index].paused = paused
            watches[index].pausedReason = paused ? "user" : ""
            return watches[index]
        }
    }

    func runWatchNow(project: String, name: String) async throws {
        if let error { throw error }
        lock.withLock {
            ran.append(name)
            if let index = watches.firstIndex(where: { $0.definition.name == name }) {
                watches[index].running = true
            }
        }
    }

    func deleteWatch(project: String, name: String) async throws {
        if let error { throw error }
        lock.withLock { watches.removeAll { $0.definition.name == name } }
    }
}

@MainActor
final class WatchesModelTests: XCTestCase {
    func testReportLabelsAndTones() {
        XCTAssertEqual(WatchFormat.reportLabel(nil), "no runs yet")
        XCTAssertEqual(WatchFormat.reportTone(nil), .muted)
        XCTAssertEqual(WatchFormat.reportLabel(report("run", "alert")), "alert")
        XCTAssertEqual(WatchFormat.reportTone(report("probe", "ok")), .ok)
        // A run that never reported counts as warn.
        XCTAssertEqual(WatchFormat.reportLabel(report("failed", "warn")), "failed")
        XCTAssertEqual(WatchFormat.reportTone(report("failed")), .warn)
        XCTAssertEqual(WatchFormat.reportLabel(report("skipped")), "skipped")
        XCTAssertEqual(WatchFormat.reportTone(report("skipped")), .muted)
        XCTAssertEqual(WatchFormat.kindNote("probe"), "probe only")
        XCTAssertEqual(WatchFormat.kindNote("failed"), "")
    }

    func testIntervalAndUntil() {
        XCTAssertEqual(WatchFormat.interval(1_800), "30m")
        XCTAssertEqual(WatchFormat.interval(5_400), "1h 30m")
        XCTAssertEqual(WatchFormat.interval(108_000), "1d 6h")
        XCTAssertEqual(WatchFormat.interval(0), "—")
        let now = Date(timeIntervalSince1970: 1_000_000)
        XCTAssertEqual(WatchFormat.until(now.addingTimeInterval(20), now: now), "due now")
        XCTAssertEqual(WatchFormat.until(now.addingTimeInterval(300), now: now), "in 5m")
        XCTAssertEqual(WatchFormat.until(now.addingTimeInterval(3 * 3_600), now: now), "in 3h")
        XCTAssertEqual(WatchFormat.until(now.addingTimeInterval(3 * 86_400), now: now), "in 3d")
    }

    func testScheduleStates() {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let due = ISO8601DateFormatter().string(from: now.addingTimeInterval(600))
        XCTAssertEqual(WatchFormat.schedule(watch("a", nextDue: due), now: now).label, "next in 10m")
        XCTAssertEqual(WatchFormat.schedule(watch("a", running: true, nextDue: due), now: now),
                       WatchFormat.Schedule(label: "running", running: true))
        XCTAssertEqual(WatchFormat.schedule(watch("a", paused: true, pausedReason: "user"), now: now).label, "paused")
        let expired = watch("a", paused: true, pausedReason: "expired")
        XCTAssertEqual(WatchFormat.schedule(expired, now: now).label, "expired")
        XCTAssertTrue(WatchFormat.isExpired(expired))
        XCTAssertEqual(WatchFormat.parkedLabel(watch("a", parked: ["s1", "s2"])), "2 questions waiting")
        XCTAssertEqual(WatchFormat.parkedLabel(watch("a")), "")
    }

    func testSortPutsQuestionsThenSeverityThenPausedLast() {
        let list = [
            watch("paused", last: report("run", "alert"), paused: true),
            watch("ok", last: report("run", "ok")),
            watch("never"),
            watch("asking", last: report("run", "ok"), parked: ["s1"]),
            watch("bwarn", last: report("failed")),
            watch("alert", last: report("run", "alert")),
            watch("awarn", last: report("run", "warn")),
        ]
        XCTAssertEqual(WatchFormat.sorted(list).map(\.definition.name),
                       ["asking", "alert", "awarn", "bwarn", "ok", "never", "paused"])
        XCTAssertEqual(WatchFormat.pollInterval(list), 30)
        XCTAssertEqual(WatchFormat.pollInterval([watch("r", running: true)]), 5)
    }

    func testListActionsUpdateRowsInPlace() async {
        let source = MockWatchesSource()
        source.watches = [watch("disk"), watch("lag")]
        let model = WatchesModel(project: "p", source: source)
        await model.refresh()
        XCTAssertTrue(model.hasLoaded)
        XCTAssertEqual(model.watches.map(\.definition.name), ["disk", "lag"])

        await model.setPaused("disk", paused: true)
        XCTAssertEqual(model.watches.map(\.definition.name), ["lag", "disk"])
        XCTAssertTrue(model.watches[1].paused)

        await model.runNow("lag")
        XCTAssertEqual(source.ran, ["lag"])
        XCTAssertTrue(model.watches.first { $0.definition.name == "lag" }!.running)
        XCTAssertEqual(model.pollInterval, 5)

        let deleted = await model.delete("disk")
        XCTAssertTrue(deleted)
        XCTAssertEqual(model.watches.map(\.definition.name), ["lag"])
        XCTAssertTrue(model.busyNames.isEmpty)
    }

    /// A poll issued before an action completed must not undo what the action
    /// returned when it lands afterwards.
    func testStalePollDoesNotOverwriteActionResult() async {
        let source = MockWatchesSource()
        source.watches = [watch("disk")]
        let model = WatchesModel(project: "p", source: source)
        await model.refresh()

        let gate = AsyncGate()
        source.listGate = gate
        let poll = Task { await model.refresh() }
        await eventually { gate.waiters == 1 }
        // The poll read its snapshot (unpaused) before the pause landed.
        await model.setPaused("disk", paused: true)
        gate.open()
        await poll.value

        XCTAssertTrue(model.watches[0].paused)
    }

    func testActionFailureSurfacesAndUnauthorizedRoutes() async {
        let source = MockWatchesSource()
        source.watches = [watch("disk")]
        let model = WatchesModel(project: "p", source: source)
        await model.refresh()

        source.error = YccError.failedPrecondition(message: "a run is already active")
        await model.runNow("disk")
        XCTAssertEqual(model.actionError, "a run is already active")
        XCTAssertFalse(model.unauthorized)

        source.error = YccError.unauthorized
        await model.refresh()
        XCTAssertTrue(model.unauthorized)
        XCTAssertEqual(model.watches.count, 1, "keeps the last rows")
    }

    func testDetailLoadsOlderReportsAndNotFound() async {
        let source = MockWatchesSource()
        source.watches = [watch("disk")]
        let page = Int(WatchDetailModel.reportPage)
        source.reportsByLimit[WatchDetailModel.reportPage] = Array(repeating: report("run", "ok"), count: page)
        source.reportsByLimit[2 * WatchDetailModel.reportPage] = Array(repeating: report("run", "ok"), count: page + 3)
        let model = WatchDetailModel(project: "p", name: "disk", source: source)
        await model.refresh()
        XCTAssertEqual(model.watch?.definition.name, "disk")
        XCTAssertEqual(model.reports.count, page)
        XCTAssertTrue(model.mayHaveOlderReports)

        await model.loadOlder()
        XCTAssertEqual(model.reports.count, page + 3)
        XCTAssertFalse(model.mayHaveOlderReports)
        XCTAssertEqual(source.reportLimits, [20, 40])

        await model.setPaused(true)
        XCTAssertEqual(model.watch?.paused, true)

        let deleted = await model.delete()
        XCTAssertTrue(deleted)
        await model.refresh()
        XCTAssertTrue(model.notFound)
    }
}
