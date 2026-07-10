import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``SessionTranscriptSource`` for headless view-model tests.
private final class MockSource: SessionTranscriptSource, @unchecked Sendable {
    var transcript: [Ycc_V1_Event] = []
    var transcriptError: Error?
    var streams: [AsyncThrowingStream<Ycc_V1_Event, Error>] = []
    private(set) var recordedFromSeqs: [Int64] = []
    private var callIndex = 0
    private let lock = NSLock()

    func getSessionTranscript(project: String, sessionId: String) async throws -> [Ycc_V1_Event] {
        if let transcriptError { throw transcriptError }
        return transcript
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

/// A fake action source recording invocations, with an injectable error to
/// exercise the toast/`failed_precondition` paths. Also conforms to
/// ``SessionTranscriptSource`` (holding no stream open) so it can back a
/// view-model on its own.
private final class MockActionSource: SessionActionSource, SessionTranscriptSource, @unchecked Sendable {
    struct Call: Equatable {
        var kind: String
        var text: String = ""
        var optionIndex: Int = -1
        var batch: [String] = []  // "idx:<n>" or "text:<s>" per answer
    }

    private let lock = NSLock()
    private var _calls: [Call] = []
    var calls: [Call] { lock.lock(); defer { lock.unlock() }; return _calls }

    /// If set, the next matching action throws this error.
    var nextError: Error?

    private func record(_ call: Call) throws {
        lock.lock()
        _calls.append(call)
        let err = nextError
        nextError = nil
        lock.unlock()
        if let err { throw err }
    }

    func sendInput(sessionId: String, text: String) async throws {
        try record(Call(kind: "send", text: text))
    }
    func answerQuestion(sessionId: String, text: String, optionIndex: Int) async throws {
        try record(Call(kind: "answer", text: text, optionIndex: optionIndex))
    }
    func answerQuestions(sessionId: String, answers: [(text: String, optionIndex: Int)]) async throws {
        let batch = answers.map { $0.optionIndex >= 0 ? "idx:\($0.optionIndex)" : "text:\($0.text)" }
        try record(Call(kind: "answerBatch", batch: batch))
    }
    func interrupt(sessionId: String) async throws { try record(Call(kind: "interrupt")) }
    func resume(sessionId: String) async throws { try record(Call(kind: "resume")) }
    func stopSession(sessionId: String) async throws { try record(Call(kind: "stop")) }

    // SessionTranscriptSource — no stream held open.
    func getSessionTranscript(project: String, sessionId: String) async throws -> [Ycc_V1_Event] { [] }
    func subscribe(sessionId: String, fromSeq: Int64) -> AsyncThrowingStream<Ycc_V1_Event, Error> {
        AsyncThrowingStream { $0.finish() }
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

    /// Reopening a live session whose transcript already contains an answered
    /// `ask_user` must never expose an intermediate pending-question state (the
    /// bug: the answer sheet flashed open, then dismissed, during per-event
    /// replay). The catch-up is a one-shot transcript fetch folded atomically,
    /// and the stream subscribes from the caught-up seq — so replay never comes
    /// through the stream at all.
    func testLiveStartCatchesUpAtomicallyViaTranscript() async {
        let source = MockSource()
        source.transcript = [
            event(1, "user_input", #"{"text":"hi"}"#, actor: "user"),
            event(2, "question_asked", #"{"prompt":"Proceed?","options":["yes","no"]}"#),
            event(3, "question_answered", #"{"answer":"yes"}"#),
            event(4, "model_turn", #"{"text":"done"}"#),
        ]
        // The live stream stays open and yields nothing new.
        source.streams = [AsyncThrowingStream { _ in }]
        let vm = SessionViewModel(
            source: source,
            sessionID: "s1",
            mode: .live,
            backoff: .init(initial: 1_000_000, maximum: 2_000_000)
        )

        vm.start()
        await waitUntil { source.recordedFromSeqs.count == 1 }

        // Caught up: the answered question is folded, nothing pending.
        XCTAssertNil(vm.pendingQuestion)
        XCTAssertEqual(vm.projection.lastPersistedSeq, 4)
        // The subscribe starts AFTER the transcript — replay never streams, so
        // no per-event intermediate state was ever observable.
        XCTAssertEqual(source.recordedFromSeqs, [4])
        vm.stop()
    }

    func testLiveStartFallsBackToStreamReplayWhenTranscriptFails() async {
        let source = MockSource()
        source.transcriptError = YccError.rpc(message: "boom")
        source.streams = [stream(sampleEvents)]
        let vm = SessionViewModel(
            source: source,
            sessionID: "s1",
            mode: .live,
            backoff: .init(initial: 1_000_000, maximum: 2_000_000)
        )

        vm.start()
        await waitUntil { vm.state == .finished }

        // Full replay still arrives via the stream from seq 0 — never a gap.
        XCTAssertEqual(vm.projection.lastPersistedSeq, 5)
        XCTAssertEqual(vm.rows.count, 4)
        XCTAssertEqual(source.recordedFromSeqs, [0])
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

    // MARK: - Interactive actions (task 0183)

    private func actionVM(_ actions: MockActionSource) -> SessionViewModel {
        // Pass the same object as both source and actions; it holds no stream.
        SessionViewModel(source: actions, actions: actions, sessionID: "s1", mode: .live)
    }

    func testSendPassesThroughTrimmed() async {
        let actions = MockActionSource()
        let vm = actionVM(actions)
        await vm.send(text: "  hello  ")
        XCTAssertEqual(actions.calls, [.init(kind: "send", text: "hello")])
        XCTAssertNil(vm.actionError)
    }

    func testSendIgnoresEmpty() async {
        let actions = MockActionSource()
        let vm = actionVM(actions)
        await vm.send(text: "   ")
        XCTAssertTrue(actions.calls.isEmpty)
    }

    func testAnswerSingleViaOptionAndViaText() async {
        let actions = MockActionSource()
        let vm = actionVM(actions)
        await vm.answer(optionIndex: 2)
        await vm.answer(text: "free text")
        XCTAssertEqual(actions.calls, [
            .init(kind: "answer", text: "", optionIndex: 2),
            .init(kind: "answer", text: "free text", optionIndex: -1),
        ])
    }

    func testAnswerBatchPositionalMixedOptionAndText() async {
        let actions = MockActionSource()
        let vm = actionVM(actions)
        await vm.answerBatch([(text: "", optionIndex: 1), (text: "later", optionIndex: -1)])
        XCTAssertEqual(actions.calls, [
            .init(kind: "answerBatch", batch: ["idx:1", "text:later"]),
        ])
    }

    func testFailedPreconditionOnAnswerSetsToastWithoutCrashing() async {
        let actions = MockActionSource()
        let vm = actionVM(actions)
        actions.nextError = YccError.failedPrecondition(message: "no pending question")
        await vm.answer(optionIndex: 0)
        XCTAssertEqual(vm.actionError, "no pending question")
        // State is not corrupted: a subsequent successful action still works.
        await vm.send(text: "hi")
        XCTAssertEqual(actions.calls.last, .init(kind: "send", text: "hi"))
    }

    func testNotFoundSetsToast() async {
        let actions = MockActionSource()
        let vm = actionVM(actions)
        actions.nextError = YccError.notFound(message: "no such session")
        await vm.send(text: "hi")
        XCTAssertEqual(vm.actionError, "no such session")
    }

    func testInterruptResumeStopInvokeSource() async {
        let actions = MockActionSource()
        let vm = actionVM(actions)
        await vm.interrupt()
        await vm.resumeSession()
        await vm.stopSession()
        XCTAssertEqual(actions.calls.map(\.kind), ["interrupt", "resume", "stop"])
    }
}
