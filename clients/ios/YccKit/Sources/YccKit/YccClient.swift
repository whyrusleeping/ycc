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

    /// Maps a connect-swift `ConnectError` into the UI-facing ``YccError``.
    static func map(_ error: ConnectError) -> YccError {
        switch error.code {
        case .unauthenticated, .permissionDenied:
            return .unauthorized
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
