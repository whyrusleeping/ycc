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
}
