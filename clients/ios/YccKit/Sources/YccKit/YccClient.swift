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

    /// Maps a connect-swift `ConnectError` into the UI-facing ``YccError``.
    static func map(_ error: ConnectError) -> YccError {
        switch error.code {
        case .unauthenticated, .permissionDenied:
            return .unauthorized
        default:
            return .rpc(message: error.message ?? "request failed (\(error.code))")
        }
    }
}
