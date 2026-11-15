import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``SessionSettingsSource`` for headless model tests.
/// Records the last request args per RPC so mapping is testable, and can be
/// primed to throw so error/unauthorized surfacing is exercised.
private final class MockSettingsSource: SessionSettingsSource, @unchecked Sendable {
    var response = Ycc_V1_ListModelsResponse()
    var listError: Error?
    var setError: Error?

    private(set) var listSessionId: String?
    private(set) var roleArgs: (sessionId: String, coordinator: String, implementer: String, reviewers: [String])?
    private(set) var thinkingArgs: (sessionId: String, level: String, role: String)?

    func listModels(sessionId: String) async throws -> Ycc_V1_ListModelsResponse {
        listSessionId = sessionId
        if let listError { throw listError }
        return response
    }

    func setRoleConfig(
        sessionId: String, coordinator: String, implementer: String, reviewers: [String]
    ) async throws {
        roleArgs = (sessionId, coordinator, implementer, reviewers)
        if let setError { throw setError }
    }

    func setThinking(sessionId: String, level: String, role: String) async throws {
        thinkingArgs = (sessionId, level, role)
        if let setError { throw setError }
    }
}

private func modelInfo(_ name: String, backend: String = "anthropic") -> Ycc_V1_ModelInfo {
    var m = Ycc_V1_ModelInfo()
    m.name = name
    m.backend = backend
    return m
}

private func listModelsResponse(
    models: [String] = ["claude", "gpt", "glm"],
    coordinator: String = "claude",
    implementer: String = "gpt",
    reviewers: [String] = ["glm"],
    coordinatorThinking: String = "high",
    implementerThinking: String = "medium",
    reviewersThinking: String = "low"
) -> Ycc_V1_ListModelsResponse {
    var r = Ycc_V1_ListModelsResponse()
    r.models = models.map { modelInfo($0) }
    r.coordinator = coordinator
    r.implementer = implementer
    r.reviewers = reviewers
    r.coordinatorThinking = coordinatorThinking
    r.implementerThinking = implementerThinking
    r.reviewersThinking = reviewersThinking
    return r
}

@MainActor
final class SessionSettingsModelTests: XCTestCase {
    // MARK: - Seeding

    func testLoadSeedsPickersFromListModels() async {
        let source = MockSettingsSource()
        source.response = listModelsResponse()
        let model = SessionSettingsModel(source: source, sessionId: "s1")

        await model.load()

        XCTAssertEqual(source.listSessionId, "s1")
        XCTAssertEqual(model.models.map(\.name), ["claude", "gpt", "glm"])
        XCTAssertEqual(model.coordinator, "claude")
        XCTAssertEqual(model.implementer, "gpt")
        XCTAssertEqual(model.reviewers, ["glm"])
        XCTAssertEqual(model.coordinatorThinking, .high)
        XCTAssertEqual(model.implementerThinking, .medium)
        XCTAssertEqual(model.reviewersThinking, .low)
        XCTAssertNil(model.errorMessage)
        XCTAssertFalse(model.unauthorized)
    }

    func testThinkingLevelParsesUnknownToMedium() {
        XCTAssertEqual(ThinkingLevel.parse(""), .medium)
        XCTAssertEqual(ThinkingLevel.parse("XHIGH"), .xhigh)
        XCTAssertEqual(ThinkingLevel.parse("bogus"), .medium)
    }

    // MARK: - Role config

    func testApplyRoleConfigSendsFullSelection() async {
        let source = MockSettingsSource()
        source.response = listModelsResponse()
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        await model.load()

        model.coordinator = "gpt"
        model.toggleReviewer("claude") // add
        await model.applyRoleConfig()

        XCTAssertEqual(source.roleArgs?.sessionId, "s1")
        XCTAssertEqual(source.roleArgs?.coordinator, "gpt")
        XCTAssertEqual(source.roleArgs?.implementer, "gpt")
        XCTAssertEqual(source.roleArgs?.reviewers, ["glm", "claude"])
    }

    func testToggleReviewerAddsAndRemoves() async {
        let source = MockSettingsSource()
        source.response = listModelsResponse(reviewers: ["glm"])
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        await model.load()

        XCTAssertTrue(model.isReviewerSelected("glm"))
        model.toggleReviewer("glm") // remove
        XCTAssertFalse(model.isReviewerSelected("glm"))
        model.toggleReviewer("gpt") // add
        XCTAssertEqual(model.reviewers, ["gpt"])
    }

    func testApplyRoleConfigRevertsOnFailure() async {
        let source = MockSettingsSource()
        source.response = listModelsResponse()
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        await model.load()
        source.setError = YccError.rpc(message: "unknown model \"nope\"")

        model.coordinator = "nope"
        await model.applyRoleConfig()

        XCTAssertEqual(model.coordinator, "claude") // reverted
        XCTAssertEqual(model.errorMessage, "unknown model \"nope\"")
    }

    // MARK: - Thinking

    func testChooseThinkingAppliesToTheRoleInThisSession() async {
        let source = MockSettingsSource()
        source.response = listModelsResponse()
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        await model.load()
        // The daemon reports the role's level after the change.
        source.response.implementerThinking = "max"

        await model.chooseThinking(.max, for: .implementer)?.value

        XCTAssertEqual(source.thinkingArgs?.sessionId, "s1")
        XCTAssertEqual(source.thinkingArgs?.level, "max")
        XCTAssertEqual(source.thinkingArgs?.role, "implementer")
        XCTAssertEqual(model.implementerThinking, .max)
        XCTAssertEqual(model.coordinatorThinking, .high)
    }

    func testChooseThinkingRefreshesRolesSharingTheModel() async {
        let source = MockSettingsSource()
        source.response = listModelsResponse(implementer: "claude", implementerThinking: "high")
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        await model.load()
        // Coordinator and implementer share "claude": the level reaches both.
        source.response.coordinatorThinking = "low"
        source.response.implementerThinking = "low"

        await model.chooseThinking(.low, for: .coordinator)?.value

        XCTAssertEqual(model.coordinatorThinking, .low)
        XCTAssertEqual(model.implementerThinking, .low)
    }

    func testChooseSameThinkingIsANoOp() async {
        let source = MockSettingsSource()
        source.response = listModelsResponse()
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        await model.load()

        XCTAssertNil(model.chooseThinking(.high, for: .coordinator))
        XCTAssertNil(source.thinkingArgs)
    }

    func testChooseThinkingRevertsOnFailure() async {
        let source = MockSettingsSource()
        source.response = listModelsResponse()
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        await model.load()
        source.setError = YccError.rpc(message: "no")

        let task = model.chooseThinking(.off, for: .reviewers)
        XCTAssertEqual(model.reviewersThinking, .off, "the picker shows the pick at once")
        await task?.value

        XCTAssertEqual(model.reviewersThinking, .low)
        XCTAssertEqual(model.errorMessage, "no")
    }

    func testThinkingLevelsFollowEachRolesModel() async {
        let source = MockSettingsSource()
        var response = listModelsResponse(reviewers: ["gpt", "glm"])
        response.models[1].thinkingLevels = ["low", "medium", "high", "xhigh"]
        response.models[2].thinkingLevels = ["low", "medium", "high"]
        source.response = response
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        await model.load()

        XCTAssertEqual(model.thinkingLevels(for: .coordinator), ThinkingLevel.allCases)
        XCTAssertEqual(model.thinkingLevels(for: .implementer), [.low, .medium, .high, .xhigh])
        XCTAssertEqual(model.thinkingLevels(for: .reviewers), [.low, .medium, .high, .xhigh])
    }

    func testRoleChangeRefreshesThinking() async {
        let source = MockSettingsSource()
        source.response = listModelsResponse()
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        await model.load()
        source.response.coordinatorThinking = "medium" // gpt's level

        await model.chooseCoordinator("gpt")?.value

        XCTAssertEqual(model.coordinatorThinking, .medium)
    }

    // MARK: - Error / unauthorized surfacing

    func testLoadUnauthorizedSetsFlag() async {
        let source = MockSettingsSource()
        source.listError = YccError.unauthorized
        let model = SessionSettingsModel(source: source, sessionId: "s1")

        await model.load()

        XCTAssertTrue(model.unauthorized)
    }

    func testApplyUnauthorizedSetsFlag() async {
        let source = MockSettingsSource()
        source.setError = YccError.unauthorized
        let model = SessionSettingsModel(source: source, sessionId: "s1")

        await model.applyThinking(.high, for: .coordinator)

        XCTAssertTrue(model.unauthorized)
    }
}
