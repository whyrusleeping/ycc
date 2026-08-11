import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``UsageSource`` for headless model tests. Records the
/// last GetUsage args so the request round-trip is testable.
private final class MockUsageSource: UsageSource, @unchecked Sendable {
    var rows: [Ycc_V1_UsageRow] = []
    var total = Ycc_V1_UsageRow()
    var workspace = "/home/me/work"
    var budget = Ycc_V1_GetBudgetResponse()
    var subscriptionAccounts: [Ycc_V1_SubscriptionUsageAccount] = []
    var projects: [Ycc_V1_ProjectInfo] = []
    var usageError: Error?

    private(set) var usageArgs: (project: String, groupBy: [String], since: String, until: String)?
    private(set) var usageCount = 0

    func getUsage(project: String, groupBy: [String], since: String, until: String)
        async throws -> (rows: [Ycc_V1_UsageRow], total: Ycc_V1_UsageRow, workspace: String)
    {
        usageCount += 1
        usageArgs = (project, groupBy, since, until)
        if let usageError { throw usageError }
        return (rows, total, workspace)
    }

    func getSubscriptionUsage(refresh: Bool) async throws -> [Ycc_V1_SubscriptionUsageAccount] {
        subscriptionAccounts
    }

    func getBudget() async throws -> Ycc_V1_GetBudgetResponse {
        budget
    }

    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        projects
    }
}

private func usageRow(
    task: String = "", total: Int64 = 0, cost: Double = 0
) -> Ycc_V1_UsageRow {
    var row = Ycc_V1_UsageRow()
    row.task = task
    row.total = total
    row.cost = cost
    return row
}

@MainActor
final class UsageModelTests: XCTestCase {
    // MARK: - Request mapping

    func testRefreshSendsGroupingAndProject() async {
        let source = MockUsageSource()
        let model = UsageModel(source: source, selectedProject: "proj")
        model.grouping = .model

        await model.refresh()

        XCTAssertEqual(source.usageArgs?.project, "proj")
        XCTAssertEqual(source.usageArgs?.groupBy, ["model"])
        XCTAssertEqual(source.usageArgs?.since, "")
        XCTAssertEqual(source.usageArgs?.until, "")
    }

    func testRefreshKeepsOverallScopeWithOneProject() async {
        let source = MockUsageSource()
        var project = Ycc_V1_ProjectInfo()
        project.name = "only"
        source.projects = [project]
        let model = UsageModel(source: source)

        await model.refresh()

        XCTAssertEqual(source.usageArgs?.project, "")
        XCTAssertEqual(model.selectedProject, "")
    }

    func testDateFilterRoundTripsWhenEnabled() async {
        let source = MockUsageSource()
        let model = UsageModel(source: source)
        model.filterByDate = true
        // Fixed UTC instants so the formatted output is deterministic.
        model.since = Date(timeIntervalSince1970: 1_704_067_200) // 2024-01-01
        model.until = Date(timeIntervalSince1970: 1_706_659_200) // 2024-01-31

        await model.refresh()

        XCTAssertEqual(source.usageArgs?.since, "2024-01-01")
        XCTAssertEqual(source.usageArgs?.until, "2024-01-31")
    }

    func testDateFilterOmittedWhenDisabled() async {
        let source = MockUsageSource()
        let model = UsageModel(source: source)
        model.filterByDate = false
        model.since = Date(timeIntervalSince1970: 1_704_067_200)

        await model.refresh()

        XCTAssertEqual(source.usageArgs?.since, "")
        XCTAssertEqual(source.usageArgs?.until, "")
    }

    // MARK: - Refresh state

    func testRefreshLoadsRowsTotalBudgetAndProjects() async {
        let source = MockUsageSource()
        source.rows = [usageRow(task: "0130", total: 15_400, cost: 0.081)]
        source.total = usageRow(total: 15_400, cost: 0.081)
        source.budget.sessionCost = 5
        source.projects = [ {
            var p = Ycc_V1_ProjectInfo(); p.name = "a"; return p
        }(), {
            var p = Ycc_V1_ProjectInfo(); p.name = "b"; return p
        }() ]
        let model = UsageModel(source: source)

        await model.refresh()

        XCTAssertEqual(model.rows.count, 1)
        XCTAssertTrue(model.hasUsage)
        XCTAssertEqual(model.total?.total, 15_400)
        XCTAssertEqual(model.budget?.sessionCost, 5)
        XCTAssertTrue(model.showsProjectFilter)
        XCTAssertEqual(model.workspace, "/home/me/work")
        XCTAssertNil(model.errorMessage)
    }

    func testEmptyUsageHasNoRows() async {
        let source = MockUsageSource()
        let model = UsageModel(source: source)
        await model.refresh()
        XCTAssertFalse(model.hasUsage)
        XCTAssertNil(model.errorMessage)
    }

    func testRefreshSurfacesRpcError() async {
        let source = MockUsageSource()
        source.usageError = YccError.rpc(message: "boom")
        let model = UsageModel(source: source)
        await model.refresh()
        XCTAssertEqual(model.errorMessage, "boom")
        XCTAssertFalse(model.unauthorized)
    }

    func testRefreshSurfacesUnauthorized() async {
        let source = MockUsageSource()
        source.usageError = YccError.unauthorized
        let model = UsageModel(source: source)
        await model.refresh()
        XCTAssertTrue(model.unauthorized)
    }
}
