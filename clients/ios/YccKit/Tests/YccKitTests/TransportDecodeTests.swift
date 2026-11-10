import Connect
import Foundation
import XCTest
import YccProto
@testable import YccKit

private final class EmptyProtoURLProtocol: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let response = HTTPURLResponse(
            url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/proto"]
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        // Empty bytes are a valid empty protobuf response.
        client?.urlProtocol(self, didLoad: Data())
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

/// The response headers arrive, then the connection drops before the body
/// does: what an in-flight request sees when iOS suspends the app.
private final class HeadersThenDropURLProtocol: URLProtocol {
    nonisolated(unsafe) static var compressed = true
    nonisolated(unsafe) static var failure = URLError.Code.networkConnectionLost

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        var headers = ["Content-Type": "application/proto"]
        if Self.compressed { headers["Content-Encoding"] = "gzip" }
        let response = HTTPURLResponse(
            url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: headers
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        if !Self.compressed {
            // A truncated body: a length-delimited field cut short.
            client?.urlProtocol(self, didLoad: Data([0x0a, 0x40, 0x01]))
        }
        client?.urlProtocol(self, didFailWithError: URLError(Self.failure))
    }

    override func stopLoading() {}
}

/// A daemon error: Connect sends the code in a JSON body under an HTTP status
/// that several codes share.
private final class DaemonErrorURLProtocol: URLProtocol {
    nonisolated(unsafe) static var status = 400
    nonisolated(unsafe) static var code = "invalid_argument"

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let response = HTTPURLResponse(
            url: request.url!, statusCode: Self.status, httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": "application/json"]
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(#"{"code":"\#(Self.code)","message":"nope"}"#.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

private struct DecodeProbeCodec: Codec {
    let didDecode: @Sendable () -> Void

    func name() -> String { "proto" }
    func serialize<Input: ProtobufMessage>(message: Input) throws -> Data {
        try ProtoCodec().serialize(message: message)
    }
    func deterministicallySerialize<Input: ProtobufMessage>(message: Input) throws -> Data {
        try ProtoCodec().deterministicallySerialize(message: message)
    }
    func deserialize<Output: ProtobufMessage>(source: Data) throws -> Output {
        // Probe inside Connect's decode, not after awaiting it: the caller's
        // executor says nothing about where the transport parsed the payload.
        XCTAssertFalse(Thread.isMainThread, "large unary payloads must not decode on main")
        didDecode()
        return try ProtoCodec().deserialize(source: source)
    }
}

final class TransportDecodeTests: XCTestCase {
    func testUnaryDecodeRunsOffMain() async throws {
        let decoded = expectation(description: "decoded")
        decoded.assertForOverFulfill = true
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [EmptyProtoURLProtocol.self]
        let transport = RetryGuardHTTPClient(configuration: configuration)
        let client = ProtocolClient(
            httpClient: transport,
            config: ProtocolClientConfig(
                host: "https://transport-test.invalid", networkProtocol: .connect,
                codec: DecodeProbeCodec(didDecode: { decoded.fulfill() })
            )
        )
        let response: ResponseMessage<Ycc_V1_GetSessionTranscriptResponse> = await client.unary(
            path: "/ycc.v1.SessionService/GetSessionTranscript",
            idempotencyLevel: .unknown,
            request: Ycc_V1_GetSessionTranscriptRequest(), headers: [:]
        )
        let message = try response.result.get()
        XCTAssertTrue(message.events.isEmpty)
        await fulfillment(of: [decoded], timeout: 2)
    }

    /// Calls GetSessionView through the app's transport and interceptor.
    private func getSessionView(
        protocolClass: AnyClass, diagnostics: LatencyDiagnostics, analytics: UsageAnalytics
    ) async -> ResponseMessage<Ycc_V1_GetSessionViewResponse> {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [protocolClass]
        let client = ProtocolClient(
            httpClient: RetryGuardHTTPClient(configuration: configuration),
            config: ProtocolClientConfig(
                host: "https://transport-test.invalid", networkProtocol: .connect,
                codec: ProtoCodec(),
                interceptors: [LatencyInterceptor.factory(diagnostics: diagnostics, analytics: analytics)]
            )
        )
        return await client.unary(
            path: "/ycc.v1.SessionService/GetSessionView",
            idempotencyLevel: .unknown,
            request: Ycc_V1_GetSessionViewRequest(), headers: [:]
        )
    }

    /// A connection lost after the response headers is a transport failure.
    /// connect-swift used to read it as a complete 200 response and report
    /// `internal` ("unexpected encoding" with gzip, a decode failure without),
    /// which iOS analytics then counted as a daemon error.
    func testBodyLostAfterHeadersIsATransportFailure() async throws {
        let cases: [(compressed: Bool, failure: URLError.Code, code: Code)] = [
            (true, .networkConnectionLost, .unavailable),
            (false, .networkConnectionLost, .unavailable),
            (true, .timedOut, .deadlineExceeded),
            (true, .cancelled, .canceled),
        ]
        for testCase in cases {
            HeadersThenDropURLProtocol.compressed = testCase.compressed
            HeadersThenDropURLProtocol.failure = testCase.failure
            let diagnostics = LatencyDiagnostics()
            let analytics = UsageAnalytics(clientVersion: "test")
            let response = await getSessionView(
                protocolClass: HeadersThenDropURLProtocol.self, diagnostics: diagnostics,
                analytics: analytics)
            let label = "\(testCase)"
            XCTAssertEqual(response.code, testCase.code, label)
            XCTAssertEqual(response.error?.code, testCase.code, label)
            XCTAssertEqual(diagnostics.snapshot().requests.map(\.outcome),
                           [String(describing: testCase.code)], label)
            // Cancellations are the client's own doing and not recorded.
            let recorded = testCase.code == .canceled ? [] : [testCase.code.name]
            XCTAssertEqual(analytics.pendingEvents.map { $0.attrs["code"] ?? "" }, recorded, label)
        }
    }

    /// Usage analytics records a daemon error's own code, not the one its
    /// HTTP status maps to.
    func testDaemonErrorCodeReachesAnalytics() async {
        for (status, code) in [(400, "invalid_argument"), (404, "not_found"), (500, "internal")] {
            DaemonErrorURLProtocol.status = status
            DaemonErrorURLProtocol.code = code
            let analytics = UsageAnalytics(clientVersion: "test")
            let response = await getSessionView(
                protocolClass: DaemonErrorURLProtocol.self, diagnostics: LatencyDiagnostics(),
                analytics: analytics)
            XCTAssertEqual(response.error?.code.name, code)
            XCTAssertEqual(analytics.pendingEvents.map(\.name), ["rpc.GetSessionView"], code)
            XCTAssertEqual(analytics.pendingEvents.map { $0.attrs["code"] ?? "" }, [code])
        }
    }
}
