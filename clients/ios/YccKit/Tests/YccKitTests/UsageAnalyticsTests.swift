import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A manually advanced clock shared with the recorder.
private final class Clock: @unchecked Sendable {
    private let lock = NSLock()
    private var now: TimeInterval = 1000
    func read() -> TimeInterval { lock.lock(); defer { lock.unlock() }; return now }
    func advance(_ seconds: TimeInterval) { lock.lock(); now += seconds; lock.unlock() }
}

private final class Sink: @unchecked Sendable {
    private let lock = NSLock()
    private var requests: [Ycc_V1_RecordUiEventsRequest] = []
    func send(_ request: Ycc_V1_RecordUiEventsRequest) { lock.lock(); requests.append(request); lock.unlock() }
    var all: [Ycc_V1_RecordUiEventsRequest] { lock.lock(); defer { lock.unlock() }; return requests }
}

final class UsageAnalyticsTests: XCTestCase {
    private func makeRecorder() -> (UsageAnalytics, Clock, Sink) {
        let clock = Clock()
        let sink = Sink()
        let recorder = UsageAnalytics(clientVersion: "1.2 (34)", uptime: { clock.read() }, wallMS: { 42 })
        recorder.setSender { sink.send($0) }
        return (recorder, clock, sink)
    }


    func testViewSequence() async {
        let (recorder, clock, sink) = makeRecorder()
        recorder.enterForeground(attrs: ["layout": "compact"])
        clock.advance(2)
        recorder.setScreen("session")
        clock.advance(5)
        recorder.present("session_settings")
        clock.advance(1)
        recorder.dismiss("session_settings")
        clock.advance(3)
        recorder.setScreen("backlog")
        let events = recorder.pendingEvents
        XCTAssertEqual(events.map(\.kind), ["visit", "view", "view", "view", "view"])
        XCTAssertEqual(events[0].attrs, ["layout": "compact"])
        XCTAssertEqual(events.dropFirst().map(\.name), ["home", "session", "session_settings", "session"])
        XCTAssertEqual(events.dropFirst().map(\.durationMs), [2000, 5000, 1000, 3000])
        XCTAssertEqual(events.dropFirst().map { $0.attrs["from"] ?? "" }, ["", "home", "session", "session_settings"])
        XCTAssertEqual(recorder.currentView, "backlog")

        await recorder.flush()
        let sent = sink.all
        XCTAssertEqual(sent.count, 1)
        XCTAssertEqual(sent[0].client, "ios")
        XCTAssertEqual(sent[0].clientVersion, "1.2__34_")
        XCTAssertEqual(sent[0].events.count, 5)
        XCTAssertTrue(recorder.pendingEvents.isEmpty)
    }

    func testBackgroundClosesDwellAndForegroundStartsNewVisit() async {
        let (recorder, clock, sink) = makeRecorder()
        recorder.enterForeground()
        recorder.setScreen("backlog")
        clock.advance(4)
        await recorder.enterBackground()
        clock.advance(100) // time in the background is not dwell
        recorder.enterForeground()
        clock.advance(1)
        recorder.setScreen("task")
        await recorder.flush()
        let sent = sink.all
        XCTAssertEqual(sent.count, 2)
        let first = sent[0].events.filter { $0.kind == "view" }
        XCTAssertEqual(first.map(\.name), ["home", "backlog"])
        XCTAssertEqual(first.last?.durationMs, 4000)
        let second = sent[1].events
        XCTAssertEqual(second.map(\.kind), ["visit", "view"])
        XCTAssertEqual(second[1].name, "backlog")
        XCTAssertEqual(second[1].durationMs, 1000)
        XCTAssertNotEqual(sent[0].visitID, sent[1].visitID)
    }

    func testActionsErrorsAndSanitising() {
        let (recorder, _, _) = makeRecorder()
        recorder.setScreen("session")
        recorder.present("drawer")
        recorder.action("drawer.open backlog", via: .swipe)
        recorder.error("backlog.update", code: "unavailable")
        recorder.rpcFailed(procedure: "/ycc.v1.SessionService/ListBacklog", code: "deadline_exceeded")
        recorder.rpcFailed(procedure: "/ycc.v1.SessionService/ListBacklog", code: "canceled")
        recorder.rpcFailed(procedure: "/ycc.v1.SessionService/RecordUiEvents", code: "unavailable")
        let events = recorder.pendingEvents
        XCTAssertEqual(events.count, 3)
        XCTAssertEqual(events[0].name, "drawer.open_backlog")
        XCTAssertEqual(events[0].view, "drawer")
        XCTAssertEqual(events[0].via, "swipe")
        XCTAssertEqual(events[1].attrs["code"], "unavailable")
        XCTAssertEqual(events[2].name, "rpc.ListBacklog")
        XCTAssertEqual(events[2].attrs["code"], "deadline_exceeded")
        XCTAssertEqual(UsageAnalytics.token("", max: 5), "unknown")
        XCTAssertEqual(UsageAnalytics.token("abcdefgh", max: 5), "abcde")
        XCTAssertEqual(UsageAnalytics.token("⌘K", max: 5), "_K")
    }

    func testCatalogSentOncePerVisitAndBufferCappedWithoutSender() async {
        let clock = Clock()
        let sink = Sink()
        let recorder = UsageAnalytics(clientVersion: "dev", uptime: { clock.read() }, wallMS: { 1 })
        for _ in 0..<(UsageAnalytics.bufferCap + 10) { recorder.action("composer.send") }
        XCTAssertEqual(recorder.pendingEvents.count, UsageAnalytics.bufferCap)
        recorder.setCatalog(views: ["home", "session"], actions: ["composer.send"])
        await recorder.flush() // no sender: nothing happens
        XCTAssertTrue(sink.all.isEmpty)
        recorder.setSender { sink.send($0) }
        await recorder.flush()
        await recorder.flush() // nothing new to send
        recorder.action("composer.send")
        await recorder.flush()
        let sent = sink.all
        XCTAssertEqual(sent.count, 2)
        XCTAssertEqual(sent[0].catalog.map(\.name), ["home", "session", "composer.send"])
        XCTAssertEqual(sent[0].catalog.map(\.kind), ["view", "view", "action"])
        XCTAssertTrue(sent[1].catalog.isEmpty)
        XCTAssertEqual(sent[1].events.count, 1)
    }

    func testFlowsRecordOpenSubmitOrCancel() {
        let (recorder, _, _) = makeRecorder()
        recorder.beginFlow("new_session")
        XCTAssertEqual(recorder.currentView, "new_session")
        recorder.submitFlow("new_session")
        recorder.submitFlow("new_session") // once only
        recorder.endFlow("new_session")
        recorder.beginFlow("quick_capture")
        recorder.endFlow("quick_capture")
        recorder.submitFlow("quick_capture") // not open: ignored
        XCTAssertEqual(recorder.currentView, "home")
        let actions = recorder.pendingEvents.filter { $0.kind == "action" }
        XCTAssertEqual(actions.map(\.name), ["new_session.open", "new_session.submit", "quick_capture.open", "quick_capture.cancel"])
        XCTAssertEqual(actions.map(\.view), ["new_session", "new_session", "quick_capture", "quick_capture"])
        XCTAssertEqual(actions.last?.via, "auto")
    }
}
