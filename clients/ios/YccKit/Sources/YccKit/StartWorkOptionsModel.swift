import Foundation
import Observation
import YccProto

/// What ``StartWorkOptionsModel`` needs from the daemon.
public protocol StartWorkSource: Sendable {
    func listModels() async throws -> Ycc_V1_ListModelsResponse
    func startSession(
        project: String, mode: String, prompt: String, coordinatorModel: String,
        images: [MessageImage]
    ) async throws -> String
    func spawnWorkstream(
        project: String, mode: String, prompt: String, coordinatorModel: String,
        preset: String, taskID: String, images: [MessageImage]
    ) async throws -> Ycc_V1_WorkstreamInfo
    func setRoleConfig(
        sessionId: String, coordinator: String, implementer: String, reviewers: [String]
    ) async throws
}

extension YccClient: StartWorkSource {}

/// A work session started from ``StartWorkOptionsModel``. `roleError` is set
/// when the session started but its implementer/reviewer choice could not be
/// applied, so it runs with the default roles.
public struct StartedWork: Equatable, Sendable {
    public let sessionID: String
    public let roleError: String?
}

/// The "start work with settings" sheet behind a long press on a task's start
/// button: role models and an optional workstream for one task's work session.
/// Every choice applies to that session only; the persisted role defaults stay
/// untouched. The coordinator rides on the start request; implementer and
/// reviewers, which `StartSession` cannot carry, are applied to the new session
/// with `SetRoleConfig` as soon as it starts — a round trip, well before the
/// coordinator's first model turn could spawn either.
@MainActor
@Observable
public final class StartWorkOptionsModel {
    public let project: String
    public let taskID: String
    public let prompt: String

    public private(set) var models: [Ycc_V1_ModelInfo] = []
    public var coordinator = ""
    public var implementer = ""
    public private(set) var reviewers: [String] = []
    public var separateWorkstream = false

    public private(set) var hasLoaded = false
    public private(set) var isLoading = false
    public private(set) var isStarting = false
    public private(set) var errorMessage: String?
    public private(set) var unauthorized = false

    private var defaults = Ycc_V1_ListModelsResponse()
    private let source: StartWorkSource

    public var defaultCoordinator: String { defaults.coordinator }
    public var defaultImplementer: String { defaults.implementer }
    public var defaultReviewers: [String] { defaults.reviewers }

    public init(source: StartWorkSource, project: String, taskID: String, prompt: String) {
        self.source = source
        self.project = project
        self.taskID = taskID
        self.prompt = prompt
    }

    /// Load the model registry and seed each role with its configured default.
    public func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let response = try await source.listModels()
            defaults = response
            models = response.models.sorted {
                $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending
            }
            coordinator = response.coordinator
            implementer = response.implementer
            reviewers = response.reviewers
            hasLoaded = true
            errorMessage = nil
        } catch {
            handle(error)
        }
    }

    /// Models offered for a role: the enabled ones, plus any disabled model the
    /// role is currently assigned so the picker can show its selection.
    public func choices(including assigned: [String]) -> [Ycc_V1_ModelInfo] {
        models.filter { !$0.disabled || assigned.contains($0.name) }
    }

    public func isReviewerSelected(_ name: String) -> Bool { reviewers.contains(name) }

    /// Toggle reviewer membership. The last reviewer stays: an empty list means
    /// "leave unchanged" to `SetRoleConfig`.
    public func toggleReviewer(_ name: String) {
        if let index = reviewers.firstIndex(of: name) {
            guard reviewers.count > 1 else { return }
            reviewers.remove(at: index)
        } else {
            reviewers.append(name)
        }
    }

    public var canStart: Bool { hasLoaded && !isStarting && !coordinator.isEmpty }

    /// Start the work session (in a new workstream when chosen), then apply the
    /// implementer/reviewer choices that differ from the defaults. Returns nil
    /// when nothing started, with ``errorMessage`` or ``unauthorized`` set.
    public func start() async -> StartedWork? {
        guard canStart else { return nil }
        isStarting = true
        defer { isStarting = false }
        // An unchanged coordinator goes as "" so it is not recorded as an
        // explicit per-session override.
        let coordinatorOverride = coordinator == defaults.coordinator ? "" : coordinator
        let sessionID: String
        do {
            if separateWorkstream {
                sessionID = try await source.spawnWorkstream(
                    project: project, mode: "work", prompt: prompt,
                    coordinatorModel: coordinatorOverride, preset: "", taskID: taskID,
                    images: []
                ).sessionID
            } else {
                sessionID = try await source.startSession(
                    project: project, mode: "work", prompt: prompt,
                    coordinatorModel: coordinatorOverride, images: [])
            }
        } catch {
            handle(error)
            return nil
        }
        errorMessage = nil
        let implementerOverride = implementer == defaults.implementer ? "" : implementer
        let reviewersOverride = reviewers == defaults.reviewers ? [] : reviewers
        guard !sessionID.isEmpty, !implementerOverride.isEmpty || !reviewersOverride.isEmpty else {
            return StartedWork(sessionID: sessionID, roleError: nil)
        }
        do {
            try await source.setRoleConfig(
                sessionId: sessionID, coordinator: "",
                implementer: implementerOverride, reviewers: reviewersOverride)
            return StartedWork(sessionID: sessionID, roleError: nil)
        } catch {
            if case YccError.unauthorized = error { unauthorized = true }
            return StartedWork(sessionID: sessionID, roleError: Self.message(for: error))
        }
    }

    private func handle(_ error: Error) {
        if case YccError.unauthorized = error {
            unauthorized = true
        } else {
            errorMessage = Self.message(for: error)
        }
    }

    private static func message(for error: Error) -> String {
        (error as? YccError)?.displayMessage ?? error.localizedDescription
    }
}
