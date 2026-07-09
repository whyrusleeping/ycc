import Foundation
import YccProto

/// The transcript data source a ``SessionViewModel`` reads from. Abstracting it
/// behind a protocol lets the reconnect / fold logic be unit-tested headlessly
/// with an in-memory stub — no network, no simulator. ``YccClient`` is the
/// production conformer.
public protocol SessionTranscriptSource: Sendable {
    /// Fetch a session's full event log for a read-only replayed transcript.
    func getSessionTranscript(project: String, sessionId: String) async throws -> [Ycc_V1_Event]

    /// Subscribe to a session's live event stream, replaying `seq > fromSeq`.
    func subscribe(sessionId: String, fromSeq: Int64) -> AsyncThrowingStream<Ycc_V1_Event, Error>
}

extension YccClient: SessionTranscriptSource {}

/// The interactive-action surface a ``SessionViewModel`` drives from the UI
/// (input bar, answer sheets, interrupt/resume/stop). Split from the read-only
/// ``SessionTranscriptSource`` so the action view-model logic can be unit-tested
/// with an in-memory fake — no network, no simulator. ``YccClient`` is the
/// production conformer.
public protocol SessionActionSource: Sendable {
    /// Deliver user input (`SendInput`); steer-by-default when mid-turn.
    func sendInput(sessionId: String, text: String) async throws

    /// Answer a single pending question (`AnswerQuestion`); `optionIndex >= 0`
    /// selects an option, `-1` sends `text` as free text.
    func answerQuestion(sessionId: String, text: String, optionIndex: Int) async throws

    /// Answer a batch positionally (`AnswerQuestions`): `answers[i]` answers the
    /// i-th question.
    func answerQuestions(sessionId: String, answers: [(text: String, optionIndex: Int)]) async throws

    /// Gracefully pause a running session to steer (`Interrupt`).
    func interrupt(sessionId: String) async throws
    /// Continue a paused session (`Resume`).
    func resume(sessionId: String) async throws
    /// Hard-terminate a session (`StopSession`).
    func stopSession(sessionId: String) async throws
}

extension YccClient: SessionActionSource {}
