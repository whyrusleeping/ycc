import Connect
import Foundation

/// A connect-swift interceptor that attaches `Authorization: Bearer <token>` to
/// every outbound request — unary **and** streaming — mirroring the TUI/web
/// clients (docs/design/ios-client.md §4, docs/remote-api.md bearer auth).
///
/// It is instantiated once per request/stream by connect-swift via an
/// ``Connect/InterceptorFactory``; ``AuthInterceptor/factory(token:)`` builds
/// that factory. The interceptor only mutates the outbound request headers, so
/// it implements the request-side hooks of both `UnaryInterceptor` and
/// `StreamInterceptor` and leaves every response hook at its default pass-through.
final class AuthInterceptor: UnaryInterceptor, StreamInterceptor {
    private let token: String

    init(token: String) {
        self.token = token
    }

    /// Builds the factory to hand to `ProtocolClientConfig(interceptors:)`.
    static func factory(token: String) -> InterceptorFactory {
        InterceptorFactory { _ in AuthInterceptor(token: token) }
    }

    /// The header name/value pair this interceptor injects. Exposed for testing.
    static let headerName = "Authorization"
    var headerValue: String { "Bearer \(token)" }

    // MARK: - UnaryInterceptor

    @Sendable
    func handleUnaryRequest<Message>(
        _ request: HTTPRequest<Message>,
        proceed: @escaping @Sendable (Result<HTTPRequest<Message>, ConnectError>) -> Void
    ) {
        var headers = request.headers
        headers[Self.headerName] = [headerValue]
        proceed(.success(HTTPRequest(
            url: request.url,
            headers: headers,
            message: request.message,
            method: request.method,
            trailers: request.trailers,
            idempotencyLevel: request.idempotencyLevel
        )))
    }

    // MARK: - StreamInterceptor

    @Sendable
    func handleStreamStart(
        _ request: HTTPRequest<Void>,
        proceed: @escaping @Sendable (Result<HTTPRequest<Void>, ConnectError>) -> Void
    ) {
        var headers = request.headers
        headers[Self.headerName] = [headerValue]
        proceed(.success(HTTPRequest(
            url: request.url,
            headers: headers,
            message: request.message,
            method: request.method,
            trailers: request.trailers,
            idempotencyLevel: request.idempotencyLevel
        )))
    }
}
