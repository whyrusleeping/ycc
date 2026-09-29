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

    private let cache: AppDataCache?
    private let cacheGeneration: UInt64

    /// A cached registry (from an earlier visit on this connection) renders the
    /// settings screen at once; ``load()`` revalidates it.
    public init(source: GlobalSettingsSource, cache: AppDataCache? = nil) {
        self.source = source
        self.cache = cache
        self.cacheGeneration = cache?.generation ?? 0
        if let cached = cache?.value(.globalModels, as: Ycc_V1_ListModelsResponse.self) {
            apply(cached)
        }
    }

    /// Bumped per load: only the newest overlapping load (e.g. a background
    /// reload after a save racing pull-to-refresh) may publish.
    private var loadGeneration: UInt64 = 0

    public func load() async {
        loadGeneration &+= 1
        let generation = loadGeneration
        isLoading = true
        defer { if generation == loadGeneration { isLoading = false } }
        do {
            let response = try await source.listModels()
            guard generation == loadGeneration else { return }
            apply(response)
            cache?.store(response, for: .globalModels, ifGeneration: cacheGeneration)
            errorMessage = nil
        } catch {
            guard generation == loadGeneration else { return }
            handle(error)
        }
    }

    private func apply(_ response: Ycc_V1_ListModelsResponse) {
        models = response.models.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
        // A (background) reload must not clobber a change still in flight;
        // that change's own completion reconciles it.
        if !isApplying {
            coordinator = response.coordinator
            implementer = response.implementer
            reviewers = response.reviewers
            committedCoordinator = coordinator
            committedImplementer = implementer
            committedReviewers = reviewers
        }
        if !isApplyingThinking(.coordinator) {
            coordinatorThinking = .parse(response.coordinatorThinking)
        }
        if !isApplyingThinking(.implementer) {
            implementerThinking = .parse(response.implementerThinking)
        }
        if !isApplyingThinking(.reviewers) {
            reviewersThinking = .parse(response.reviewersThinking)
        }
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

    /// The user picked a coordinator: update the picker at once and persist it.
    /// Views bind through this (not `onChange`), because ``load()`` seeds the
    /// same property and an observer cannot tell a seed from a pick — every
    /// settings visit used to send a redundant `SetRoleConfig`.
    @discardableResult
    public func chooseCoordinator(_ name: String) -> Task<Void, Never>? {
        guard name != coordinator, !isApplying else { return nil }
        coordinator = name
        return Task { @MainActor [weak self] in await self?.applyRoles() }
    }

    /// The user picked an implementer (see ``chooseCoordinator(_:)``).
    @discardableResult
    public func chooseImplementer(_ name: String) -> Task<Void, Never>? {
        guard name != implementer, !isApplying else { return nil }
        implementer = name
        return Task { @MainActor [weak self] in await self?.applyRoles() }
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

    /// Roles whose thinking change is in flight. Only those pickers are
    /// disabled; the rest of the form stays interactive.
    public private(set) var applyingThinkingRoles: Set<ThinkingRole> = []

    /// Whether the picker for `role` has a thinking change in flight (an
    /// `all`-scope change busies every role, and vice versa).
    public func isApplyingThinking(_ role: ThinkingRole) -> Bool {
        if applyingThinkingRoles.contains(role) || applyingThinkingRoles.contains(.all) { return true }
        return role == .all && !applyingThinkingRoles.isEmpty
    }

    /// Change a role's default thinking level. The picker shows the new level
    /// immediately; `SetThinking` confirms it in the background and a failure
    /// reverts the picker (only if nothing newer replaced it) with an error.
    public func setThinking(_ level: ThinkingLevel, for role: ThinkingRole) async {
        guard !isApplyingThinking(role) else { return }
        let previous = (coordinatorThinking, implementerThinking, reviewersThinking)
        applyThinkingLocally(level, for: role)
        applyingThinkingRoles.insert(role)
        defer { applyingThinkingRoles.remove(role) }
        do {
            try await source.setThinking(sessionId: "", level: level.wireValue, role: role.wireValue)
            errorMessage = nil
        } catch {
            if (role == .all || role == .coordinator), coordinatorThinking == level {
                coordinatorThinking = previous.0
            }
            if (role == .all || role == .implementer), implementerThinking == level {
                implementerThinking = previous.1
            }
            if (role == .all || role == .reviewers), reviewersThinking == level {
                reviewersThinking = previous.2
            }
            handle(error)
        }
    }

    private func applyThinkingLocally(_ level: ThinkingLevel, for role: ThinkingRole) {
        switch role {
        case .all:
            coordinatorThinking = level
            implementerThinking = level
            reviewersThinking = level
        case .coordinator: coordinatorThinking = level
        case .implementer: implementerThinking = level
        case .reviewers: reviewersThinking = level
        }
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

    /// Upsert a model. Returns as soon as the daemon accepts it (so the editor
    /// can dismiss after one round trip); the registry list reloads in the
    /// background.
    public func saveModel(_ config: Ycc_V1_ModelConfig) async -> Bool {
        guard !isApplying else { return false }
        isApplying = true
        defer { isApplying = false }
        do {
            try await source.upsertModel(config)
            errorMessage = nil
            scheduleReload()
            return true
        } catch {
            handle(error)
            return false
        }
    }

    /// Remove a model. The row disappears once the daemon confirms; the
    /// registry list reloads in the background.
    public func removeModel(name: String) async -> Bool {
        guard !isApplying else { return false }
        isApplying = true
        defer { isApplying = false }
        do {
            try await source.removeModel(name: name)
            errorMessage = nil
            models.removeAll { $0.name == name }
            scheduleReload()
            return true
        } catch {
            handle(error)
            return false
        }
    }

    /// The background registry reload started after a model mutation.
    @ObservationIgnored private(set) var reloadTask: Task<Void, Never>?

    private func scheduleReload() {
        // A superseded reload is harmless: `load()` publishes only its newest
        // generation, so an older response can never overwrite a newer one.
        reloadTask = Task { @MainActor [weak self] in
            await self?.load()
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
