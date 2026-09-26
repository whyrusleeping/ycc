import Connect
import Foundation
import XCTest
@testable import YccKit

final class LatencyDiagnosticsTests: XCTestCase {
    func testBoundedRequestsAndSeparateAggregates() {
        let store = LatencyDiagnostics(capacity: 2)
        for i in 0..<3 {
            store.record(.init(procedure: "ListSessionHistory", kind: "unary", requestID: "id-\(i)",
                               roundTripMS: Double(i + 1), serverMS: 1, outcome: i == 2 ? "unavailable" : "ok",
                               firstMessageMS: nil, messages: 0))
        }
        store.record(.init(procedure: "Subscribe", kind: "stream", requestID: "id-4",
                           roundTripMS: 200, serverMS: nil, outcome: "ok", firstMessageMS: 4, messages: 5))
        for _ in 0..<3 { store.record(.init(name: "home.history", durationMS: 5, events: 0, rows: 100)) }
        let snapshot = store.snapshot()
        XCTAssertEqual(snapshot.requests.map(\.requestID), ["id-2", "id-4"])
        XCTAssertEqual(snapshot.stages.count, 2)
        XCTAssertEqual(store.summaries()["unary ListSessionHistory"]?.errors, 1)
        XCTAssertEqual(store.summaries()["stream Subscribe"]?.p95MS, 200)
        XCTAssertEqual(store.summaries()["stage home.history"]?.count, 2)
    }

    func testSpanEndsOnlyOnce() {
        let store = LatencyDiagnostics()
        let span = store.begin("transcript.firstDisplay")
        DispatchQueue.concurrentPerform(iterations: 16) { _ in span.end(events: 7, rows: 3) }
        span.end(events: 99, rows: 99)
        let stages = store.snapshot().stages
        XCTAssertEqual(stages.count, 1)
        XCTAssertEqual(stages.first?.events, 7)
        XCTAssertEqual(stages.first?.rows, 3)
    }

    func testServerTimingParsesOnlyFiniteAppDuration() {
        XCTAssertEqual(LatencyDiagnostics.serverDuration("db;dur=9, app;dur=12.345, cache;desc=hit"), 12.345)
        XCTAssertNil(LatencyDiagnostics.serverDuration("db;dur=4"))
        XCTAssertNil(LatencyDiagnostics.serverDuration("app;dur=NaN"))
        XCTAssertNil(LatencyDiagnostics.serverDuration("app;dur=-3"))
    }

    func testHeaderLookupIsCaseInsensitive() {
        XCTAssertEqual(LatencyInterceptor.header(["server-timing": ["app;dur=3"]], "Server-Timing"), "app;dur=3")
    }

    func testUnaryAndStreamRequestIDs() {
        let interceptor = LatencyInterceptor(diagnostics: LatencyDiagnostics())
        let url = URL(string: "http://host/ycc.v1.SessionService/ListProjects")!
        interceptor.handleUnaryRequest(HTTPRequest(
            url: url, headers: [:], message: Data() as Data?, method: .post,
            trailers: nil, idempotencyLevel: .unknown
        )) { result in
            guard case .success(let request) = result else { return XCTFail("unary request failed") }
            let id = request.headers[LatencyInterceptor.headerName]?.first ?? ""
            XCTAssertEqual(id.count, 16)
            XCTAssertNotNil(id.range(of: "^[A-Za-z0-9._-]+$", options: .regularExpression))
        }
        interceptor.handleStreamStart(HTTPRequest(
            url: url, headers: [:], message: (), method: .post,
            trailers: nil, idempotencyLevel: .unknown
        )) { result in
            guard case .success(let request) = result else { return XCTFail("stream start failed") }
            XCTAssertEqual(request.headers[LatencyInterceptor.headerName]?.first?.count, 16)
        }
    }
}
