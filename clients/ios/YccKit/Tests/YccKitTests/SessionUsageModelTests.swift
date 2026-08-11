import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``SessionUsageSource`` for headless model tests.
/// Records the last GetUsage args so the request round-trip is testable.
private final class MockSessionUsageSource: SessionUsageSource, @unchecked Sendable {
    var rows: [Ycc_V1_UsageRow] = []
    var total = Ycc_V1_UsageRow()
    var workspace = "/home/me/work"
    var usageError: Error?

    private(set) var usageArgs: (project: String, groupBy: [String], since: String, until: String)?

    func getUsage(project: String, groupBy: [String], since: String, until: String)
        async throws -> (rows: [Ycc_V1_UsageRow], total: Ycc_V1_UsageRow, workspace: String)
    {
        usageArgs = (project, groupBy, since, until)
        if let usageError { throw usageError }
        return (rows, total, workspace)
    }
}

private func usageRow(
    model: String = "", session: String = "",
    input: Int64 = 0, output: Int64 = 0, cacheRead: Int64 = 0, cacheWrite: Int64 = 0,
    total: Int64 = 0, cost: Double = 0, priceStatus: String = "priced"
) -> Ycc_V1_UsageRow {
    var r = Ycc_V1_UsageRow()
    r.model = model
    r.session = session
    r.input = input
    r.output = output
    r.cacheRead = cacheRead
    r.cacheWrite = cacheWrite
    r.total = total
    r.cost = cost
    r.priceStatus = priceStatus
    return r
}

@MainActor
final class SessionUsageModelTests: XCTestCase {
    // MARK: - Request mapping

    func testRefreshRequestsSessionModelGrouping() async {
        let source = MockSessionUsageSource()
        let model = SessionUsageModel(source: source, project: "proj", sessionID: "sess-1")

        await model.refresh()

        XCTAssertEqual(source.usageArgs?.project, "proj")
        XCTAssertEqual(source.usageArgs?.groupBy, ["session", "model"])
        XCTAssertEqual(source.usageArgs?.since, "")
        XCTAssertEqual(source.usageArgs?.until, "")
    }

    // MARK: - Session filtering + total

    func testRefreshKeepsOnlyThisSessionsRows() async {
        let source = MockSessionUsageSource()
        source.rows = [
            usageRow(model: "claude", session: "sess-1", input: 100, output: 50, total: 150, cost: 0.01),
            usageRow(model: "gpt", session: "sess-2", input: 999, output: 999, total: 1_998, cost: 9),
            usageRow(model: "gpt", session: "sess-1", input: 10, output: 5, total: 15, cost: 0.001),
        ]
        let model = SessionUsageModel(source: source, project: "", sessionID: "sess-1")

        await model.refresh()

        XCTAssertEqual(model.rows.map(\.model), ["claude", "gpt"])
        XCTAssertTrue(model.hasUsage)
        XCTAssertEqual(model.total?.total, 165)
        XCTAssertEqual(model.total?.cost ?? 0, 0.011, accuracy: 1e-9)
        XCTAssertNil(model.errorMessage)
    }

    func testRefreshWithNoMatchingSessionIsEmpty() async {
        let source = MockSessionUsageSource()
        source.rows = [usageRow(model: "claude", session: "other", total: 150, cost: 0.01)]
        let model = SessionUsageModel(source: source, project: "", sessionID: "sess-1")

        await model.refresh()

        XCTAssertFalse(model.hasUsage)
        XCTAssertNil(model.total)
        XCTAssertNil(model.errorMessage)
    }

    // MARK: - Pure aggregation

    func testSessionBreakdownSumsTokenClasses() {
        let (rows, total) = SessionUsageModel.sessionBreakdown(
            rows: [
                usageRow(model: "a", session: "s", input: 1, output: 2, cacheRead: 3, cacheWrite: 4, total: 10),
                usageRow(model: "b", session: "s", input: 10, output: 20, cacheRead: 30, cacheWrite: 40, total: 100),
            ],
            sessionID: "s")

        XCTAssertEqual(rows.count, 2)
        XCTAssertEqual(total?.input, 11)
        XCTAssertEqual(total?.output, 22)
        XCTAssertEqual(total?.cacheRead, 33)
        XCTAssertEqual(total?.cacheWrite, 44)
        XCTAssertEqual(total?.total, 110)
        XCTAssertEqual(total?.session, "s")
    }

    func testSessionBreakdownEmptyForNoRows() {
        let (rows, total) = SessionUsageModel.sessionBreakdown(rows: [], sessionID: "s")
        XCTAssertTrue(rows.isEmpty)
        XCTAssertNil(total)
    }

    func testSessionBreakdownCombinesPriceStatuses() {
        let cases: [([String], String)] = [
            (["priced", ""], "priced"),
            (["unpriced", "unpriced"], "unpriced"),
            (["priced", "unpriced"], "partial"),
            (["partial"], "partial"),
        ]

        for (statuses, expected) in cases {
            let rows = statuses.enumerated().map {
                usageRow(model: "model-\($0.offset)", session: "s", priceStatus: $0.element)
            }
            let (_, total) = SessionUsageModel.sessionBreakdown(rows: rows, sessionID: "s")
            XCTAssertEqual(total?.priceStatus, expected, "statuses: \(statuses)")
        }
    }

    // MARK: - Errors

    func testRefreshSurfacesRpcError() async {
        let source = MockSessionUsageSource()
        source.usageError = YccError.rpc(message: "boom")
        let model = SessionUsageModel(source: source, project: "", sessionID: "s")

        await model.refresh()

        XCTAssertEqual(model.errorMessage, "boom")
        XCTAssertFalse(model.unauthorized)
    }

    func testRefreshSurfacesUnauthorized() async {
        let source = MockSessionUsageSource()
        source.usageError = YccError.unauthorized
        let model = SessionUsageModel(source: source, project: "", sessionID: "s")

        await model.refresh()

        XCTAssertTrue(model.unauthorized)
    }
}
