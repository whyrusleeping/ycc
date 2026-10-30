import Foundation
import XCTest
import YccProto
@testable import YccKit

private func check(_ id: String, _ status: String) -> Ycc_V1_HealthCheck {
    var c = Ycc_V1_HealthCheck()
    c.id = id
    c.label = id
    c.status = status
    return c
}

private final class MockHealthSource: HealthSource, @unchecked Sendable {
    var passive: [Ycc_V1_HealthCheck] = []
    var rechecked: [Ycc_V1_HealthCheck] = []
    var error: Error?
    /// When set, passive reads park here until the test opens it.
    var passiveGate: AsyncGate?

    func getHealth(refresh: Bool) async throws -> [Ycc_V1_HealthCheck] {
        if let error { throw error }
        if refresh { return rechecked }
        await passiveGate?.wait()
        return passive
    }
}

@MainActor
final class HealthModelTests: XCTestCase {
    func testAlertingListsFailuresBeforeWarningsAndIgnoresQuietStatuses() {
        let checks = [
            check("a", "ok"), check("b", "warn"), check("c", "pending"),
            check("d", "fail"), check("e", "off"), check("f", "warn"),
        ]
        XCTAssertEqual(HealthModel.alerting(checks).map(\.id), ["d", "b", "f"])
    }

    /// An older daemon answers `unimplemented`: no banner, no error.
    func testFailedPassiveLoadSurfacesNothing() async {
        let source = MockHealthSource()
        source.error = YccError.rpc(message: "unimplemented")
        let model = HealthModel(source: source)

        await model.refresh()

        XCTAssertTrue(model.checks.isEmpty)
        XCTAssertNil(model.checkError)
    }

    /// A passive read started before Check now must not overwrite the
    /// re-check's fresher results when it lands afterwards.
    func testStalePassiveReadDoesNotReplaceRecheck() async {
        let source = MockHealthSource()
        let gate = AsyncGate()
        source.passiveGate = gate
        source.passive = [check("exa", "fail")]
        source.rechecked = [check("exa", "ok")]
        let model = HealthModel(source: source)

        let passive = Task { await model.refresh() }
        await eventually { gate.waiters == 1 }
        await model.checkNow()
        gate.open()
        await passive.value

        XCTAssertEqual(model.checks.map(\.status), ["ok"])
    }
}
