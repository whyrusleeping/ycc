import Connect
import Foundation
import YccProto

/// A thin wrapper over the generated connect-swift `Ycc_V1_SessionServiceClient`
/// that pins a base URL + bearer token and exposes typed async methods.
///
/// All requests carry the bearer token via ``AuthInterceptor`` (unary and
/// streaming alike). The connect protocol + JSON codec are used so traffic is
/// human-readable when debugging against the daemon (docs/design/ios-client.md
/// §4). Later tasks reach the full RPC surface through ``YccClient/generated``.
public final class YccClient: Sendable {
    /// The underlying generated service client, for RPCs not yet wrapped here.
    public let generated: Ycc_V1_SessionServiceClient

    /// - Parameters:
    ///   - baseURL: The daemon base URL, e.g. `http://myhost:8790` (a tailnet
    ///     `http://` address is expected; see the ATS note in project.yml).
    ///   - token: The bearer token; attached to every request.
    public init(baseURL: URL, token: String) {
        // `host` is the scheme+authority the generated paths are appended to.
        // Strip any trailing slash so path joining stays clean.
        var host = baseURL.absoluteString
        while host.hasSuffix("/") {
            host.removeLast()
        }
        let config = ProtocolClientConfig(
            host: host,
            networkProtocol: .connect,
            codec: JSONCodec(),
            interceptors: [AuthInterceptor.factory(token: token)]
        )
        let protocolClient = ProtocolClient(
            httpClient: URLSessionHTTPClient(),
            config: config
        )
        self.generated = Ycc_V1_SessionServiceClient(client: protocolClient)
    }

    /// Lists the daemon's registered projects. Used by the connect screen to
    /// validate credentials: a bad token surfaces as ``YccError/unauthorized``.
    public func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        let response = await generated.listProjects(request: Ycc_V1_ListProjectsRequest())
        switch response.result {
        case .success(let message):
            return message.projects
        case .failure(let error):
            throw Self.map(error)
        }
    }

    /// Lists the daemon's session history — live and persisted, most-recent
    /// first per the daemon (docs/remote-api.md "ListSessionHistory"). `project`
    /// is optional: empty selects the daemon default workspace; a registered
    /// project name filters to that workspace's sessions.
    public func listSessionHistory(project: String = "") async throws -> [Ycc_V1_SessionSummary] {
        var request = Ycc_V1_ListSessionHistoryRequest()
        request.project = project
        let response = await generated.listSessionHistory(request: request)
        switch response.result {
        case .success(let message):
            return message.sessions
        case .failure(let error):
            throw Self.map(error)
        }
    }

    /// Fetch a session's full event log for a read-only replayed transcript
    /// (no stream held open). `project` is optional for a single-project daemon.
    public func getSessionTranscript(
        project: String = "", sessionId: String
    ) async throws -> [Ycc_V1_Event] {
        var request = Ycc_V1_GetSessionTranscriptRequest()
        request.project = project
        request.sessionID = sessionId
        let response = await generated.getSessionTranscript(request: request)
        switch response.result {
        case .success(let message):
            return message.events
        case .failure(let error):
            throw Self.map(error)
        }
    }

    /// Subscribe to a session's live event stream. The server replays persisted
    /// events with `seq > fromSeq` then tails live events (docs/remote-api.md
    /// "Subscribe"). A fresh subscriber passes `fromSeq: 0`; a reconnecting one
    /// passes its last **persisted** seq so only newer events replay — no gap,
    /// no duplication.
    ///
    /// The returned stream finishes when the RPC completes cleanly and throws a
    /// mapped ``YccError`` on stream error. Cancelling the consuming task (or
    /// deiniting its `for await`) cancels the underlying Connect stream.
    public func subscribe(
        sessionId: String, fromSeq: Int64
    ) -> AsyncThrowingStream<Ycc_V1_Event, Error> {
        let stream = generated.subscribe()
        return AsyncThrowingStream { continuation in
            let task = Task {
                var request = Ycc_V1_SubscribeRequest()
                request.sessionID = sessionId
                request.fromSeq = fromSeq
                do {
                    try stream.send(request)
                } catch {
                    continuation.finish(throwing: error)
                    return
                }
                for await result in stream.results() {
                    switch result {
                    case .headers:
                        continue
                    case .message(let event):
                        continuation.yield(event)
                    case .complete(_, let error, _):
                        if let error {
                            continuation.finish(throwing: Self.mapAny(error))
                        } else {
                            continuation.finish()
                        }
                        return
                    }
                }
                continuation.finish()
            }
            continuation.onTermination = { _ in
                task.cancel()
                stream.cancel()
            }
        }
    }

    // MARK: - Session interactions (task 0183)

    /// Deliver user input to a running/idle session (`SendInput`). Steer-by-default:
    /// the daemon queues the text as a steer when the session is mid-turn.
    public func sendInput(sessionId: String, text: String) async throws {
        var request = Ycc_V1_SendInputRequest()
        request.sessionID = sessionId
        request.text = text
        try unary(await generated.sendInput(request: request))
    }

    /// Answer a single pending `ask_user` question (`AnswerQuestion`).
    /// `optionIndex >= 0` selects that suggested option; `-1` sends `text` as a
    /// free-text answer.
    public func answerQuestion(sessionId: String, text: String, optionIndex: Int) async throws {
        var request = Ycc_V1_AnswerQuestionRequest()
        request.sessionID = sessionId
        request.text = text
        request.optionIndex = Int32(optionIndex)
        try unary(await generated.answerQuestion(request: request))
    }

    /// Answer a batch `ask_user` positionally (`AnswerQuestions`): `answers[i]`
    /// answers the i-th question. Each answer's `optionIndex >= 0` selects an
    /// option; `-1` sends its `text` as free text.
    public func answerQuestions(
        sessionId: String, answers: [(text: String, optionIndex: Int)]
    ) async throws {
        var request = Ycc_V1_AnswerQuestionsRequest()
        request.sessionID = sessionId
        request.answers = answers.map {
            var a = Ycc_V1_QuestionAnswer()
            a.text = $0.text
            a.optionIndex = Int32($0.optionIndex)
            return a
        }
        try unary(await generated.answerQuestions(request: request))
    }

    /// Gracefully pause a running session to steer it (`Interrupt`, spec §18.7).
    public func interrupt(sessionId: String) async throws {
        var request = Ycc_V1_InterruptRequest()
        request.sessionID = sessionId
        try unary(await generated.interrupt(request: request))
    }

    /// Continue a paused session (`Resume`).
    public func resume(sessionId: String) async throws {
        var request = Ycc_V1_ResumeRequest()
        request.sessionID = sessionId
        try unary(await generated.resume(request: request))
    }

    /// Hard-terminate a session (`StopSession`, spec §12) — no resume.
    public func stopSession(sessionId: String) async throws {
        var request = Ycc_V1_StopSessionRequest()
        request.sessionID = sessionId
        try unary(await generated.stopSession(request: request))
    }

    /// Discard a unary response's payload, mapping any failure to ``YccError``.
    private func unary<M>(_ response: ResponseMessage<M>) throws {
        if case .failure(let error) = response.result {
            throw Self.map(error)
        }
    }

    /// Maps a connect-swift `ConnectError` into the UI-facing ``YccError``.
    static func map(_ error: ConnectError) -> YccError {
        switch error.code {
        case .unauthenticated, .permissionDenied:
            return .unauthorized
        case .notFound:
            return .notFound(message: error.message ?? "not found")
        case .failedPrecondition:
            return .failedPrecondition(message: error.message ?? "precondition failed")
        default:
            return .rpc(message: error.message ?? "request failed (\(error.code))")
        }
    }

    /// Maps an arbitrary stream error (usually a `ConnectError`) to ``YccError``.
    static func mapAny(_ error: Error) -> YccError {
        if let connect = error as? ConnectError {
            return map(connect)
        }
        return .rpc(message: error.localizedDescription)
    }
}
