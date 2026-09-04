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
    private(set) var tested: Ycc_V1_ModelConfig?
    private(set) var testCallCount = 0
    var testResponse = Ycc_V1_TestModelResponse()
    var testDelayNanos: UInt64 = 0

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
    func testModel(_ model: Ycc_V1_ModelConfig) async throws -> Ycc_V1_TestModelResponse {
        tested = model
        testCallCount += 1
        if testDelayNanos > 0 {
            try await Task.sleep(nanoseconds: testDelayNanos)
        }
        if let error { throw error }
        return testResponse
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
    func testEditorDraftBuildsSharedSaveAndTestConfig() {
        let draft = ModelEditorDraft(
            name: "  fireworks  ", isEnabled: false, backend: "openai", auth: "api-key",
            baseURL: "  https://api.fireworks.ai/inference/v1  ",
            modelID: "  accounts/fireworks/models/glm-5p3  ",
            keyEnv: "  FIREWORKS_API_KEY  ", thinking: "adaptive", effort: "high",
            thinkingDisplay: "summarized", priceInput: "1.4", priceOutput: "4.4",
            priceCacheRead: "0.26", priceCacheWrite: "not-a-price")

        XCTAssertTrue(draft.canSubmit)
        let config = draft.modelConfig()
        XCTAssertEqual(config.name, "fireworks")
        XCTAssertEqual(config.baseURL, "https://api.fireworks.ai/inference/v1")
        XCTAssertEqual(config.model, "accounts/fireworks/models/glm-5p3")
        XCTAssertEqual(config.keyEnv, "FIREWORKS_API_KEY")
        XCTAssertEqual(config.backend, "openai")
        XCTAssertEqual(config.auth, "api-key")
        XCTAssertEqual(config.thinking, "adaptive")
        XCTAssertEqual(config.effort, "high")
        XCTAssertEqual(config.thinkingDisplay, "summarized")
        XCTAssertTrue(config.disabled)
        XCTAssertTrue(config.hasDisabled)
        XCTAssertEqual(config.priceInput, 1.4)
        XCTAssertEqual(config.priceOutput, 4.4)
        XCTAssertEqual(config.priceCacheRead, 0.26)
        XCTAssertFalse(config.hasPriceCacheWrite)

        var presentationOnlyChange = draft
        presentationOnlyChange.name = "renamed"
        presentationOnlyChange.isEnabled = true
        presentationOnlyChange.priceInput = "9"
        XCTAssertEqual(presentationOnlyChange.probeFingerprint, draft.probeFingerprint)
        presentationOnlyChange.effort = "low"
        XCTAssertNotEqual(presentationOnlyChange.probeFingerprint, draft.probeFingerprint)
    }

    func testDisabledModelsRemainListedButAreNotEnabledChoices() async {
        let source = MockGlobalSettingsSource()
        var response = globalResponse()
        response.models[0].disabled = true
        source.response = response
        let model = GlobalSettingsModel(source: source)

        await model.load()

        XCTAssertEqual(model.models.map(\.name), ["alpha", "zeta"])
        XCTAssertEqual(model.enabledModels.map(\.name), ["alpha"])
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

    func testModelProbeMapsDraftAndSuccessResult() async {
        let source = MockGlobalSettingsSource()
        source.testResponse.success = true
        source.testResponse.message = "Model responded successfully."
        source.testResponse.durationMs = 42
        let model = GlobalSettingsModel(source: source)
        var draft = Ycc_V1_ModelConfig()
        draft.name = "fireworks"
        draft.backend = "openai"
        draft.baseURL = "https://api.fireworks.ai/inference/v1"
        draft.model = "accounts/fireworks/models/glm-5p3"
        draft.keyEnv = "FIREWORKS_API_KEY"
        draft.effort = "high"
        draft.disabled = true

        await model.testModel(draft)

        XCTAssertEqual(source.tested?.name, "fireworks")
        XCTAssertEqual(source.tested?.baseURL, draft.baseURL)
        XCTAssertEqual(source.tested?.model, draft.model)
        XCTAssertEqual(source.tested?.keyEnv, "FIREWORKS_API_KEY")
        XCTAssertEqual(source.tested?.effort, "high")
        XCTAssertEqual(source.tested?.disabled, true)
        XCTAssertEqual(model.modelTestResult, ModelTestResult(
            success: true, message: "Model responded successfully.",
            durationMS: 42, errorKind: "", status: 0))
        XCTAssertFalse(model.isTestingModel)
    }

    func testModelProbeSurfacesProviderAndTransportFailuresInline() async {
        let source = MockGlobalSettingsSource()
        var failure = Ycc_V1_TestModelResponse()
        failure.message = "unknown model id"
        failure.errorKind = "invalid_request"
        failure.status = 404
        source.testResponse = failure
        let model = GlobalSettingsModel(source: source)

        await model.testModel(Ycc_V1_ModelConfig())
        XCTAssertEqual(model.modelTestResult, ModelTestResult(
            success: false, message: "unknown model id",
            durationMS: 0, errorKind: "invalid_request", status: 404))

        source.error = YccError.rpc(message: "daemon unavailable")
        await model.testModel(Ycc_V1_ModelConfig())
        XCTAssertEqual(model.modelTestResult?.success, false)
        XCTAssertEqual(model.modelTestResult?.message, "daemon unavailable")
        XCTAssertNil(model.errorMessage)

        source.error = YccError.unauthorized
        await model.testModel(Ycc_V1_ModelConfig())
        XCTAssertTrue(model.unauthorized)
        XCTAssertEqual(model.modelTestResult?.message, "Daemon authentication expired.")
    }

    func testModelProbeTracksInFlightAndRejectsOverlap() async {
        let source = MockGlobalSettingsSource()
        source.testDelayNanos = 100_000_000
        source.testResponse.success = true
        source.testResponse.message = "ok"
        let model = GlobalSettingsModel(source: source)
        let task = Task { await model.testModel(Ycc_V1_ModelConfig()) }
        while !model.isTestingModel { await Task.yield() }

        XCTAssertTrue(model.isTestingModel)
        await model.testModel(Ycc_V1_ModelConfig())
        XCTAssertEqual(source.testCallCount, 1)

        // Editing/dismissing the form invalidates the request. Its eventual
        // completion must not republish a stale result in another editor.
        model.clearModelTestResult()
        XCTAssertFalse(model.isTestingModel)
        await task.value
        XCTAssertFalse(model.isTestingModel)
        XCTAssertNil(model.modelTestResult)
    }

    func testInvalidatedProbeStillPropagatesDaemonUnauthorized() async {
        let source = MockGlobalSettingsSource()
        source.testDelayNanos = 50_000_000
        source.error = YccError.unauthorized
        let model = GlobalSettingsModel(source: source)
        let task = Task { await model.testModel(Ycc_V1_ModelConfig()) }
        while !model.isTestingModel { await Task.yield() }

        model.clearModelTestResult()
        await task.value

        XCTAssertTrue(model.unauthorized)
        XCTAssertNil(model.modelTestResult)
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
        config.disabled = true
        let saved = await model.saveModel(config)
        XCTAssertTrue(saved)
        XCTAssertEqual(source.upserted?.name, "new")
        XCTAssertEqual(source.upserted?.disabled, true)
        XCTAssertEqual(source.upserted?.hasDisabled, true)

        source.error = YccError.unauthorized
        let removed = await model.removeModel(name: "new")
        XCTAssertFalse(removed)
        XCTAssertTrue(model.unauthorized)
    }
}
