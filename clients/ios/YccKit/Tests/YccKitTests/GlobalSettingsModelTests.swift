import XCTest
import YccProto
@testable import YccKit

private final class MockGlobalSettingsSource: GlobalSettingsSource, @unchecked Sendable {
    var response = Ycc_V1_ListModelsResponse()
    var error: Error?
    var configs: [String: Ycc_V1_ModelConfig] = [:]
    private(set) var roleArgs: (String, String, String, [String])?
    private(set) var thinkingArgs: (String, String, String)?
    private(set) var upserted: Ycc_V1_ModelConfig?
    private(set) var removed: String?

    func listModels() async throws -> Ycc_V1_ListModelsResponse {
        if let error { throw error }
        return response
    }
    func setRoleConfig(
        sessionId: String, coordinator: String, implementer: String, reviewers: [String]
    ) async throws {
        if let error { throw error }
        roleArgs = (sessionId, coordinator, implementer, reviewers)
    }
    func setThinking(sessionId: String, level: String, role: String) async throws {
        if let error { throw error }
        thinkingArgs = (sessionId, level, role)
    }
    func getModelConfig(name: String) async throws -> Ycc_V1_ModelConfig {
        if let error { throw error }
        return configs[name] ?? Ycc_V1_ModelConfig()
    }
    func upsertModel(_ model: Ycc_V1_ModelConfig) async throws {
        if let error { throw error }
        upserted = model
    }
    func removeModel(name: String) async throws {
        if let error { throw error }
        removed = name
    }
    func discoverModels(
        backend: String, baseURL: String, keyEnv: String
    ) async throws -> Ycc_V1_DiscoverModelsResponse {
        if let error { throw error }
        var result = Ycc_V1_DiscoverModelsResponse()
        result.modelIds = ["model-a", "model-b"]
        return result
    }
}

private func globalResponse() -> Ycc_V1_ListModelsResponse {
    var a = Ycc_V1_ModelInfo(); a.name = "zeta"
    var b = Ycc_V1_ModelInfo(); b.name = "alpha"
    var response = Ycc_V1_ListModelsResponse()
    response.models = [a, b]
    response.coordinator = "zeta"
    response.implementer = "alpha"
    response.reviewers = ["zeta"]
    response.coordinatorThinking = "high"
    response.implementerThinking = "low"
    response.reviewersThinking = "medium"
    return response
}

@MainActor
final class GlobalSettingsModelTests: XCTestCase {
    func testLoadSeedsGlobalDefaultsAndSortsModels() async {
        let source = MockGlobalSettingsSource()
        source.response = globalResponse()
        let model = GlobalSettingsModel(source: source)

        await model.load()

        XCTAssertEqual(model.models.map(\.name), ["alpha", "zeta"])
        XCTAssertEqual(model.coordinator, "zeta")
        XCTAssertEqual(model.implementer, "alpha")
        XCTAssertEqual(model.reviewers, ["zeta"])
        XCTAssertEqual(model.coordinatorThinking, .high)
        XCTAssertEqual(model.implementerThinking, .low)
    }

    func testGlobalRoleApplyUsesEmptySessionID() async {
        let source = MockGlobalSettingsSource()
        source.response = globalResponse()
        let model = GlobalSettingsModel(source: source)
        await model.load()

        model.coordinator = "alpha"
        _ = model.toggleReviewer("alpha")
        await model.applyRoles()

        XCTAssertEqual(source.roleArgs?.0, "")
        XCTAssertEqual(source.roleArgs?.1, "alpha")
        XCTAssertEqual(source.roleArgs?.3, ["zeta", "alpha"])
    }

    func testCannotRemoveLastReviewer() async {
        let source = MockGlobalSettingsSource()
        source.response = globalResponse()
        let model = GlobalSettingsModel(source: source)
        await model.load()

        XCTAssertFalse(model.toggleReviewer("zeta"))
        XCTAssertEqual(model.reviewers, ["zeta"])
        XCTAssertEqual(model.errorMessage, "At least one reviewer must remain selected.")
    }

    func testThinkingUsesRoleAndEmptySessionID() async {
        let source = MockGlobalSettingsSource()
        source.response = globalResponse()
        let model = GlobalSettingsModel(source: source)
        await model.load()

        await model.setThinking(.max, for: .reviewers)

        XCTAssertEqual(source.thinkingArgs?.0, "")
        XCTAssertEqual(source.thinkingArgs?.1, "max")
        XCTAssertEqual(source.thinkingArgs?.2, "reviewers")
        XCTAssertEqual(model.reviewersThinking, .max)
    }

    func testRegistryOperationsAndUnauthorized() async {
        let source = MockGlobalSettingsSource()
        source.response = globalResponse()
        let model = GlobalSettingsModel(source: source)
        await model.load()

        var config = Ycc_V1_ModelConfig()
        config.name = "new"
        config.backend = "ollama"
        config.model = "qwen"
        let saved = await model.saveModel(config)
        XCTAssertTrue(saved)
        XCTAssertEqual(source.upserted?.name, "new")

        source.error = YccError.unauthorized
        let removed = await model.removeModel(name: "new")
        XCTAssertFalse(removed)
        XCTAssertTrue(model.unauthorized)
    }
}
