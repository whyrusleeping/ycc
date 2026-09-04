import Foundation
import Observation
import YccProto

/// Daemon-wide model/role settings used by the iOS home settings screen.
public protocol GlobalSettingsSource: Sendable {
    func listModels() async throws -> Ycc_V1_ListModelsResponse
    func setRoleConfig(
        sessionId: String, coordinator: String, implementer: String, reviewers: [String]
    ) async throws
    func setThinking(sessionId: String, level: String, role: String) async throws
    func getModelConfig(name: String) async throws -> Ycc_V1_ModelConfig
    func upsertModel(_ model: Ycc_V1_ModelConfig) async throws
    func removeModel(name: String) async throws
    func discoverModels(
        backend: String, baseURL: String, keyEnv: String
    ) async throws -> Ycc_V1_DiscoverModelsResponse
    func testModel(_ model: Ycc_V1_ModelConfig) async throws -> Ycc_V1_TestModelResponse
}

extension YccClient: GlobalSettingsSource {}

/// Editable form values shared by Save and Test so both actions construct the
/// same complete unsaved `ModelConfig`.
public struct ModelEditorDraft: Equatable, Sendable {
    public var name: String
    public var isEnabled: Bool
    public var backend: String
    public var auth: String
    public var baseURL: String
    public var modelID: String
    public var keyEnv: String
    public var thinking: String
    public var effort: String
    public var thinkingDisplay: String
    public var priceInput: String
    public var priceOutput: String
    public var priceCacheRead: String
    public var priceCacheWrite: String

    public init(
        name: String = "", isEnabled: Bool = true, backend: String = "anthropic",
        auth: String = "api-key", baseURL: String = "", modelID: String = "",
        keyEnv: String = "", thinking: String = "", effort: String = "",
        thinkingDisplay: String = "", priceInput: String = "", priceOutput: String = "",
        priceCacheRead: String = "", priceCacheWrite: String = ""
    ) {
        self.name = name
        self.isEnabled = isEnabled
        self.backend = backend
        self.auth = auth
        self.baseURL = baseURL
        self.modelID = modelID
        self.keyEnv = keyEnv
        self.thinking = thinking
        self.effort = effort
        self.thinkingDisplay = thinkingDisplay
        self.priceInput = priceInput
        self.priceOutput = priceOutput
        self.priceCacheRead = priceCacheRead
        self.priceCacheWrite = priceCacheWrite
    }

    public var canSubmit: Bool {
        !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !backend.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !modelID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// Identity for fields that alter the provider inference request. Availability
    /// and pricing do not invalidate a successful connection test.
    public var probeFingerprint: String {
        [backend, auth, baseURL, modelID, keyEnv, thinking, effort, thinkingDisplay]
            .joined(separator: "\u{0}")
    }

    public func modelConfig() -> Ycc_V1_ModelConfig {
        var config = Ycc_V1_ModelConfig()
        config.name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        config.disabled = !isEnabled
        config.backend = backend
        config.auth = auth
        config.baseURL = baseURL.trimmingCharacters(in: .whitespacesAndNewlines)
        config.model = modelID.trimmingCharacters(in: .whitespacesAndNewlines)
        config.keyEnv = keyEnv.trimmingCharacters(in: .whitespacesAndNewlines)
        config.thinking = thinking
        config.effort = effort
        config.thinkingDisplay = thinkingDisplay
        if let value = Double(priceInput) { config.priceInput = value }
        if let value = Double(priceOutput) { config.priceOutput = value }
        if let value = Double(priceCacheRead) { config.priceCacheRead = value }
        if let value = Double(priceCacheWrite) { config.priceCacheWrite = value }
        return config
    }
}

/// Presentation-safe result of a real model probe.
public struct ModelTestResult: Equatable, Sendable {
    public let success: Bool
    public let message: String
    public let durationMS: Int64
    public let errorKind: String
    public let status: Int32
}

/// Observable state for global role defaults, assigned-model thinking, and the
/// logical model registry. An empty session id tells the daemon to update persisted defaults
/// without targeting a live session.
@MainActor
@Observable
public final class GlobalSettingsModel {
    public private(set) var models: [Ycc_V1_ModelInfo] = []
    /// Models available for new role assignments. Disabled entries remain in
    /// ``models`` so the backend list can display and re-enable them.
    public var enabledModels: [Ycc_V1_ModelInfo] { models.filter { !$0.disabled } }
    public var coordinator = ""
    public var implementer = ""
    public var reviewers: [String] = []
    public private(set) var coordinatorThinking: ThinkingLevel = .medium
    public private(set) var implementerThinking: ThinkingLevel = .medium
    public private(set) var reviewersThinking: ThinkingLevel = .medium

    public private(set) var isLoading = false
    public private(set) var isApplying = false
    public private(set) var isTestingModel = false
    public private(set) var modelTestResult: ModelTestResult?
    public private(set) var errorMessage: String?
    public private(set) var unauthorized = false

    private let source: GlobalSettingsSource
    private var modelTestGeneration = 0
    private var committedCoordinator = ""
    private var committedImplementer = ""
    private var committedReviewers: [String] = []

    public init(source: GlobalSettingsSource) {
        self.source = source
    }

    public func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let response = try await source.listModels()
            models = response.models.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
            coordinator = response.coordinator
            implementer = response.implementer
            reviewers = response.reviewers
            committedCoordinator = coordinator
            committedImplementer = implementer
            committedReviewers = reviewers
            coordinatorThinking = .parse(response.coordinatorThinking)
            implementerThinking = .parse(response.implementerThinking)
            reviewersThinking = .parse(response.reviewersThinking)
            errorMessage = nil
        } catch { handle(error) }
    }

    public func applyRoles() async {
        guard !isApplying else { return }
        isApplying = true
        defer { isApplying = false }
        do {
            try await source.setRoleConfig(
                sessionId: "", coordinator: coordinator,
                implementer: implementer, reviewers: reviewers)
            committedCoordinator = coordinator
            committedImplementer = implementer
            committedReviewers = reviewers
            errorMessage = nil
        } catch {
            coordinator = committedCoordinator
            implementer = committedImplementer
            reviewers = committedReviewers
            handle(error)
        }
    }

    public func isReviewerSelected(_ name: String) -> Bool { reviewers.contains(name) }

    /// Toggle reviewer membership. The final reviewer cannot be removed because the
    /// daemon's wire contract uses an empty list to mean “leave unchanged”.
    @discardableResult
    public func toggleReviewer(_ name: String) -> Bool {
        if let index = reviewers.firstIndex(of: name) {
            guard reviewers.count > 1 else {
                errorMessage = "At least one reviewer must remain selected."
                return false
            }
            reviewers.remove(at: index)
        } else {
            reviewers.append(name)
        }
        return true
    }

    public func thinking(for role: ThinkingRole) -> ThinkingLevel {
        switch role {
        case .all, .coordinator: return coordinatorThinking
        case .implementer: return implementerThinking
        case .reviewers: return reviewersThinking
        }
    }

    public func setThinking(_ level: ThinkingLevel, for role: ThinkingRole) async {
        guard !isApplying else { return }
        isApplying = true
        defer { isApplying = false }
        do {
            try await source.setThinking(sessionId: "", level: level.wireValue, role: role.wireValue)
            switch role {
            case .all:
                coordinatorThinking = level
                implementerThinking = level
                reviewersThinking = level
            case .coordinator: coordinatorThinking = level
            case .implementer: implementerThinking = level
            case .reviewers: reviewersThinking = level
            }
            errorMessage = nil
        } catch { handle(error) }
    }

    public func getModelConfig(name: String) async -> Ycc_V1_ModelConfig? {
        do {
            errorMessage = nil
            return try await source.getModelConfig(name: name)
        } catch {
            handle(error)
            return nil
        }
    }

    public func saveModel(_ config: Ycc_V1_ModelConfig) async -> Bool {
        guard !isApplying else { return false }
        isApplying = true
        defer { isApplying = false }
        do {
            try await source.upsertModel(config)
            errorMessage = nil
            await load()
            return true
        } catch {
            handle(error)
            return false
        }
    }

    public func removeModel(name: String) async -> Bool {
        guard !isApplying else { return false }
        isApplying = true
        defer { isApplying = false }
        do {
            try await source.removeModel(name: name)
            errorMessage = nil
            await load()
            return true
        } catch {
            handle(error)
            return false
        }
    }

    public func discoverModels(
        backend: String, baseURL: String, keyEnv: String
    ) async -> Ycc_V1_DiscoverModelsResponse? {
        guard !isApplying else { return nil }
        isApplying = true
        defer { isApplying = false }
        do {
            let result = try await source.discoverModels(
                backend: backend, baseURL: baseURL, keyEnv: keyEnv)
            errorMessage = nil
            return result
        } catch {
            handle(error)
            return nil
        }
    }

    /// Test the exact unsaved model draft without changing the live registry.
    /// Provider failures are normal response data; transport/auth failures are
    /// also folded into the inline result so the editor never loses feedback.
    public func testModel(_ config: Ycc_V1_ModelConfig) async {
        guard !isApplying && !isTestingModel else { return }
        modelTestGeneration &+= 1
        let generation = modelTestGeneration
        isTestingModel = true
        modelTestResult = nil
        errorMessage = nil
        defer {
            if generation == modelTestGeneration {
                isTestingModel = false
            }
        }
        do {
            let response = try await source.testModel(config)
            guard generation == modelTestGeneration else { return }
            modelTestResult = ModelTestResult(
                success: response.success,
                message: response.message,
                durationMS: response.durationMs,
                errorKind: response.errorKind,
                status: response.status)
        } catch {
            // Daemon authentication remains global even if the editor that
            // launched the request has already disappeared.
            if case YccError.unauthorized = error {
                unauthorized = true
            }
            guard generation == modelTestGeneration else { return }
            modelTestResult = ModelTestResult(
                success: false,
                message: message(for: error),
                durationMS: 0,
                errorKind: "",
                status: 0)
        }
    }

    public func clearModelTestResult() {
        modelTestGeneration &+= 1
        isTestingModel = false
        modelTestResult = nil
    }
    public func clearError() { errorMessage = nil }

    private func message(for error: Error) -> String {
        switch error {
        case YccError.unauthorized: return "Daemon authentication expired."
        case let YccError.rpc(message): return message
        case let YccError.notFound(message): return message
        case let YccError.failedPrecondition(message): return message
        default: return error.localizedDescription
        }
    }

    private func handle(_ error: Error) {
        switch error {
        case YccError.unauthorized: unauthorized = true
        case let YccError.rpc(message): errorMessage = message
        case let YccError.notFound(message): errorMessage = message
        case let YccError.failedPrecondition(message): errorMessage = message
        default: errorMessage = error.localizedDescription
        }
    }
}
