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

    private(set) var levelArgs: (sessionId: String, level: String)?
    private(set) var roleArgs: (sessionId: String, coordinator: String, implementer: String, reviewers: [String])?
    private(set) var thinkingArgs: (sessionId: String, level: String, role: String)?

    func listModels() async throws -> Ycc_V1_ListModelsResponse {
        if let listError { throw listError }
        return response
    }

    func setInteractionLevel(sessionId: String, level: String) async throws {
        levelArgs = (sessionId, level)
        if let setError { throw setError }
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
        let model = SessionSettingsModel(
            source: source, sessionId: "s1", currentInteractionLevel: "autonomous")

        await model.load()

        XCTAssertEqual(model.models.map(\.name), ["claude", "gpt", "glm"])
        XCTAssertEqual(model.coordinator, "claude")
        XCTAssertEqual(model.implementer, "gpt")
        XCTAssertEqual(model.reviewers, ["glm"])
        XCTAssertEqual(model.coordinatorThinking, .high)
        XCTAssertEqual(model.implementerThinking, .medium)
        XCTAssertEqual(model.reviewersThinking, .low)
        // Interaction level seeds from the projection value threaded at init.
        XCTAssertEqual(model.interactionLevel, .autonomous)
        XCTAssertNil(model.errorMessage)
        XCTAssertFalse(model.unauthorized)
    }

    func testInteractionLevelDefaultsToJudgementWhenUnknown() {
        let source = MockSettingsSource()
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        XCTAssertEqual(model.interactionLevel, .judgement)
    }

    func testThinkingLevelParsesUnknownToMedium() {
        XCTAssertEqual(ThinkingLevel.parse(""), .medium)
        XCTAssertEqual(ThinkingLevel.parse("XHIGH"), .xhigh)
        XCTAssertEqual(ThinkingLevel.parse("bogus"), .medium)
    }

    func testThinkingRoleWireValue() {
        XCTAssertEqual(ThinkingRole.all.wireValue, "")
        XCTAssertEqual(ThinkingRole.coordinator.wireValue, "coordinator")
        XCTAssertEqual(ThinkingRole.reviewers.wireValue, "reviewers")
    }

    // MARK: - Interaction level

    func testApplyInteractionLevelSendsRequestAndCommits() async {
        let source = MockSettingsSource()
        source.response = listModelsResponse()
        let model = SessionSettingsModel(
            source: source, sessionId: "s1", currentInteractionLevel: "judgement")
        await model.load()

        model.interactionLevel = .autonomous
        await model.applyInteractionLevel()

        XCTAssertEqual(source.levelArgs?.sessionId, "s1")
        XCTAssertEqual(source.levelArgs?.level, "autonomous")
        XCTAssertEqual(model.interactionLevel, .autonomous)
        XCTAssertNil(model.errorMessage)
    }

    func testApplyInteractionLevelNoOpWhenUnchanged() async {
        let source = MockSettingsSource()
        let model = SessionSettingsModel(
            source: source, sessionId: "s1", currentInteractionLevel: "judgement")

        model.interactionLevel = .judgement
        await model.applyInteractionLevel()

        XCTAssertNil(source.levelArgs) // never sent
    }

    func testApplyInteractionLevelRevertsAndSurfacesErrorOnFailure() async {
        let source = MockSettingsSource()
        source.setError = YccError.failedPrecondition(message: "cannot change now")
        let model = SessionSettingsModel(
            source: source, sessionId: "s1", currentInteractionLevel: "judgement")

        model.interactionLevel = .autonomous
        await model.applyInteractionLevel()

        // Reverted to the committed value; error surfaced verbatim.
        XCTAssertEqual(model.interactionLevel, .judgement)
        XCTAssertEqual(model.errorMessage, "cannot change now")
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

    func testApplyThinkingSendsScopeAndLevel() async {
        let source = MockSettingsSource()
        source.response = listModelsResponse()
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        await model.load()

        model.selectThinkingRole(.implementer)
        // Picker seeds from the implementer's known level.
        XCTAssertEqual(model.thinkingLevel, .medium)
        model.thinkingLevel = .max
        await model.applyThinking()

        XCTAssertEqual(source.thinkingArgs?.sessionId, "s1")
        XCTAssertEqual(source.thinkingArgs?.level, "max")
        XCTAssertEqual(source.thinkingArgs?.role, "implementer")
        XCTAssertEqual(model.implementerThinking, .max)
    }

    func testApplyThinkingAllRolesSendsEmptyRoleAndUpdatesAll() async {
        let source = MockSettingsSource()
        source.response = listModelsResponse()
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        await model.load()

        model.selectThinkingRole(.all)
        model.thinkingLevel = .off
        await model.applyThinking()

        XCTAssertEqual(source.thinkingArgs?.role, "") // empty = all roles
        XCTAssertEqual(source.thinkingArgs?.level, "off")
        XCTAssertEqual(model.coordinatorThinking, .off)
        XCTAssertEqual(model.implementerThinking, .off)
        XCTAssertEqual(model.reviewersThinking, .off)
    }

    func testApplyThinkingAllRolesNotSuppressedWhenRolesDiverge() async {
        // Roles diverge (coordinator high, implementer medium); applying the
        // coordinator's own level at `all` scope must still fire so the roles
        // unify rather than being suppressed by the no-op guard.
        let source = MockSettingsSource()
        source.response = listModelsResponse()
        let model = SessionSettingsModel(source: source, sessionId: "s1")
        await model.load()

        model.selectThinkingRole(.all)
        XCTAssertEqual(model.thinkingLevel, .high) // seeded from coordinator
        await model.applyThinking()

        XCTAssertEqual(source.thinkingArgs?.role, "")
        XCTAssertEqual(source.thinkingArgs?.level, "high")
        XCTAssertEqual(model.implementerThinking, .high)
        XCTAssertEqual(model.reviewersThinking, .high)
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
        let model = SessionSettingsModel(
            source: source, sessionId: "s1", currentInteractionLevel: "judgement")

        model.interactionLevel = .interactive
        await model.applyInteractionLevel()

        XCTAssertTrue(model.unauthorized)
    }
}
