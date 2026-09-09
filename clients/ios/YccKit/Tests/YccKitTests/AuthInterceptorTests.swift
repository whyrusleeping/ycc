import Connect
import Foundation
import XCTest
@testable import YccKit

final class AuthInterceptorTests: XCTestCase {
    private func unaryRequest() -> HTTPRequest<Data?> {
        HTTPRequest(
            url: URL(string: "http://host:8790/ycc.v1.SessionService/ListProjects")!,
            headers: ["Content-Type": ["application/json"]],
            message: Data() as Data?,
            method: .post,
            trailers: nil,
            idempotencyLevel: .unknown
        )
    }

    func testUnaryRequestGetsBearerHeader() {
        let interceptor = AuthInterceptor(token: "abc123")
        let exp = expectation(description: "proceed called")
        interceptor.handleUnaryRequest(unaryRequest()) { result in
            guard case .success(let req) = result else {
                return XCTFail("interceptor failed the request")
            }
            XCTAssertEqual(req.headers["Authorization"], ["Bearer abc123"])
            // Existing headers are preserved.
            XCTAssertEqual(req.headers["Content-Type"], ["application/json"])
            exp.fulfill()
        }
        wait(for: [exp], timeout: 1)
    }

    func testStreamStartGetsBearerHeader() {
        let interceptor = AuthInterceptor(token: "streamtoken")
        let request = HTTPRequest<Void>(
            url: URL(string: "http://host:8790/ycc.v1.SessionService/Subscribe")!,
            headers: [:],
            message: (),
            method: .post,
            trailers: nil,
            idempotencyLevel: .unknown
        )
        let exp = expectation(description: "proceed called")
        interceptor.handleStreamStart(request) { result in
            guard case .success(let req) = result else {
                return XCTFail("interceptor failed the stream start")
            }
            XCTAssertEqual(req.headers["Authorization"], ["Bearer streamtoken"])
            exp.fulfill()
        }
        wait(for: [exp], timeout: 1)
    }

    func testErrorMappingUnauthenticated() {
        XCTAssertEqual(
            YccClient.map(ConnectError(code: .unauthenticated, message: "nope")),
            .unauthorized)
        XCTAssertEqual(
            YccClient.map(ConnectError(code: .permissionDenied, message: "nope")),
            .unauthorized)
    }

    func testErrorMappingOther() {
        XCTAssertEqual(
            YccClient.map(ConnectError(code: .unavailable, message: "down")),
            .rpc(message: "down"))
    }

    func testSessionTransportMappingPreservesTransientCancellationAndTerminalCodes() {
        XCTAssertEqual(
            YccClient.mapSessionTransport(
                ConnectError(code: .unavailable, message: "down")) as? YccError,
            YccError.rpc(message: "down"))

        XCTAssertTrue(
            YccClient.mapSessionTransport(
                ConnectError(code: .canceled, message: "cancelled")) is CancellationError)

        XCTAssertEqual(
            YccClient.mapSessionTransport(
                ConnectError(code: .invalidArgument, message: "bad cursor"))
                as? TerminalSessionTransportError,
            TerminalSessionTransportError.terminal(message: "bad cursor"))
        XCTAssertEqual(
            YccClient.mapSessionTransport(
                ConnectError(code: .unimplemented, message: "unsupported"))
                as? TerminalSessionTransportError,
            TerminalSessionTransportError.terminal(message: "unsupported"))
    }
}
