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
}

extension YccClient: GlobalSettingsSource {}

/// Observable state for global role defaults, per-role thinking, and the logical
/// model registry. An empty session id tells the daemon to update persisted defaults
/// without targeting a live session.
@MainActor
@Observable
public final class GlobalSettingsModel {
    public private(set) var models: [Ycc_V1_ModelInfo] = []
    public var coordinator = ""
    public var implementer = ""
    public var reviewers: [String] = []
    public private(set) var coordinatorThinking: ThinkingLevel = .medium
    public private(set) var implementerThinking: ThinkingLevel = .medium
    public private(set) var reviewersThinking: ThinkingLevel = .medium

    public private(set) var isLoading = false
    public private(set) var isApplying = false
    public private(set) var errorMessage: String?
    public private(set) var unauthorized = false

    private let source: GlobalSettingsSource
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

    public func clearError() { errorMessage = nil }

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
