import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``SessionTranscriptSource`` for headless view-model tests.
private final class MockSource: SessionTranscriptSource, @unchecked Sendable {
    var transcript: [Ycc_V1_Event] = []
    var streams: [AsyncThrowingStream<Ycc_V1_Event, Error>] = []
    private(set) var recordedFromSeqs: [Int64] = []
    private var callIndex = 0
    private let lock = NSLock()

    func getSessionTranscript(project: String, sessionId: String) async throws -> [Ycc_V1_Event] {
        transcript
    }

    func subscribe(sessionId: String, fromSeq: Int64) -> AsyncThrowingStream<Ycc_V1_Event, Error> {
        lock.lock()
        defer { lock.unlock() }
        recordedFromSeqs.append(fromSeq)
        let idx = min(callIndex, streams.count - 1)
        callIndex += 1
        return streams.isEmpty ? AsyncThrowingStream { $0.finish() } : streams[idx]
    }
}

@MainActor
final class SessionViewModelTests: XCTestCase {
    private func event(_ seq: Int64, _ type: String, _ dataJson: String, actor: String = "coordinator") -> Ycc_V1_Event {
        var e = Ycc_V1_Event()
        e.seq = seq
        e.type = type
        e.dataJson = dataJson
        e.actor = actor
        return e
    }

    private func stream(_ events: [Ycc_V1_Event], thenThrow error: Error? = nil) -> AsyncThrowingStream<Ycc_V1_Event, Error> {
        AsyncThrowingStream { continuation in
            for e in events { continuation.yield(e) }
            if let error {
                continuation.finish(throwing: error)
            } else {
                continuation.finish()
            }
        }
    }

    private func waitUntil(_ condition: @escaping () -> Bool, timeout: TimeInterval = 3) async {
        let deadline = Date().addingTimeInterval(timeout)
        while !condition() && Date() < deadline {
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
    }

    private var sampleEvents: [Ycc_V1_Event] {
        [
            event(1, "user_input", #"{"text":"hi"}"#, actor: "user"),
            event(2, "model_turn", #"{"text":"one"}"#),
            event(3, "tool_call", #"{"id":"a","name":"ls","args":"{}"}"#),
            event(4, "tool_result", #"{"id":"a","name":"ls","result":"done"}"#),
            event(5, "model_turn", #"{"text":"two"}"#),
        ]
    }

    func testPersistedModeLoadsTranscriptOnce() async {
        let source = MockSource()
        source.transcript = sampleEvents
        let vm = SessionViewModel(source: source, sessionID: "s1", mode: .persisted)

        vm.start()
        await waitUntil { vm.state == .finished }

        XCTAssertEqual(vm.state, .finished)
        XCTAssertEqual(vm.projection.lastPersistedSeq, 5)
        // user bubble + model + tool + model = 4 rows.
        XCTAssertEqual(vm.rows.count, 4)
        XCTAssertTrue(source.recordedFromSeqs.isEmpty, "persisted mode holds no stream open")
    }

    func testLiveReconnectReplaysFromLastPersistedSeq() async {
        let source = MockSource()
        let all = sampleEvents
        // First stream drops after seq 3; the reconnect resumes from seq 3.
        source.streams = [
            stream(Array(all.prefix(3)), thenThrow: YccError.rpc(message: "dropped")),
            stream(Array(all.suffix(2))),
        ]
        let vm = SessionViewModel(
            source: source,
            sessionID: "s1",
            mode: .live,
            backoff: .init(initial: 1_000_000, maximum: 2_000_000)
        )

        vm.start()
        await waitUntil { vm.state == .finished }

        XCTAssertEqual(vm.state, .finished)
        XCTAssertEqual(vm.projection.lastPersistedSeq, 5)
        XCTAssertEqual(vm.rows.count, 4)
        // Fresh subscribe from 0, then reconnect from the last persisted seq (3).
        XCTAssertEqual(source.recordedFromSeqs, [0, 3])

        // Compare against a one-pass fold of the same events.
        var onePass = SessionProjection()
        onePass.apply(all)
        XCTAssertEqual(vm.projection.rows, onePass.rows)
    }

    func testReconnectClearsStaleLiveTailBeforeNewEvents() async {
        let source = MockSource()
        // Stream 1: a durable user_input then a streaming turn_delta, then a drop.
        let dropStream = AsyncThrowingStream<Ycc_V1_Event, Error> { continuation in
            continuation.yield(self.event(1, "user_input", #"{"text":"hi"}"#, actor: "user"))
            var delta = Ycc_V1_Event()
            delta.seq = 0
            delta.type = "turn_delta"
            delta.transient = true
            delta.dataJson = #"{"text":"partial answer so"}"#
            continuation.yield(delta)
            continuation.finish(throwing: YccError.rpc(message: "dropped"))
        }
        // Stream 2: stays open (never yields) so we can observe the state right
        // after reconnect subscribes but before any new event arrives.
        let openStream = AsyncThrowingStream<Ycc_V1_Event, Error> { _ in }
        source.streams = [dropStream, openStream]

        let vm = SessionViewModel(
            source: source,
            sessionID: "s1",
            mode: .live,
            backoff: .init(initial: 1_000_000, maximum: 2_000_000)
        )

        vm.start()
        // Wait until the reconnect has subscribed a second time.
        await waitUntil { source.recordedFromSeqs.count == 2 }

        // The stale tail from before the drop must be gone even though no new
        // delta/model_turn has arrived on the reconnected stream.
        XCTAssertNil(vm.projection.liveTail)
        XCTAssertEqual(vm.projection.durableRows.count, 1, "the durable user bubble survives")
        // Reconnect resumes from the last persisted seq (the user_input at 1).
        XCTAssertEqual(source.recordedFromSeqs, [0, 1])
        vm.stop()
    }

    func testStartAfterCleanFinishRestarts() async {
        let source = MockSource()
        source.streams = [
            stream([event(1, "model_turn", #"{"text":"one"}"#)]),  // clean close
            stream([event(2, "model_turn", #"{"text":"two"}"#)]),  // restart delivers more
        ]
        let vm = SessionViewModel(
            source: source,
            sessionID: "s1",
            mode: .live,
            backoff: .init(initial: 1_000_000, maximum: 2_000_000)
        )

        vm.start()
        await waitUntil { vm.state == .finished }
        XCTAssertEqual(vm.projection.lastPersistedSeq, 1)

        // A plain start() after a clean finish must not be a silent no-op.
        vm.start()
        await waitUntil { vm.projection.lastPersistedSeq == 2 }
        XCTAssertEqual(vm.projection.lastPersistedSeq, 2)
        XCTAssertEqual(source.recordedFromSeqs, [0, 1])
        vm.stop()
    }

    func testReconnectAfterCleanFinishReSubscribesFromCursor() async {
        let source = MockSource()
        let all = sampleEvents
        source.streams = [
            stream(Array(all.prefix(3))),   // clean close after seq 3
            stream(Array(all.suffix(2))),   // foreground reconnect delivers rest
        ]
        let vm = SessionViewModel(
            source: source,
            sessionID: "s1",
            mode: .live,
            backoff: .init(initial: 1_000_000, maximum: 2_000_000)
        )

        vm.start()
        await waitUntil { vm.state == .finished }
        XCTAssertEqual(vm.projection.lastPersistedSeq, 3)

        // App foregrounded → reconnect from the last persisted seq.
        vm.reconnect()
        await waitUntil { vm.projection.lastPersistedSeq == 5 }

        XCTAssertEqual(vm.projection.lastPersistedSeq, 5)
        XCTAssertEqual(source.recordedFromSeqs, [0, 3])
        vm.stop()
    }
}
