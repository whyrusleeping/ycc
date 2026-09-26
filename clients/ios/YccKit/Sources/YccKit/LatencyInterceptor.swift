import Connect
import Foundation
import YccProto

/// Per-request interceptor. Records only the fixed RPC path, timing and status;
/// the UUID-derived ID can be matched with the daemon's /debug/latency entries.
final class LatencyInterceptor: UnaryInterceptor, StreamInterceptor, @unchecked Sendable {
    static let headerName = "Ycc-Request-Id"
    private let diagnostics: LatencyDiagnostics
    private let id = String(UUID().uuidString.prefix(16))
    private let lock = NSLock()
    private var procedure = ""
    private var started = ProcessInfo.processInfo.systemUptime
    private var decodeSpan: LatencyDiagnostics.Span?
    private var firstMessageMS: Double?
    private var messages = 0

    init(diagnostics: LatencyDiagnostics) { self.diagnostics = diagnostics }

    static func factory(diagnostics: LatencyDiagnostics) -> InterceptorFactory {
        InterceptorFactory { _ in LatencyInterceptor(diagnostics: diagnostics) }
    }

    private func elapsed() -> Double { (ProcessInfo.processInfo.systemUptime - started) * 1000 }

    @Sendable
    func handleUnaryRequest<Message>(
        _ request: HTTPRequest<Message>,
        proceed: @escaping @Sendable (Result<HTTPRequest<Message>, ConnectError>) -> Void
    ) {
        lock.lock()
        procedure = request.url.path
        started = ProcessInfo.processInfo.systemUptime
        lock.unlock()
        var headers = request.headers
        headers[Self.headerName] = [id]
        proceed(.success(HTTPRequest(url: request.url, headers: headers, message: request.message,
                                     method: request.method, trailers: request.trailers,
                                     idempotencyLevel: request.idempotencyLevel)))
    }

    @Sendable
    func handleUnaryRawResponse(
        _ response: HTTPResponse, proceed: @escaping @Sendable (HTTPResponse) -> Void
    ) {
        lock.lock()
        let path = procedure
        let ms = elapsed()
        if path.hasSuffix("/GetSessionTranscript") || path.hasSuffix("/GetSessionView") || path.hasSuffix("/GetSessionViewPage") {
            decodeSpan = diagnostics.begin("transcript.decode")
        }
        lock.unlock()
        diagnostics.record(.init(procedure: path, kind: "unary", requestID: id,
                                 roundTripMS: ms,
                                 serverMS: LatencyDiagnostics.serverDuration(Self.header(response.headers, "Server-Timing")),
                                 outcome: String(describing: response.code), firstMessageMS: nil, messages: 0))
        proceed(response)
    }

    @Sendable
    func handleUnaryResponse<Message>(
        _ response: ResponseMessage<Message>,
        proceed: @escaping @Sendable (ResponseMessage<Message>) -> Void
    ) {
        lock.lock()
        let span = decodeSpan
        decodeSpan = nil
        lock.unlock()
        if let span {
            var events = 0
            var rows = 0
            if let transcript = response.message as? Ycc_V1_GetSessionTranscriptResponse {
                events = transcript.events.count
            } else if let view = response.message as? Ycc_V1_GetSessionViewResponse {
                rows = view.rows.count
                events = view.rows.reduce(0) { $0 + $1.events.count }
            } else if let page = response.message as? Ycc_V1_GetSessionViewPageResponse {
                rows = page.rows.count
                events = page.rows.reduce(0) { $0 + $1.events.count }
            }
            span.end(events: events, rows: rows)
        }
        proceed(response)
    }

    @Sendable
    func handleStreamStart(
        _ request: HTTPRequest<Void>,
        proceed: @escaping @Sendable (Result<HTTPRequest<Void>, ConnectError>) -> Void
    ) {
        lock.lock()
        procedure = request.url.path
        started = ProcessInfo.processInfo.systemUptime
        lock.unlock()
        var headers = request.headers
        headers[Self.headerName] = [id]
        proceed(.success(HTTPRequest(url: request.url, headers: headers, message: request.message,
                                     method: request.method, trailers: request.trailers,
                                     idempotencyLevel: request.idempotencyLevel)))
    }

    @Sendable
    func handleStreamRawResult(
        _ result: StreamResult<Data>, proceed: @escaping @Sendable (StreamResult<Data>) -> Void
    ) {
        switch result {
        case .message:
            lock.lock()
            if messages == 0 { firstMessageMS = elapsed() }
            messages += 1
            lock.unlock()
        case .complete(let code, _, _):
            lock.lock()
            let path = procedure
            let ms = elapsed()
            let first = firstMessageMS
            let count = messages
            lock.unlock()
            diagnostics.record(.init(procedure: path, kind: "stream", requestID: id,
                                     roundTripMS: ms, serverMS: nil,
                                     outcome: String(describing: code), firstMessageMS: first, messages: count))
        case .headers: break
        }
        proceed(result)
    }

    static func header(_ headers: Headers, _ name: String) -> String? {
        headers.first(where: { $0.key.caseInsensitiveCompare(name) == .orderedSame })?.value.first
    }
}
