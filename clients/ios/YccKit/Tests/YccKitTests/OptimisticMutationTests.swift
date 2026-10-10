import Foundation
import XCTest
import YccProto
@testable import YccKit

// Task 0403: optimistic mutations, non-blocking actions, and the app-level
// stale-while-revalidate cache. Every RPC costs a phone round trip, so these
// tests hold the fake RPC open (`AsyncGate`) and assert what the UI state looks
// like *while* it is in flight, then after success and after failure.

/// A one-shot latch a fake RPC can park on until the test releases it.
final class AsyncGate: @unchecked Sendable {
    private let lock = NSLock()
    private var continuations: [CheckedContinuation<Void, Never>] = []
    private var opened = false
    private var _waiters = 0

    /// How many callers have reached ``wait()`` (released or not).
    var waiters: Int { lock.lock(); defer { lock.unlock() }; return _waiters }

    func wait() async {
        await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
            lock.lock()
            _waiters += 1
            if opened {
                lock.unlock()
                continuation.resume()
            } else {
                continuations.append(continuation)
                lock.unlock()
            }
        }
    }

    func open() {
        lock.lock()
        opened = true
        let pending = continuations
        continuations.removeAll()
        lock.unlock()
        for continuation in pending { continuation.resume() }
    }
}

@MainActor
func eventually(timeout: TimeInterval = 3, _ condition: () -> Bool) async {
    let deadline = Date().addingTimeInterval(timeout)
    while !condition() && Date() < deadline {
        try? await Task.sleep(nanoseconds: 2_000_000)
    }
}

private func makeEvent(
    _ seq: Int64, _ type: String, _ dataJson: String, actor: String = "coordinator"
) -> Ycc_V1_Event {
    var e = Ycc_V1_Event()
    e.seq = seq
    e.type = type
    e.dataJson = dataJson
    e.actor = actor
    return e
}

/// A transcript + action source whose actions can be held open per kind and
/// whose (legacy) event stream the test drives.
private final class GatedSessionSource: SessionActionSource, SessionTranscriptSource, @unchecked Sendable {
    private let lock = NSLock()
    private var _calls: [String] = []
    private var _texts: [String] = []
    private var continuation: AsyncThrowingStream<Ycc_V1_Event, Error>.Continuation?
    var calls: [String] { lock.lock(); defer { lock.unlock() }; return _calls }
    var sentTexts: [String] { lock.lock(); defer { lock.unlock() }; return _texts }
    var isSubscribed: Bool { lock.lock(); defer { lock.unlock() }; return continuation != nil }

    var transcript: [Ycc_V1_Event] = []
    var transcriptGate: AsyncGate?
    private var _subscribeCount = 0
    var subscribeCount: Int { lock.lock(); defer { lock.unlock() }; return _subscribeCount }
    var gates: [String: AsyncGate] = [:]
    /// Errors thrown by the next call of a kind (consumed once).
    var errors: [String: Error] = [:]

    private func record(_ kind: String, text: String = "") async throws {
        let gate: AsyncGate? = {
            lock.lock()
            defer { lock.unlock() }
            _calls.append(kind)
            if kind == "send" { _texts.append(text) }
            return gates[kind]
        }()
        if let gate { await gate.wait() }
        let error: Error? = {
            lock.lock()
            defer { lock.unlock() }
            return errors.removeValue(forKey: kind)
        }()
        if let error { throw error }
    }

    func emit(_ event: Ycc_V1_Event) {
        lock.lock()
        let continuation = continuation
        lock.unlock()
        continuation?.yield(event)
    }

    func finishStream() {
        lock.lock()
        let continuation = continuation
        self.continuation = nil
        lock.unlock()
        continuation?.finish()
    }

    func reopenSession(project: String, sessionId: String) async throws { try await record("reopen") }
    func sendInput(sessionId: String, text: String, images: [MessageImage]) async throws {
        try await record("send", text: text)
    }
    func answerQuestion(sessionId: String, text: String, optionIndex: Int) async throws {
        try await record("answer")
    }
    func answerQuestions(sessionId: String, answers: [(text: String, optionIndex: Int)]) async throws {
        try await record("answerBatch")
    }
    func interrupt(sessionId: String) async throws { try await record("interrupt") }
    func resume(sessionId: String) async throws { try await record("resume") }
    func rolloverContext(sessionId: String) async throws { try await record("rollover") }
    func stopSession(sessionId: String) async throws { try await record("stop") }

    func getSessionTranscript(project: String, sessionId: String) async throws -> [Ycc_V1_Event] {
        if let transcriptGate { await transcriptGate.wait() }
        return transcript
    }
    func getSessionAttachment(project: String, sessionId: String, attachmentId: String) async throws -> MessageImage {
        throw YccError.notFound(message: "attachment not found")
    }
    func subscribe(sessionId: String, fromSeq: Int64) -> AsyncThrowingStream<Ycc_V1_Event, Error> {
        AsyncThrowingStream { continuation in
            lock.lock()
            self.continuation = continuation
            _subscribeCount += 1
            lock.unlock()
        }
    }
}

/// An indexed (GetSessionView/SubscribeSessionView) source with gated actions.
private final class IndexedGatedSource: SessionActionSource, SessionTranscriptSource, @unchecked Sendable {
    let view: Ycc_V1_GetSessionViewResponse
    private let lock = NSLock()
    private var continuation: AsyncThrowingStream<Ycc_V1_SessionViewUpdate, Error>.Continuation?
    private var _calls: [String] = []
    var calls: [String] { lock.lock(); defer { lock.unlock() }; return _calls }
    var isSubscribed: Bool { lock.lock(); defer { lock.unlock() }; return continuation != nil }
    var answerGate: AsyncGate?
    var answerError: Error?
    var snapshotGate: AsyncGate?
    var reopenGate: AsyncGate?
    private var _snapshotCount = 0
    private var _subscribedFrom: [Int64] = []
    var snapshotCount: Int { lock.lock(); defer { lock.unlock() }; return _snapshotCount }
    var subscribedFrom: [Int64] { lock.lock(); defer { lock.unlock() }; return _subscribedFrom }

    init(view: Ycc_V1_GetSessionViewResponse) { self.view = view }

    func send(_ update: Ycc_V1_SessionViewUpdate) {
        lock.lock()
        let continuation = continuation
        lock.unlock()
        continuation?.yield(update)
    }

    var supportsIndexedSessionView: Bool { true }
    func getSessionView(project: String, sessionId: String) async throws -> Ycc_V1_GetSessionViewResponse {
        bumpSnapshots()
        if let snapshotGate { await snapshotGate.wait() }
        return view
    }
    private func bumpSnapshots() {
        lock.lock()
        _snapshotCount += 1
        lock.unlock()
    }
    func subscribeSessionView(sessionId: String, fromSeq: Int64) -> AsyncThrowingStream<Ycc_V1_SessionViewUpdate, Error> {
        AsyncThrowingStream { continuation in
            lock.lock()
            self.continuation = continuation
            _subscribedFrom.append(fromSeq)
            lock.unlock()
        }
    }
    func getSessionTranscript(project: String, sessionId: String) async throws -> [Ycc_V1_Event] { [] }
    func getSessionAttachment(project: String, sessionId: String, attachmentId: String) async throws -> MessageImage {
        throw YccError.notFound(message: "attachment not found")
    }
    func subscribe(sessionId: String, fromSeq: Int64) -> AsyncThrowingStream<Ycc_V1_Event, Error> {
        AsyncThrowingStream { $0.finish() }
    }

    private func recordCall(_ kind: String) {
        lock.lock()
        _calls.append(kind)
        lock.unlock()
    }

    func reopenSession(project: String, sessionId: String) async throws {
        recordCall("reopen")
        if let reopenGate { await reopenGate.wait() }
    }
    func sendInput(sessionId: String, text: String, images: [MessageImage]) async throws { recordCall("send") }
    func answerQuestion(sessionId: String, text: String, optionIndex: Int) async throws {
        recordCall("answer")
        if let answerGate { await answerGate.wait() }
        if let answerError { throw answerError }
    }
    func answerQuestions(sessionId: String, answers: [(text: String, optionIndex: Int)]) async throws {
        recordCall("answerBatch")
    }
    func interrupt(sessionId: String) async throws { recordCall("interrupt") }
    func resume(sessionId: String) async throws { recordCall("resume") }
    func rolloverContext(sessionId: String) async throws { recordCall("rollover") }
    func stopSession(sessionId: String) async throws { recordCall("stop") }
}

@MainActor
final class SessionOptimisticActionTests: XCTestCase {
    private let question = makeEvent(1, "question_asked", #"{"question":"Proceed?","options":["yes","no"]}"#)

    private func liveVM(_ source: GatedSessionSource, controlAckTimeout: UInt64 = 60_000_000_000,
                        sentEchoTimeout: UInt64 = 60_000_000_000,
                        sleep: (@Sendable (UInt64) async throws -> Void)? = nil) -> SessionViewModel {
        SessionViewModel(
            source: source, actions: source, project: "demo", sessionID: "s1", mode: .live,
            publishInterval: 0, controlAckTimeout: controlAckTimeout, sentEchoTimeout: sentEchoTimeout,
            sleep: sleep ?? { try await Task.sleep(nanoseconds: $0) })
    }

    private func answeredText(_ vm: SessionViewModel) -> String?? {
        for row in vm.durableRows.reversed() {
            if case .question(_, _, let answer) = row.kind { return .some(answer) }
        }
        return .none
    }

    // MARK: Answers

    func testAnswerClosesGateBeforeTheRoundTripAndDropsDoubleSubmit() async {
        let source = GatedSessionSource()
        source.transcript = [question]
        let gate = AsyncGate()
        source.gates["answer"] = gate
        let vm = liveVM(source)
        vm.start()
        await eventually { vm.pendingQuestion != nil && source.isSubscribed }

        let first = Task { await vm.answer(optionIndex: 1) }
        await eventually { gate.waiters == 1 }
        XCTAssertTrue(vm.isSubmittingAnswer)
        XCTAssertNil(vm.pendingQuestion, "the sheet/banner close before the RPC returns")
        XCTAssertEqual(answeredText(vm), .some("no"))

        let second = await vm.answer(optionIndex: 0)
        XCTAssertFalse(second, "a double tap must not send a second answer")

        gate.open()
        let accepted = await first.value
        XCTAssertTrue(accepted)
        XCTAssertEqual(source.calls, ["answer"])
        XCTAssertFalse(vm.isSubmittingAnswer)
        XCTAssertNil(vm.pendingQuestion)
        XCTAssertNil(vm.actionError)
        vm.stop()
    }

    func testFailedAnswerRestoresTheQuestionWithAnError() async {
        let source = GatedSessionSource()
        source.transcript = [question]
        let gate = AsyncGate()
        source.gates["answer"] = gate
        source.errors["answer"] = YccError.rpc(message: "offline")
        let vm = liveVM(source)
        vm.start()
        await eventually { vm.pendingQuestion != nil }
        let rowID = vm.pendingQuestion?.rowID

        let pending = Task { await vm.answer(text: "go") }
        await eventually { gate.waiters == 1 }
        XCTAssertNil(vm.pendingQuestion)
        gate.open()
        let accepted = await pending.value

        XCTAssertFalse(accepted)
        XCTAssertEqual(vm.pendingQuestion?.rowID, rowID, "rolled back: the question is waiting again")
        XCTAssertEqual(answeredText(vm), .some(nil), "the row reads unanswered again")
        XCTAssertEqual(vm.actionError, "offline")
        XCTAssertEqual(vm.rolledBackQuestionRowID, rowID)
        vm.stop()
    }

    func testRollbackDefersToADurableAnswerThatArrivedMeanwhile() async {
        let source = GatedSessionSource()
        source.transcript = [question]
        let gate = AsyncGate()
        source.gates["answer"] = gate
        source.errors["answer"] = YccError.failedPrecondition(message: "no pending question")
        let vm = liveVM(source)
        vm.start()
        await eventually { vm.pendingQuestion != nil && source.isSubscribed }

        let pending = Task { await vm.answer(optionIndex: 0) }
        await eventually { gate.waiters == 1 }
        // Answered from another client while ours was in flight.
        source.emit(makeEvent(2, "question_answered", #"{"answer":"yes"}"#))
        await eventually { vm.projection.lastPersistedSeq == 2 }
        gate.open()
        _ = await pending.value

        XCTAssertNil(vm.pendingQuestion, "durable truth wins over the rollback")
        XCTAssertEqual(vm.actionError, "no pending question")
        vm.stop()
    }

    func testStaleIndexedStateCannotReopenAnOptimisticallyAnsweredQuestion() async {
        var view = Ycc_V1_GetSessionViewResponse()
        view.state.indexedThroughSeq = 10
        view.state.phase = "running"
        view.state.pendingRowID = "seq-10"
        var q = Ycc_V1_SessionViewQuestion()
        q.prompt = "Proceed?"
        q.options = ["yes", "no"]
        view.state.pendingQuestions = [q]
        var row = Ycc_V1_SessionPresentationRow()
        row.id = "seq-10"
        row.positionSeq = 10
        row.updatedSeq = 10
        row.events = [makeEvent(10, "question_asked", #"{"question":"Proceed?","options":["yes","no"]}"#)]
        view.rows = [row]
        let source = IndexedGatedSource(view: view)
        let gate = AsyncGate()
        source.answerGate = gate
        let vm = SessionViewModel(source: source, actions: source, sessionID: "s1", mode: .live, publishInterval: 0)
        vm.start()
        await eventually { vm.pendingQuestion != nil && source.isSubscribed }

        let pending = Task { await vm.answer(optionIndex: 0) }
        await eventually { gate.waiters == 1 }
        XCTAssertNil(vm.pendingQuestion)

        // A snapshot produced before the daemon processed the answer.
        var stale = Ycc_V1_SessionViewUpdate()
        stale.state = view.state
        stale.state.indexedThroughSeq = 11
        source.send(stale)
        await eventually { vm.projection.lastPersistedSeq == 11 }
        XCTAssertNil(vm.pendingQuestion, "a stale state must not re-open the answered gate")

        gate.open()
        let accepted = await pending.value
        XCTAssertTrue(accepted)
        var answered = Ycc_V1_SessionViewUpdate()
        answered.state.indexedThroughSeq = 12
        answered.state.phase = "running"
        source.send(answered)
        await eventually { vm.projection.lastPersistedSeq == 12 }
        XCTAssertNil(vm.pendingQuestion)

        // A genuinely new question still opens normally.
        var next = Ycc_V1_SessionViewUpdate()
        next.state = view.state
        next.state.indexedThroughSeq = 13
        next.state.pendingRowID = "seq-13"
        source.send(next)
        await eventually { vm.projection.lastPersistedSeq == 13 }
        XCTAssertEqual(vm.pendingQuestion?.rowID, "seq-13")
        vm.stop()
    }

    // MARK: Session controls

    func testInterruptShowsPausingAtOnceAndDisablesControlsUntilAcknowledged() async {
        let source = GatedSessionSource()
        source.transcript = [makeEvent(1, "session_started", "{}")]
        let gate = AsyncGate()
        source.gates["interrupt"] = gate
        let vm = liveVM(source)
        vm.start()
        await eventually { source.isSubscribed }
        XCTAssertFalse(vm.displayPauseRequested)

        let pending = Task { await vm.interrupt() }
        await eventually { gate.waiters == 1 }
        XCTAssertTrue(vm.isControlInFlight)
        XCTAssertTrue(vm.displayPauseRequested, "the pausing banner shows before the RPC returns")
        XCTAssertFalse(vm.projection.pauseRequested, "durable state is untouched")
        await vm.resumeSession()
        await vm.stopSession()
        XCTAssertEqual(source.calls, ["interrupt"], "controls are disabled while one is in flight")

        gate.open()
        await pending.value
        XCTAssertFalse(vm.isControlInFlight)
        XCTAssertEqual(vm.pendingControl?.kind, .pause, "still bridging until the durable ack")
        XCTAssertTrue(vm.displayPauseRequested)

        source.emit(makeEvent(2, "pause_requested", "{}"))
        await eventually { vm.pendingControl == nil }
        XCTAssertNil(vm.pendingControl, "the durable ack retires the optimistic state")
        XCTAssertTrue(vm.displayPauseRequested)
        XCTAssertTrue(vm.projection.pauseRequested)
        vm.stop()
    }

    func testFailedResumeRevertsToPausedWithAnError() async {
        let source = GatedSessionSource()
        source.transcript = [makeEvent(1, "interrupted", "{}")]
        let gate = AsyncGate()
        source.gates["resume"] = gate
        source.errors["resume"] = YccError.failedPrecondition(message: "not paused")
        let vm = liveVM(source)
        vm.start()
        await eventually { vm.phase == .paused }

        let pending = Task { await vm.resumeSession() }
        await eventually { gate.waiters == 1 }
        XCTAssertEqual(vm.displayPhase, .running, "the paused banner clears on tap")
        XCTAssertEqual(vm.phase, .paused)
        gate.open()
        await pending.value

        XCTAssertNil(vm.pendingControl)
        XCTAssertEqual(vm.displayPhase, .paused, "reverted on failure")
        XCTAssertEqual(vm.actionError, "not paused")
        vm.stop()
    }

    func testRetryHidesTheErrorBannerUntilTheDurableResume() async {
        let source = GatedSessionSource()
        source.transcript = [makeEvent(1, "session_error", #"{"msg":"rate limited"}"#)]
        let vm = liveVM(source)
        vm.start()
        await eventually { if case .error = vm.phase { return source.isSubscribed }; return false }

        await vm.retry()
        XCTAssertEqual(vm.displayPhase, .running)
        source.emit(makeEvent(2, "resumed", "{}"))
        await eventually { vm.pendingControl == nil }
        XCTAssertEqual(vm.phase, .running)
        vm.stop()
    }

    func testMissingEchoFallsBackToDurableStateAfterTimeout() async {
        let source = GatedSessionSource()
        let timeout = AsyncGate()
        let vm = liveVM(source, controlAckTimeout: 1, sleep: { _ in await timeout.wait() })

        await vm.stopSession()
        XCTAssertTrue(vm.isStopPending)
        XCTAssertFalse(vm.isControlInFlight)
        timeout.open()
        await eventually { vm.pendingControl == nil }
        XCTAssertNil(vm.pendingControl)
        XCTAssertFalse(vm.isStopPending)
    }

    // MARK: Send

    func testSendShowsAProvisionalBubbleThatTheEchoRetires() async {
        let source = GatedSessionSource()
        source.transcript = [makeEvent(1, "session_started", "{}")]
        let gate = AsyncGate()
        source.gates["send"] = gate
        let vm = liveVM(source)
        vm.start()
        await eventually { source.isSubscribed }

        let pending = Task { await vm.send(text: "  hello  ") }
        await eventually { gate.waiters == 1 }
        XCTAssertEqual(vm.pendingUserMessages.map(\.text), ["hello"])
        XCTAssertEqual(vm.pendingUserMessages.first?.status, .sending)
        XCTAssertTrue(vm.isAwaitingAgentActivity)

        gate.open()
        await pending.value
        XCTAssertEqual(vm.pendingUserMessages.first?.status, .sent)

        source.emit(makeEvent(2, "user_input", #"{"text":"hello"}"#, actor: "user"))
        await eventually { vm.pendingUserMessages.isEmpty }
        XCTAssertTrue(vm.pendingUserMessages.isEmpty, "the durable echo replaces the bubble")
        XCTAssertEqual(vm.durableRows.filter {
            if case .userMessage = $0.kind { return true }
            return false
        }.count, 1, "no duplicate bubble")
        vm.stop()
    }

    func testOlderIdenticalMessageDoesNotRetireANewBubble() async {
        let source = GatedSessionSource()
        source.transcript = [makeEvent(1, "user_input", #"{"text":"continue"}"#, actor: "user")]
        let vm = liveVM(source)
        vm.start()
        await eventually { vm.projection.lastPersistedSeq == 1 && source.isSubscribed }

        await vm.send(text: "continue")
        source.emit(makeEvent(2, "model_turn", #"{"text":"ok"}"#))
        await eventually { vm.projection.lastPersistedSeq == 2 }
        XCTAssertEqual(vm.pendingUserMessages.count, 1, "seq-1 predates this send")
        vm.stop()
    }

    func testFailedSendIsMarkedFailedAndCanBeRetriedOrDiscarded() async {
        let source = GatedSessionSource()
        source.errors["send"] = YccError.rpc(message: "offline")
        let vm = liveVM(source)

        await vm.send(text: "hello")
        XCTAssertEqual(vm.pendingUserMessages.first?.status, .failed("offline"))
        XCTAssertEqual(vm.actionError, "offline")
        XCTAssertFalse(vm.isAwaitingAgentActivity)

        let id = vm.pendingUserMessages[0].id
        await vm.retrySend(id: id)
        XCTAssertEqual(source.sentTexts, ["hello", "hello"])
        XCTAssertEqual(vm.pendingUserMessages.first?.status, .sent)
        XCTAssertNil(vm.discardFailedSend(id: id), "only failed messages can be discarded")

        source.errors["send"] = YccError.rpc(message: "offline")
        await vm.send(text: "second")
        let failedID = vm.pendingUserMessages.last!.id
        XCTAssertEqual(vm.discardFailedSend(id: failedID)?.text, "second")
        XCTAssertEqual(vm.pendingUserMessages.map(\.text), ["hello"])
    }

    func testPersistedSendShowsTheBubbleWhileReopening() async {
        let source = GatedSessionSource()
        let gate = AsyncGate()
        source.gates["reopen"] = gate
        let vm = SessionViewModel(
            source: source, actions: source, project: "demo", sessionID: "s1", mode: .persisted,
            publishInterval: 0)

        let pending = Task { await vm.send(text: "more work") }
        await eventually { gate.waiters == 1 }
        XCTAssertTrue(vm.isReopening)
        XCTAssertEqual(vm.pendingUserMessages.map(\.text), ["more work"])
        XCTAssertEqual(source.calls, ["reopen"], "SendInput waits for ResumeSession")

        gate.open()
        await pending.value
        XCTAssertEqual(source.calls, ["reopen", "send"])
        XCTAssertEqual(vm.mode, .live)
        XCTAssertFalse(vm.isReopening)
        vm.stop()
    }

    func testReopenFailureMarksTheBubbleFailed() async {
        let source = GatedSessionSource()
        source.errors["reopen"] = YccError.notFound(message: "session log expired")
        let vm = SessionViewModel(source: source, actions: source, sessionID: "s1", mode: .persisted)

        await vm.send(text: "more work")
        XCTAssertEqual(source.calls, ["reopen"])
        XCTAssertEqual(vm.pendingUserMessages.first?.status, .failed("session log expired"))
    }

    // MARK: Reopen on open

    func testStartWithReopenLoadsHistoryWhileResumingAndSharesOneResume() async {
        let source = GatedSessionSource()
        source.transcript = [makeEvent(1, "model_turn", #"{"text":"earlier"}"#)]
        let gate = AsyncGate()
        source.gates["reopen"] = gate
        let vm = SessionViewModel(
            source: source, actions: source, project: "demo", sessionID: "s1", mode: .persisted,
            publishInterval: 0)

        vm.start(reopen: true)
        await eventually { vm.state == .finished && gate.waiters == 1 }
        XCTAssertEqual(vm.durableRows.count, 1, "history paints while ResumeSession is in flight")
        XCTAssertTrue(vm.isReopening)

        // A send racing the open-time reopen joins it rather than resuming twice.
        let send = Task { await vm.send(text: "next") }
        await eventually { vm.pendingUserMessages.count == 1 }
        gate.open()
        await send.value
        XCTAssertEqual(source.calls, ["reopen", "send"])
        XCTAssertEqual(vm.mode, .live)
        vm.stop()
    }
}

@MainActor
final class SessionReviewFollowUpTests: XCTestCase {
    private let question = makeEvent(1, "question_asked", #"{"question":"Proceed?","options":["yes","no"]}"#)

    private func liveVM(_ source: GatedSessionSource, sentEchoTimeout: UInt64 = 60_000_000_000,
                        sleep: (@Sendable (UInt64) async throws -> Void)? = nil) -> SessionViewModel {
        SessionViewModel(
            source: source, actions: source, project: "demo", sessionID: "s1", mode: .live,
            publishInterval: 0, sentEchoTimeout: sentEchoTimeout,
            sleep: sleep ?? { try await Task.sleep(nanoseconds: $0) })
    }

    private func answeredText(_ vm: SessionViewModel) -> String?? {
        for row in vm.durableRows.reversed() {
            if case .question(_, _, let answer) = row.kind { return .some(answer) }
        }
        return .none
    }

    // B1: the daemon answers a pending question with text-only SendInput.

    func testTextSentWhileAQuestionIsPendingAnswersItWithoutABubble() async {
        let source = GatedSessionSource()
        source.transcript = [question]
        let gate = AsyncGate()
        source.gates["send"] = gate
        let vm = liveVM(source)
        vm.start()
        await eventually { vm.pendingQuestion != nil && source.isSubscribed }

        let pending = Task { await vm.send(text: " go with plan B ") }
        await eventually { gate.waiters == 1 }
        XCTAssertTrue(vm.pendingUserMessages.isEmpty, "no user_input echo will come: no bubble")
        XCTAssertNil(vm.pendingQuestion, "the gate closes optimistically")
        XCTAssertEqual(answeredText(vm), .some("go with plan B"))
        XCTAssertTrue(vm.isSubmittingAnswer)
        gate.open()
        await pending.value

        XCTAssertEqual(source.sentTexts, ["go with plan B"])
        source.emit(makeEvent(2, "question_answered", #"{"answer":"go with plan B"}"#))
        await eventually { vm.projection.lastPersistedSeq == 2 }
        XCTAssertTrue(vm.pendingUserMessages.isEmpty)
        XCTAssertNil(vm.pendingQuestion)
        vm.stop()
    }

    func testTextSentWhileABatchIsPendingAlsoAnswersIt() async {
        let source = GatedSessionSource()
        source.transcript = [makeEvent(1, "question_asked",
            #"{"questions":[{"question":"Which DB?","options":["pg"]},{"question":"When?"}]}"#)]
        let vm = liveVM(source)
        vm.start()
        await eventually { vm.pendingQuestion?.isBatch == true }

        await vm.send(text: "whatever you think")
        XCTAssertTrue(vm.pendingUserMessages.isEmpty)
        XCTAssertNil(vm.pendingQuestion)
        vm.stop()
    }

    func testFailedSendAsAnswerRestoresTheQuestionAndKeepsTheText() async {
        let source = GatedSessionSource()
        source.transcript = [question]
        source.errors["send"] = YccError.rpc(message: "offline")
        let vm = liveVM(source)
        vm.start()
        await eventually { vm.pendingQuestion != nil && source.isSubscribed }

        await vm.send(text: "plan B")
        XCTAssertNotNil(vm.pendingQuestion, "rolled back")
        XCTAssertEqual(vm.actionError, "offline")
        XCTAssertEqual(vm.pendingUserMessages.map(\.text), ["plan B"])
        XCTAssertTrue(vm.pendingUserMessages.first?.isFailed == true, "the cleared composer text is not lost")

        // Retrying while the question is still pending answers it again.
        await vm.retrySend(id: vm.pendingUserMessages[0].id)
        XCTAssertEqual(source.sentTexts, ["plan B", "plan B"])
        XCTAssertTrue(vm.pendingUserMessages.isEmpty)
        XCTAssertNil(vm.pendingQuestion)
        vm.stop()
    }

    func testPicturesWhileAQuestionIsPendingFailVisiblyWithoutClosingIt() async {
        let source = GatedSessionSource()
        source.transcript = [question]
        source.errors["send"] = YccError.failedPrecondition(
            message: "answer the pending question before sending pictures")
        let vm = liveVM(source)
        vm.start()
        await eventually { vm.pendingQuestion != nil }
        let picture = MessageImage(data: Data([1]), mediaType: "image/png")

        await vm.send(text: "look", images: [picture])
        XCTAssertNotNil(vm.pendingQuestion)
        XCTAssertEqual(vm.pendingUserMessages.first?.status,
                       .failed("answer the pending question before sending pictures"))
        XCTAssertEqual(vm.discardFailedSend(id: vm.pendingUserMessages[0].id)?.images, [picture],
                       "Edit restores the pictures too")
        vm.stop()
    }

    // S2: "no pending question" means already answered elsewhere.

    func testAlreadyAnsweredFailureKeepsTheGateClosedAndResyncs() async {
        let source = GatedSessionSource()
        source.transcript = [question]
        source.errors["answer"] = YccError.failedPrecondition(message: "session s1 has no pending question")
        let vm = liveVM(source)
        vm.start()
        await eventually { vm.pendingQuestion != nil && source.isSubscribed }
        let subscriptions = source.subscribeCount

        let accepted = await vm.answer(optionIndex: 0)
        XCTAssertFalse(accepted)
        XCTAssertNil(vm.pendingQuestion, "no phantom 'question waiting' banner")
        XCTAssertNil(vm.rolledBackQuestionRowID)
        XCTAssertEqual(vm.actionError, "session s1 has no pending question")
        await eventually { source.subscribeCount > subscriptions }
        XCTAssertGreaterThan(source.subscribeCount, subscriptions, "server truth is re-fetched")
        vm.stop()
    }

    func testDisabledModelFailureStillRestoresTheQuestion() async {
        let source = GatedSessionSource()
        source.transcript = [question]
        source.errors["answer"] = YccError.failedPrecondition(message: "model is disabled")
        let vm = liveVM(source)
        vm.start()
        await eventually { vm.pendingQuestion != nil }

        _ = await vm.answer(optionIndex: 0)
        XCTAssertNotNil(vm.pendingQuestion, "the question is still pending on the daemon")
        vm.stop()
    }

    // S4: accepted bubbles never linger.

    func testSentBubbleIsDroppedWhenTheStreamEnds() async {
        let source = GatedSessionSource()
        source.transcript = [makeEvent(1, "session_started", "{}")]
        let vm = liveVM(source)
        vm.start()
        await eventually { source.isSubscribed && vm.state == .streaming }

        await vm.send(text: "hello")
        XCTAssertEqual(vm.pendingUserMessages.first?.status, .sent)
        source.finishStream()
        await eventually { vm.state == .finished }
        XCTAssertTrue(vm.pendingUserMessages.isEmpty)
    }

    func testSentBubbleExpiresWithoutAnEcho() async {
        let source = GatedSessionSource()
        let timeout = AsyncGate()
        let vm = liveVM(source, sentEchoTimeout: 1, sleep: { _ in await timeout.wait() })
        source.errors["send"] = YccError.rpc(message: "offline")
        await vm.send(text: "failed one")
        await vm.send(text: "hello")
        XCTAssertEqual(vm.pendingUserMessages.map(\.status), [.failed("offline"), .sent])

        timeout.open()
        await eventually { vm.pendingUserMessages.count == 1 }
        XCTAssertEqual(vm.pendingUserMessages.map(\.text), ["failed one"], "failed bubbles keep Retry/Edit")
    }

    // Nit: a send before any history is installed cannot be retired by older rows.

    func testMessageSentBeforeHistoryLoadsIgnoresAnOlderIdenticalRow() async {
        let source = GatedSessionSource()
        source.transcript = [makeEvent(1, "user_input", #"{"text":"continue"}"#, actor: "user")]
        let historyGate = AsyncGate()
        source.transcriptGate = historyGate
        let vm = liveVM(source)
        vm.start()
        await eventually { historyGate.waiters == 1 }

        await vm.send(text: "continue")
        historyGate.open()
        await eventually { vm.projection.lastPersistedSeq == 1 && source.isSubscribed }
        XCTAssertEqual(vm.pendingUserMessages.count, 1, "seq 1 predates the send")

        source.emit(makeEvent(2, "user_input", #"{"text":"continue"}"#, actor: "user"))
        await eventually { vm.pendingUserMessages.isEmpty }
        XCTAssertTrue(vm.pendingUserMessages.isEmpty)
        vm.stop()
    }

    // S3: resume-from-list takes one snapshot.

    private func indexedHistory() -> Ycc_V1_GetSessionViewResponse {
        var view = Ycc_V1_GetSessionViewResponse()
        view.state.indexedThroughSeq = 7
        view.state.phase = "idle"
        var row = Ycc_V1_SessionPresentationRow()
        row.id = "seq-7"
        row.positionSeq = 7
        row.updatedSeq = 7
        row.events = [makeEvent(7, "model_turn", #"{"text":"done"}"#)]
        view.rows = [row]
        return view
    }

    func testReopenAfterHistoryPaintsSubscribesWithoutASecondSnapshot() async {
        let source = IndexedGatedSource(view: indexedHistory())
        let reopen = AsyncGate()
        source.reopenGate = reopen
        let vm = SessionViewModel(source: source, actions: source, sessionID: "s1", mode: .persisted,
                                  publishInterval: 0)

        vm.start(reopen: true)
        await eventually { vm.state == .finished && reopen.waiters == 1 }
        XCTAssertEqual(vm.durableRows.map(\.id), ["seq-7"])
        reopen.open()
        await eventually { source.isSubscribed }

        XCTAssertEqual(vm.mode, .live)
        XCTAssertEqual(source.snapshotCount, 1, "the installed snapshot is reused")
        XCTAssertEqual(source.subscribedFrom, [7])
        vm.stop()
    }

    func testReopenFinishingBeforeTheSnapshotStreamsFromThatSnapshot() async {
        let source = IndexedGatedSource(view: indexedHistory())
        let snapshot = AsyncGate()
        source.snapshotGate = snapshot
        let vm = SessionViewModel(source: source, actions: source, sessionID: "s1", mode: .persisted,
                                  publishInterval: 0)

        vm.start(reopen: true)
        await eventually { vm.mode == .live && snapshot.waiters == 1 }
        snapshot.open()
        await eventually { source.isSubscribed }

        XCTAssertEqual(source.snapshotCount, 1, "the in-flight load is not cancelled and redone")
        XCTAssertEqual(source.subscribedFrom, [7])
        XCTAssertEqual(vm.durableRows.map(\.id), ["seq-7"])
        vm.stop()
    }
}

// MARK: - App data cache

@MainActor
final class AppDataCacheTests: XCTestCase {
    func testClearForgetsEverythingAndRejectsStaleWrites() async {
        let cache = AppDataCache()
        let generation = cache.generation
        cache.store([1, 2], for: .backlog("p"))
        cache.updateProjects([Ycc_V1_ProjectInfo()])

        cache.clear()

        XCTAssertNil(cache.value(.backlog("p"), as: [Int].self))
        XCTAssertNil(cache.projects)
        cache.store([3], for: .backlog("p"), ifGeneration: generation)
        XCTAssertNil(cache.value(.backlog("p"), as: [Int].self), "a pre-switch load must not repopulate")
        cache.store([4], for: .backlog("p"), ifGeneration: cache.generation)
        XCTAssertEqual(cache.value(.backlog("p"), as: [Int].self), [4])
    }
}

// MARK: - Backlog

private final class GatedBacklogSource: BacklogSource, @unchecked Sendable {
    var tasks: [Ycc_V1_BacklogTaskSummary] = []
    var projects: [Ycc_V1_ProjectInfo] = []
    var updateGate: AsyncGate?
    var updateError: Error?
    private(set) var listCount = 0
    private(set) var listProjectsCount = 0
    private(set) var updateCount = 0

    func listBacklog(project: String) async throws -> [Ycc_V1_BacklogTaskSummary] {
        listCount += 1
        return tasks
    }
    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        listProjectsCount += 1
        return projects
    }
    func createTask(project: String, title: String, body: String, priority: Int) async throws -> Ycc_V1_TaskDetail {
        var detail = Ycc_V1_TaskDetail()
        detail.id = "0100"
        detail.title = title
        detail.status = "todo"
        detail.priority = Int32(priority)
        return detail
    }
    func updateTaskStatus(project: String, id: String, status: String) async throws -> Ycc_V1_TaskDetail {
        updateCount += 1
        if let updateGate { await updateGate.wait() }
        if let updateError { throw updateError }
        // Reflect the change in the list, as the daemon would on the next load.
        if let index = tasks.firstIndex(where: { $0.id == id }) {
            tasks[index].status = status
            tasks[index].priority = 2
        }
        var detail = Ycc_V1_TaskDetail()
        detail.id = id
        detail.title = "Task \(id)"
        detail.status = status
        detail.priority = 2
        detail.ready = true
        return detail
    }
}

private func task(_ id: String, _ status: String) -> Ycc_V1_BacklogTaskSummary {
    var s = Ycc_V1_BacklogTaskSummary()
    s.id = id
    s.title = "Task \(id)"
    s.status = status
    s.priority = 3
    return s
}

private func project(_ name: String) -> Ycc_V1_ProjectInfo {
    var p = Ycc_V1_ProjectInfo()
    p.name = name
    return p
}

@MainActor
final class BacklogOptimisticTests: XCTestCase {
    func testStatusChangeMovesTheRowAtOnceAndOnlyBusiesThatRow() async {
        let source = GatedBacklogSource()
        source.tasks = [task("0001", "todo"), task("0002", "todo")]
        let gate = AsyncGate()
        source.updateGate = gate
        let model = BacklogModel(source: source, selectedProject: "proj")
        await model.refresh()

        let first = Task { await model.setStatus(taskID: "0001", to: .done) }
        await eventually { gate.waiters == 1 }
        XCTAssertEqual(model.tasks.first { $0.id == "0001" }?.status, "done", "moved before the RPC returns")
        XCTAssertTrue(model.isUpdating("0001"))
        XCTAssertFalse(model.isUpdating("0002"), "other rows stay interactive")
        let again = await model.setStatus(taskID: "0001", to: .inProgress)
        XCTAssertFalse(again, "the same row cannot be changed twice concurrently")

        let second = Task { await model.setStatus(taskID: "0002", to: .inProgress) }
        await eventually { gate.waiters == 2 }
        XCTAssertEqual(model.tasks.first { $0.id == "0002" }?.status, "in_progress")

        // A list landing mid-flight must not flicker optimistic rows back.
        await model.refresh()
        XCTAssertEqual(model.tasks.first { $0.id == "0001" }?.status, "done")

        gate.open()
        let results = (await first.value, await second.value)
        XCTAssertTrue(results.0 && results.1)
        XCTAssertTrue(model.updatingTaskIDs.isEmpty)
        XCTAssertEqual(model.tasks.first { $0.id == "0001" }?.priority, 2, "patched from the response")
        XCTAssertEqual(source.updateCount, 2)
    }

    func testFailedStatusChangeRollsBack() async {
        let source = GatedBacklogSource()
        source.tasks = [task("0001", "todo")]
        let gate = AsyncGate()
        source.updateGate = gate
        source.updateError = YccError.failedPrecondition(message: "unknown status")
        let model = BacklogModel(source: source, selectedProject: "proj")
        await model.refresh()

        let pending = Task { await model.setStatus(taskID: "0001", to: .done) }
        await eventually { gate.waiters == 1 }
        XCTAssertEqual(model.tasks.first?.status, "done")
        gate.open()
        let ok = await pending.value

        XCTAssertFalse(ok)
        XCTAssertEqual(model.tasks.first?.status, "todo", "rolled back")
        XCTAssertEqual(model.updateError, "unknown status")
        XCTAssertFalse(model.isUpdating("0001"))
    }

    func testStatusChangeRevalidatesTheBacklogOnlyInTheBackground() async {
        let cache = AppDataCache()
        cache.updateProjects([project("proj"), project("other")])
        let source = GatedBacklogSource()
        source.tasks = [task("0001", "todo")]
        let model = BacklogModel(source: source, selectedProject: "proj", cache: cache)
        await model.refresh()
        let listsBefore = source.listCount

        let ok = await model.setStatus(taskID: "0001", to: .done)
        XCTAssertTrue(ok)
        XCTAssertEqual(source.listCount, listsBefore, "the caller is not held for a reload")
        await model.backgroundRefreshTask?.value
        XCTAssertEqual(source.listCount, listsBefore + 1)
        XCTAssertEqual(source.listProjectsCount, 0, "no ListProjects: the app-level list is used")
    }

    func testRefreshFetchesProjectsOnlyWithoutAnAppLevelList() async {
        let source = GatedBacklogSource()
        source.projects = [project("proj")]
        let uncached = BacklogModel(source: source)
        await uncached.refresh()
        XCTAssertEqual(source.listProjectsCount, 1)
        XCTAssertEqual(uncached.selectedProject, "proj", "a sole project is adopted")

        let cache = AppDataCache()
        cache.updateProjects([project("a"), project("b")])
        let cached = BacklogModel(source: source, selectedProject: "a", cache: cache)
        XCTAssertEqual(cached.projects.map(\.name), ["a", "b"], "the filter is ready before any RPC")
        await cached.refresh()
        XCTAssertEqual(source.listProjectsCount, 1)
    }

    func testQuickCaptureInsertsTheCreatedTaskWithoutWaitingForAReload() async {
        let source = GatedBacklogSource()
        let model = BacklogModel(source: source, selectedProject: "proj")
        await model.refresh()
        let before = source.listCount

        let ok = await model.create(title: "new idea", body: "", priority: 2)

        XCTAssertTrue(ok)
        XCTAssertEqual(model.tasks.map(\.id), ["0100"])
        XCTAssertEqual(model.tasks.first?.priority, 2)
        XCTAssertEqual(source.listCount, before)
        await model.backgroundRefreshTask?.value
        XCTAssertEqual(source.listCount, before + 1)
    }

    func testRevisitRendersCachedBacklogAndRevalidates() async {
        let cache = AppDataCache()
        let source = GatedBacklogSource()
        source.tasks = [task("0001", "todo")]
        let first = BacklogModel(source: source, selectedProject: "proj", cache: cache)
        await first.refresh()

        source.tasks = [task("0001", "done"), task("0002", "todo")]
        let revisit = BacklogModel(source: source, selectedProject: "proj", cache: cache)
        XCTAssertEqual(revisit.tasks.map(\.status), ["todo"], "last data shows instantly")
        await revisit.refresh()
        XCTAssertEqual(revisit.tasks.map(\.id), ["0001", "0002"])

        cache.clear()
        XCTAssertTrue(BacklogModel(source: source, selectedProject: "proj", cache: cache).tasks.isEmpty)
    }
}

// MARK: - Task detail

private final class StubTaskDetailSource: TaskDetailSource, @unchecked Sendable {
    var detail = Ycc_V1_TaskDetail()
    var getGate: AsyncGate?
    var updateGate: AsyncGate?
    var updateError: Error?

    func getTask(project: String, id: String) async throws -> Ycc_V1_TaskDetail {
        if let getGate { await getGate.wait() }
        return detail
    }
    func listSessionHistory(project: String, limit: Int32, cursor: String) async throws -> SessionHistoryPage {
        SessionHistoryPage(sessions: [], pinned: [], nextCursor: "")
    }
    func updateTaskStatus(project: String, id: String, status: String) async throws -> Ycc_V1_TaskDetail {
        if let updateGate { await updateGate.wait() }
        if let updateError { throw updateError }
        var updated = detail
        updated.status = status
        return updated
    }
    func updateTask(
        project: String, id: String, title: String, status: String,
        priority: Int, body: String, dependsOn: [String], specRefs: [String]
    ) async throws -> Ycc_V1_TaskDetail { detail }
}

@MainActor
final class TaskDetailSeedingTests: XCTestCase {
    func testDetailRendersFromTheBacklogRowWhileGetTaskLoads() async {
        let cache = AppDataCache()
        cache.store([task("0007", "in_review")], for: .backlog("proj"))
        let source = StubTaskDetailSource()
        source.detail.id = "0007"
        source.detail.title = "Task 0007"
        source.detail.status = "in_review"
        source.detail.body = "full body"
        let gate = AsyncGate()
        source.getGate = gate
        let model = TaskDetailModel(source: source, project: "proj", taskID: "0007", cache: cache)

        XCTAssertEqual(model.task?.title, "Task 0007", "header renders before any RPC")
        XCTAssertEqual(model.status, .inReview)
        XCTAssertTrue(model.isPlaceholder)
        XCTAssertFalse(model.canEdit, "editing waits for the real detail")

        let load = Task { await model.load() }
        await eventually { gate.waiters == 1 }
        gate.open()
        await load.value
        XCTAssertFalse(model.isPlaceholder)
        XCTAssertEqual(model.task?.body, "full body")
        XCTAssertTrue(model.canEdit)

        // A revisit shows the cached full detail at once.
        let revisit = TaskDetailModel(source: source, project: "proj", taskID: "0007", cache: cache)
        XCTAssertEqual(revisit.task?.body, "full body")
        XCTAssertFalse(revisit.isPlaceholder)
    }

    func testStatusChangeIsOptimisticAndRollsBack() async {
        let source = StubTaskDetailSource()
        source.detail.id = "0007"
        source.detail.status = "todo"
        let model = TaskDetailModel(source: source, project: "proj", taskID: "0007")
        await model.load()
        let gate = AsyncGate()
        source.updateGate = gate
        source.updateError = YccError.rpc(message: "offline")

        let pending = Task { await model.setStatus(.done) }
        await eventually { gate.waiters == 1 }
        XCTAssertEqual(model.status, .done)
        gate.open()
        _ = await pending.value
        XCTAssertEqual(model.status, .todo)
        XCTAssertEqual(model.errorMessage, "offline")
    }
}

// MARK: - Usage

private final class CountingUsageSource: UsageSource, @unchecked Sendable {
    private let lock = NSLock()
    private var _usageCalls = 0
    private var _subscriptionRefreshes: [Bool] = []
    private var _budgetCalls = 0
    private var _projectCalls = 0
    var usageCalls: Int { lock.lock(); defer { lock.unlock() }; return _usageCalls }
    var subscriptionRefreshes: [Bool] { lock.lock(); defer { lock.unlock() }; return _subscriptionRefreshes }
    var budgetCalls: Int { lock.lock(); defer { lock.unlock() }; return _budgetCalls }
    var projectCalls: Int { lock.lock(); defer { lock.unlock() }; return _projectCalls }
    /// Gates consumed by successive GetUsage calls (nil entries pass through).
    var usageGates: [AsyncGate?] = []
    var accounts: [Ycc_V1_SubscriptionUsageAccount] = []

    func getUsage(project: String, groupBy: [String], since: String, until: String)
        async throws -> (rows: [Ycc_V1_UsageRow], total: Ycc_V1_UsageRow, workspace: String)
    {
        let gate: AsyncGate? = {
            lock.lock()
            defer { lock.unlock() }
            _usageCalls += 1
            return usageGates.isEmpty ? nil : usageGates.removeFirst()
        }()
        if let gate { await gate.wait() }
        var row = Ycc_V1_UsageRow()
        row.task = groupBy.first ?? ""
        row.session = project
        return ([row], Ycc_V1_UsageRow(), "/ws")
    }
    func getSubscriptionUsage(refresh: Bool) async throws -> [Ycc_V1_SubscriptionUsageAccount] {
        locked { _subscriptionRefreshes.append(refresh) }
        return accounts
    }
    var budgetGate: AsyncGate?
    func getBudget() async throws -> Ycc_V1_GetBudgetResponse {
        locked { _budgetCalls += 1 }
        if let budgetGate { await budgetGate.wait() }
        return Ycc_V1_GetBudgetResponse()
    }
    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        locked { _projectCalls += 1 }
        return []
    }

    private func locked(_ body: () -> Void) {
        lock.lock()
        defer { lock.unlock() }
        body()
    }
}

@MainActor
final class UsageLoadingTests: XCTestCase {
    func testSubscriptionIsForceRefreshedOnlyOnFirstLoadOrExplicitRefresh() async {
        let cache = AppDataCache()
        let source = CountingUsageSource()
        var account = Ycc_V1_SubscriptionUsageAccount()
        account.provider = "anthropic"
        source.accounts = [account]

        let first = UsageModel(source: source, selectedProject: "p", cache: cache)
        await first.load()
        XCTAssertEqual(source.subscriptionRefreshes, [true])

        let revisit = UsageModel(source: source, selectedProject: "p", cache: cache)
        XCTAssertEqual(revisit.subscriptionAccounts.map(\.provider), ["anthropic"])
        XCTAssertTrue(revisit.hasUsage, "cached rows render before revalidation")
        await revisit.load()
        XCTAssertEqual(source.subscriptionRefreshes, [true], "a revisit reuses the cached allowance")

        await revisit.refresh()
        XCTAssertEqual(source.subscriptionRefreshes, [true, true], "pull-to-refresh forces it")
    }

    func testFilterChangesReloadOnlyUsage() async {
        let source = CountingUsageSource()
        let model = UsageModel(source: source)
        await model.load()
        let budgets = source.budgetCalls
        let subscriptions = source.subscriptionRefreshes.count

        model.grouping = .model
        await model.reloadUsage()

        XCTAssertEqual(source.usageCalls, 2)
        XCTAssertEqual(source.budgetCalls, budgets)
        XCTAssertEqual(source.subscriptionRefreshes.count, subscriptions)
        XCTAssertEqual(model.rows.first?.task, "model")
    }

    func testSupersededUsageLoadCannotOverwriteANewerOne() async {
        let source = CountingUsageSource()
        let slow = AsyncGate()
        source.usageGates = [slow, nil]
        let model = UsageModel(source: source)

        model.grouping = .task
        let older = Task { await model.reloadUsage() }
        await eventually { slow.waiters == 1 }
        model.grouping = .day
        await model.reloadUsage()
        XCTAssertEqual(model.rows.first?.task, "day")

        slow.open()
        await older.value
        XCTAssertEqual(model.rows.first?.task, "day", "the older response is discarded")
        XCTAssertFalse(model.isLoading)
    }

    func testProjectSwitchMidLoadNeverCachesRowsUnderTheWrongProject() async {
        let cache = AppDataCache()
        let source = CountingUsageSource()
        let budget = AsyncGate()
        source.budgetGate = budget
        let bRows = AsyncGate()
        source.usageGates = [nil, bRows]
        let model = UsageModel(source: source, selectedProject: "A", cache: cache)

        let load = Task { await model.load() }
        await eventually { model.rows.first?.session == "A" && budget.waiters == 1 }
        model.selectedProject = "B"
        let switchLoad = Task { await model.reloadUsage() }
        await eventually { bRows.waiters == 1 }
        budget.open()
        await load.value

        let cachedB = cache.value(.usage("B"), as: UsageModel.Snapshot.self)
        XCTAssertNil(cachedB, "A's rows must not be filed under B")
        let cachedA = cache.value(.usage("A"), as: UsageModel.Snapshot.self)
        XCTAssertEqual(cachedA?.rows.first?.session, "A")

        bRows.open()
        await switchLoad.value
        XCTAssertEqual(cache.value(.usage("B"), as: UsageModel.Snapshot.self)?.rows.first?.session, "B")
    }

    func testCachedProjectsSkipListProjects() async {
        let cache = AppDataCache()
        cache.updateProjects([project("a"), project("b")])
        let source = CountingUsageSource()
        let model = UsageModel(source: source, cache: cache)
        XCTAssertTrue(model.showsProjectFilter)
        await model.load()
        XCTAssertEqual(source.projectCalls, 0)
    }
}

// MARK: - Settings

private final class GatedGlobalSettingsSource: GlobalSettingsSource, @unchecked Sendable {
    var response = Ycc_V1_ListModelsResponse()
    /// Per-call (gate, response) overrides for overlapping-load tests.
    var scriptedLists: [(AsyncGate?, Ycc_V1_ListModelsResponse)] = []
    var thinkingGate: AsyncGate?
    var thinkingError: Error?
    private(set) var listCount = 0
    private(set) var roleCalls = 0
    private(set) var upserts = 0

    func listModels() async throws -> Ycc_V1_ListModelsResponse {
        listCount += 1
        guard !scriptedLists.isEmpty else { return response }
        let (gate, scripted) = scriptedLists.removeFirst()
        if let gate { await gate.wait() }
        return scripted
    }
    func setRoleConfig(sessionId: String, coordinator: String, implementer: String, reviewers: [String]) async throws {
        roleCalls += 1
    }
    func setThinking(sessionId: String, level: String, role: String) async throws {
        if let thinkingGate { await thinkingGate.wait() }
        if let thinkingError { throw thinkingError }
    }
    func getModelConfig(name: String) async throws -> Ycc_V1_ModelConfig { Ycc_V1_ModelConfig() }
    func upsertModel(_ model: Ycc_V1_ModelConfig) async throws { upserts += 1 }
    func removeModel(name: String) async throws {}
    func discoverModels(backend: String, baseURL: String, keyEnv: String) async throws -> Ycc_V1_DiscoverModelsResponse {
        Ycc_V1_DiscoverModelsResponse()
    }
    func testModel(_ model: Ycc_V1_ModelConfig) async throws -> Ycc_V1_TestModelResponse {
        Ycc_V1_TestModelResponse()
    }
}

private func settingsResponse() -> Ycc_V1_ListModelsResponse {
    var a = Ycc_V1_ModelInfo(); a.name = "alpha"
    var b = Ycc_V1_ModelInfo(); b.name = "beta"
    var response = Ycc_V1_ListModelsResponse()
    response.models = [a, b]
    response.coordinator = "alpha"
    response.implementer = "beta"
    response.reviewers = ["alpha"]
    response.coordinatorThinking = "high"
    response.implementerThinking = "low"
    response.reviewersThinking = "medium"
    return response
}

@MainActor
final class SettingsOptimisticTests: XCTestCase {
    func testGlobalThinkingIsOptimisticPerControlAndRevertsOnFailure() async {
        let source = GatedGlobalSettingsSource()
        source.response = settingsResponse()
        let model = GlobalSettingsModel(source: source)
        await model.load()
        let gate = AsyncGate()
        source.thinkingGate = gate
        source.thinkingError = YccError.rpc(message: "config locked")

        let pending = Task { await model.setThinking(.max, for: .reviewers) }
        await eventually { gate.waiters == 1 }
        XCTAssertEqual(model.reviewersThinking, .max, "the picker does not snap back")
        XCTAssertTrue(model.isApplyingThinking(.reviewers))
        XCTAssertFalse(model.isApplyingThinking(.coordinator), "only that control is busy")
        XCTAssertFalse(model.isApplying, "the rest of the form stays enabled")

        gate.open()
        await pending.value
        XCTAssertEqual(model.reviewersThinking, .medium, "reverted to the confirmed level")
        XCTAssertEqual(model.errorMessage, "config locked")
        XCTAssertFalse(model.isApplyingThinking(.reviewers))
    }

    func testGlobalSettingsRevisitRendersTheCachedRegistry() async {
        let cache = AppDataCache()
        let source = GatedGlobalSettingsSource()
        source.response = settingsResponse()
        await GlobalSettingsModel(source: source, cache: cache).load()

        let revisit = GlobalSettingsModel(source: source, cache: cache)
        XCTAssertEqual(revisit.models.map(\.name), ["alpha", "beta"], "no spinner on revisit")
        XCTAssertEqual(revisit.coordinator, "alpha")
        XCTAssertEqual(revisit.coordinatorThinking, .high)
        XCTAssertEqual(source.listCount, 1)
    }

    func testOverlappingRegistryLoadsCannotLandOutOfOrder() async {
        let source = GatedGlobalSettingsSource()
        var old = settingsResponse()
        old.coordinator = "old"
        let slow = AsyncGate()
        source.scriptedLists = [(slow, old), (nil, settingsResponse())]
        let model = GlobalSettingsModel(source: source)

        let first = Task { await model.load() }
        await eventually { slow.waiters == 1 }
        await model.load()
        XCTAssertEqual(model.coordinator, "alpha")
        slow.open()
        await first.value
        XCTAssertEqual(model.coordinator, "alpha", "the superseded response is discarded")
        XCTAssertFalse(model.isLoading)
    }

    func testOverlappingReviewTierLoadsCannotLandOutOfOrder() async {
        let source = ScriptedTiersSource()
        var old = Ycc_V1_ListReviewTiersResponse()
        old.defaultTier = "old"
        var fresh = Ycc_V1_ListReviewTiersResponse()
        fresh.defaultTier = "fresh"
        let slow = AsyncGate()
        source.scripted = [(slow, old), (nil, fresh)]
        let model = ReviewTiersModel(source: source)

        let first = Task { await model.load() }
        await eventually { slow.waiters == 1 }
        await model.load()
        XCTAssertEqual(model.defaultTier, "fresh")
        slow.open()
        await first.value
        XCTAssertEqual(model.defaultTier, "fresh")
        XCTAssertFalse(model.isLoading)
    }

    func testModelSaveReturnsBeforeTheRegistryReload() async {
        let source = GatedGlobalSettingsSource()
        source.response = settingsResponse()
        let model = GlobalSettingsModel(source: source)
        await model.load()
        let lists = source.listCount

        let saved = await model.saveModel(Ycc_V1_ModelConfig())
        XCTAssertTrue(saved)
        XCTAssertEqual(source.listCount, lists, "the editor dismisses on the upsert alone")
        await model.reloadTask?.value
        XCTAssertEqual(source.listCount, lists + 1)
    }

    func testGlobalRolePicksApplyButSeedingNeverDoes() async {
        let source = GatedGlobalSettingsSource()
        source.response = settingsResponse()
        let model = GlobalSettingsModel(source: source)
        await model.load()
        XCTAssertEqual(source.roleCalls, 0)

        XCTAssertNil(model.chooseCoordinator("alpha"), "re-picking the current value is a no-op")
        await model.chooseCoordinator("beta")?.value
        XCTAssertEqual(source.roleCalls, 1)
        XCTAssertEqual(model.coordinator, "beta")
    }

    func testSessionSettingsSeedingNeverSendsSetThinking() async {
        let source = SessionSettingsRecorder()
        var response = settingsResponse()
        response.coordinatorThinking = "high"
        response.implementerThinking = "low"
        source.response = response
        let model = SessionSettingsModel(source: source, sessionId: "s1")

        await model.load()
        XCTAssertEqual(model.thinkingLevel, .high, "seeded from the coordinator")
        model.selectThinkingRole(.implementer)
        model.selectThinkingRole(.all)
        XCTAssertTrue(source.thinkingCalls.isEmpty, "seeding and scope changes never apply")

        XCTAssertNil(model.chooseThinkingLevel(.high), "picking the shown level is a no-op")
        await model.chooseThinkingLevel(.max)?.value
        XCTAssertEqual(source.thinkingCalls.map(\.level), ["max"])
        XCTAssertEqual(source.thinkingCalls.map(\.role), [""])
        XCTAssertEqual(model.implementerThinking, .max)

        await model.chooseImplementer("alpha")?.value
        XCTAssertEqual(source.roleCalls, 1)
    }
}

private final class ScriptedTiersSource: ReviewTiersSource, @unchecked Sendable {
    var scripted: [(AsyncGate?, Ycc_V1_ListReviewTiersResponse)] = []
    func listReviewTiers() async throws -> Ycc_V1_ListReviewTiersResponse {
        let (gate, response) = scripted.removeFirst()
        if let gate { await gate.wait() }
        return response
    }
    func listModels() async throws -> Ycc_V1_ListModelsResponse { Ycc_V1_ListModelsResponse() }
    func upsertReviewTier(_ tier: Ycc_V1_ReviewTierInfo) async throws {}
    func removeReviewTier(name: String) async throws {}
    func setReviewDefault(name: String) async throws {}
}

private final class SessionSettingsRecorder: SessionSettingsSource, @unchecked Sendable {
    var response = Ycc_V1_ListModelsResponse()
    private(set) var thinkingCalls: [(level: String, role: String)] = []
    private(set) var roleCalls = 0

    func listModels(sessionId: String) async throws -> Ycc_V1_ListModelsResponse { response }
    func setRoleConfig(sessionId: String, coordinator: String, implementer: String, reviewers: [String]) async throws {
        roleCalls += 1
    }
    func setThinking(sessionId: String, level: String, role: String) async throws {
        thinkingCalls.append((level, role))
    }
}

// MARK: - New session composer

private final class CatalogSource: NewSessionSource, @unchecked Sendable {
    private(set) var projectCalls = 0
    private(set) var modeCalls = 0
    var projects: [Ycc_V1_ProjectInfo] = []

    func listModes() async throws -> (modes: [Ycc_V1_Mode], presets: [Ycc_V1_Preset]) {
        modeCalls += 1
        var work = Ycc_V1_Mode()
        work.name = "work"
        var chat = Ycc_V1_Mode()
        chat.name = "chat"
        return ([work, chat], [])
    }
    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        projectCalls += 1
        return projects
    }
    func listModels() async throws -> Ycc_V1_ListModelsResponse { settingsResponse() }
    func startSession(
        project: String, mode: String, prompt: String, coordinatorModel: String, images: [MessageImage]
    ) async throws -> String { "s_new" }
    func resumeSession(project: String, sessionId: String) async throws -> String { sessionId }
}

private final class MemoryDefaults: SessionDefaultsStore {
    var lastMode: String?
    var lastProject: String?
}

@MainActor
final class NewSessionCatalogCacheTests: XCTestCase {
    func testReopenedComposerRendersFromTheCachedCatalog() async {
        let cache = AppDataCache()
        let source = CatalogSource()
        source.projects = [project("only")]
        let first = NewSessionModel(source: source, defaults: MemoryDefaults(), cache: cache)
        XCTAssertTrue(first.modes.isEmpty)
        await first.load()
        XCTAssertEqual(source.projectCalls, 1)
        XCTAssertEqual(cache.projects?.map(\.name), ["only"])

        let reopened = NewSessionModel(source: source, defaults: MemoryDefaults(), cache: cache)
        XCTAssertEqual(reopened.modes.map(\.name), ["work", "chat"], "no spinner on reopen")
        XCTAssertEqual(reopened.selectedMode, "work")
        XCTAssertEqual(reopened.selectedProject, "only")
        XCTAssertEqual(reopened.defaultModel, "alpha")
        await reopened.load()
        XCTAssertEqual(source.projectCalls, 1, "projects come from the app-level list")
        XCTAssertEqual(source.modeCalls, 2, "the catalog still revalidates")

        source.projects = [project("only"), project("added")]
        await reopened.load(refreshProjects: true)
        XCTAssertEqual(source.projectCalls, 2)
        XCTAssertEqual(reopened.projects.map(\.name), ["only", "added"])
    }
}

// MARK: - Work loop

private final class LoopSource: WorkLoopSource, @unchecked Sendable {
    private(set) var getCalls = 0
    var loop: Ycc_V1_WorkLoopInfo? = nil

    func getWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo? {
        getCalls += 1
        return loop
    }
    func startWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo {
        var info = Ycc_V1_WorkLoopInfo()
        info.state = "running"
        return info
    }
    func stopWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo? {
        var info = Ycc_V1_WorkLoopInfo()
        info.state = "stopping"
        return info
    }
}

@MainActor
final class WorkLoopFreshnessTests: XCTestCase {
    func testActionResponseMakesTheFollowUpPollRedundant() async {
        var clock = Date(timeIntervalSince1970: 1_000)
        let source = LoopSource()
        let model = WorkLoopModel(source: source, project: "p", now: { clock })

        await model.refreshIfStale()
        XCTAssertEqual(source.getCalls, 1, "an unconfirmed model always loads")
        await model.start()
        XCTAssertEqual(model.state, .running)
        await model.refreshIfStale()
        XCTAssertEqual(source.getCalls, 1, "the Start response already carried the state")

        clock = clock.addingTimeInterval(5)
        await model.refreshIfStale()
        XCTAssertEqual(source.getCalls, 2)
    }

    func testRevisitSeedsTheLastSnapshotButStillLoads() async {
        let cache = AppDataCache()
        let source = LoopSource()
        let first = WorkLoopModel(source: source, project: "p", cache: cache)
        await first.start()

        let revisit = WorkLoopModel(source: source, project: "p", cache: cache)
        XCTAssertEqual(revisit.state, .running, "the banner renders before any RPC")
        await revisit.refreshIfStale()
        XCTAssertEqual(source.getCalls, 1, "a cached snapshot is not daemon-confirmed")
    }
}
