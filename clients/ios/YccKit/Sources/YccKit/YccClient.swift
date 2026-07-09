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

    // MARK: - Starting & resuming sessions (task 0185)

    /// Lists the daemon's session modes and presets (`ListModes`, spec §9).
    /// Modes carry a `name`/`title`/`description`; presets are home-menu entries
    /// that start a `mode` with a tailored `opening_prompt`. Drives the new-session
    /// mode/preset pickers (docs/design/ios-client.md §6 phase 2 step 5).
    public func listModes() async throws -> (modes: [Ycc_V1_Mode], presets: [Ycc_V1_Preset]) {
        let response = await generated.listModes(request: Ycc_V1_ListModesRequest())
        switch response.result {
        case .success(let message):
            return (message.modes, message.presets)
        case .failure(let error):
            throw Self.map(error)
        }
    }

    /// Start a new session (`StartSession`, docs/remote-api.md "StartSession").
    /// `project` is an optional registered project name (empty => daemon default
    /// workspace); `interactionLevel` is `interactive` | `judgement` |
    /// `autonomous`. Returns the new session id to `Subscribe` from seq 0.
    public func startSession(
        project: String = "", mode: String, prompt: String, interactionLevel: String
    ) async throws -> String {
        var request = Ycc_V1_StartSessionRequest()
        request.project = project
        request.mode = mode
        request.prompt = prompt
        request.interactionLevel = interactionLevel
        let response = await generated.startSession(request: request)
        switch response.result {
        case .success(let message):
            return message.sessionID
        case .failure(let error):
            throw Self.map(error)
        }
    }

    /// Re-open a persisted session on its existing event log (`ResumeSession`,
    /// spec §4.5/§18.6). Idempotent if the session is already live. `project` is
    /// optional for a single-project daemon. Returns the session id, which is
    /// stable across the resume so the caller can `Subscribe` to the same log.
    public func resumeSession(project: String = "", sessionId: String) async throws -> String {
        var request = Ycc_V1_ResumeSessionRequest()
        request.project = project
        request.sessionID = sessionId
        let response = await generated.resumeSession(request: request)
        switch response.result {
        case .success(let message):
            return message.sessionID
        case .failure(let error):
            throw Self.map(error)
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

    // MARK: - Backlog browser (task 0184)

    /// List the backlog's summary rows (`ListBacklog`, spec §18.5). `project` is
    /// optional: empty selects the daemon default workspace; a registered project
    /// name filters to that workspace's backlog. Each row carries readiness
    /// (`ready`/`blockedBy`) derived from dependency status.
    public func listBacklog(project: String = "") async throws -> [Ycc_V1_BacklogTaskSummary] {
        var request = Ycc_V1_ListBacklogRequest()
        request.project = project
        let response = await generated.listBacklog(request: request)
        switch response.result {
        case .success(let message):
            return message.tasks
        case .failure(let error):
            throw Self.map(error)
        }
    }

    /// Fetch one task's full detail (`GetTask`, spec §18.5): frontmatter fields
    /// plus the markdown `body`. `project` is optional for a single-project daemon.
    public func getTask(project: String = "", id: String) async throws -> Ycc_V1_TaskDetail {
        var request = Ycc_V1_GetTaskRequest()
        request.project = project
        request.id = id
        let response = await generated.getTask(request: request)
        switch response.result {
        case .success(let message):
            return message.task
        case .failure(let error):
            throw Self.map(error)
        }
    }

    /// Change a task's status (`UpdateTask` with only the optional `status` field
    /// set, spec §18.5). Other fields are left untouched. Returns the refreshed
    /// task detail from the daemon's response.
    public func updateTaskStatus(
        project: String = "", id: String, status: String
    ) async throws -> Ycc_V1_TaskDetail {
        var request = Ycc_V1_UpdateTaskRequest()
        request.project = project
        request.id = id
        request.status = status
        let response = await generated.updateTask(request: request)
        switch response.result {
        case .success(let message):
            return message.task
        case .failure(let error):
            throw Self.map(error)
        }
    }

    /// Add a new task to the backlog (`CreateTask`, task 0143): title plus an
    /// optional markdown body (scaffolded server-side). `project` is optional for
    /// a single-project daemon. Returns the created task's detail.
    public func createTask(
        project: String = "", title: String, body: String
    ) async throws -> Ycc_V1_TaskDetail {
        var request = Ycc_V1_CreateTaskRequest()
        request.project = project
        request.title = title
        request.body = body
        let response = await generated.createTask(request: request)
        switch response.result {
        case .success(let message):
            return message.task
        case .failure(let error):
            throw Self.map(error)
        }
    }

    // MARK: - Usage & budget (task 0188)

    /// Priced token-usage breakdown, grouped and filtered (`GetUsage`, spec
    /// §20.5). `groupBy` is any of `task` | `model` | `session` | `agent` | `day`
    /// (empty => the daemon's default `task`); `since`/`until` are `YYYY-MM-DD`
    /// inclusive (empty => unbounded). Returns the per-group `rows`, the `total`
    /// row, and the resolved `workspace` path. int64 token counts arrive as
    /// `Int64` through the generated client (JSON string on the wire).
    public func getUsage(
        project: String = "", groupBy: [String], since: String = "", until: String = ""
    ) async throws -> (rows: [Ycc_V1_UsageRow], total: Ycc_V1_UsageRow, workspace: String) {
        var request = Ycc_V1_GetUsageRequest()
        request.project = project
        request.groupBy = groupBy
        request.since = since
        request.until = until
        let response = await generated.getUsage(request: request)
        switch response.result {
        case .success(let message):
            return (message.rows, message.total, message.workspace)
        case .failure(let error):
            throw Self.map(error)
        }
    }

    /// The configured spend-guard caps (`GetBudget`, spec §20.6). Every field is
    /// `0` when unset (unlimited); `sessionCost`/`loopCost` are US dollars and
    /// `sessionTokens`/`loopTokens` count total tokens.
    public func getBudget() async throws -> Ycc_V1_GetBudgetResponse {
        let response = await generated.getBudget(request: Ycc_V1_GetBudgetRequest())
        switch response.result {
        case .success(let message):
            return message
        case .failure(let error):
            throw Self.map(error)
        }
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
