import Foundation
import Observation
import YccProto

/// What ``StartWorkOptionsModel`` needs from the daemon.
public protocol StartWorkSource: Sendable {
    func listModels() async throws -> Ycc_V1_ListModelsResponse
    func startSession(
        project: String, mode: String, prompt: String, coordinatorModel: String,
        coordinatorThinking: String, images: [MessageImage]
    ) async throws -> String
    func spawnWorkstream(
        project: String, mode: String, prompt: String, coordinatorModel: String,
        coordinatorThinking: String, preset: String, taskID: String, images: [MessageImage]
    ) async throws -> Ycc_V1_WorkstreamInfo
    func setRoleConfig(
        sessionId: String, coordinator: String, implementer: String, reviewers: [String]
    ) async throws
    func setThinking(sessionId: String, level: String, role: String) async throws
}

extension YccClient: StartWorkSource {}

/// A work session started from ``StartWorkOptionsModel``. `roleError` is set
/// when the session started but its implementer/reviewer models or thinking
/// levels could not be applied, so it runs with the defaults for those.
public struct StartedWork: Equatable, Sendable {
    public let sessionID: String
    public let roleError: String?
}

/// The "start work with settings" sheet behind a long press on a task's start
/// button: role models, their thinking levels, and an optional workstream for
/// one task's work session. Every choice applies to that session only; the
/// persisted defaults stay untouched. The coordinator model and thinking ride on
/// the start request; implementer and reviewers, which `StartSession` cannot
/// carry, are applied to the new session with `SetRoleConfig` and session-scoped
/// `SetThinking` as soon as it starts — round trips, well before the
/// coordinator's first model turn could spawn either.
@MainActor
@Observable
public final class StartWorkOptionsModel {
    public let project: String
    public let taskID: String
    public let prompt: String

    public private(set) var models: [Ycc_V1_ModelInfo] = []
    public var coordinator = "" {
        didSet { if coordinator != oldValue { modelChanged(for: .coordinator) } }
    }
    public var implementer = "" {
        didSet { if implementer != oldValue { modelChanged(for: .implementer) } }
    }
    public private(set) var reviewers: [String] = [] {
        didSet { if reviewers != oldValue { modelChanged(for: .reviewers) } }
    }
    /// Each role's thinking level for this session; `nil` keeps the model's
    /// saved level. The daemon keeps one level per model, so a pick reaches
    /// every role sharing a model (``chooseThinking(_:for:)``), and a model
    /// change moves a pick to the nearest supported level.
    public private(set) var coordinatorThinking: ThinkingLevel?
    public private(set) var implementerThinking: ThinkingLevel?
    public private(set) var reviewersThinking: ThinkingLevel?
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

    /// The levels a role's model(s) support (the reviewers: any reviewer model's).
    public func thinkingLevels(for role: ThinkingRole) -> [ThinkingLevel] {
        switch role {
        case .all, .coordinator: return ThinkingLevel.supported(named: coordinator, in: models)
        case .implementer: return ThinkingLevel.supported(named: implementer, in: models)
        case .reviewers: return ThinkingLevel.supported(anyOf: reviewers, in: models)
        }
    }

    private static let roles: [ThinkingRole] = [.coordinator, .implementer, .reviewers]

    private func roleModels(_ role: ThinkingRole) -> [String] {
        switch role {
        case .all, .coordinator: return [coordinator]
        case .implementer: return [implementer]
        case .reviewers: return reviewers
        }
    }

    public func thinking(for role: ThinkingRole) -> ThinkingLevel? {
        switch role {
        case .all, .coordinator: return coordinatorThinking
        case .implementer: return implementerThinking
        case .reviewers: return reviewersThinking
        }
    }

    private func setThinking(_ level: ThinkingLevel?, for role: ThinkingRole) {
        switch role {
        case .all, .coordinator: coordinatorThinking = level
        case .implementer: implementerThinking = level
        case .reviewers: reviewersThinking = level
        }
    }

    /// The other roles that share a model with `role`.
    private func rolesSharingModel(with role: ThinkingRole) -> [ThinkingRole] {
        let mine = Set(roleModels(role))
        return Self.roles.filter { $0 != role && !mine.isDisjoint(with: roleModels($0)) }
    }

    /// The user picked a thinking level for a role. Roles sharing one of its
    /// models get the same level (clamped to their models), as the daemon will
    /// run them.
    public func chooseThinking(_ level: ThinkingLevel?, for role: ThinkingRole) {
        setThinking(level, for: role)
        for other in rolesSharingModel(with: role) {
            setThinking(level.clamped(to: thinkingLevels(for: other)), for: other)
        }
    }

    /// A role's model changed: keep its pick at a supported level, or adopt the
    /// pick of a role it now shares a model with.
    private func modelChanged(for role: ThinkingRole) {
        let levels = thinkingLevels(for: role)
        if let shared = rolesSharingModel(with: role).lazy.compactMap({ self.thinking(for: $0) }).first {
            setThinking(shared.clamped(to: levels), for: role)
        } else {
            setThinking(thinking(for: role).clamped(to: levels), for: role)
        }
    }

    /// Why the picks cannot all apply, when two roles sharing a model would
    /// run it at different levels (possible after a model change): the daemon
    /// keeps one level per model in a session.
    public var thinkingConflict: String? {
        for model in Set(Self.roles.flatMap(roleModels)).sorted() {
            let levels = ThinkingLevel.supported(named: model, in: models)
            let sharing = Self.roles.filter { roleModels($0).contains(model) }
            let picks = sharing.map { thinking(for: $0).clamped(to: levels) }
            if Set(picks).count > 1 {
                let names = sharing.map(\.title).joined(separator: " and ")
                return "\(names) share \(model), so pick the same thinking level for them."
            }
        }
        return nil
    }

    /// The default thinking level of a role when it keeps its default model(s),
    /// for the "Default" choice's subtitle; nil when unknown.
    public func defaultThinking(for role: ThinkingRole) -> ThinkingLevel? {
        let (level, unchanged): (String, Bool)
        switch role {
        case .all, .coordinator: (level, unchanged) = (defaults.coordinatorThinking, coordinator == defaults.coordinator)
        case .implementer: (level, unchanged) = (defaults.implementerThinking, implementer == defaults.implementer)
        case .reviewers: (level, unchanged) = (defaults.reviewersThinking, reviewers == defaults.reviewers)
        }
        return unchanged && !level.isEmpty ? ThinkingLevel.parse(level) : nil
    }

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

    public var canStart: Bool {
        hasLoaded && !isStarting && !coordinator.isEmpty && thinkingConflict == nil
    }

    /// Start the work session (in a new workstream when chosen), then apply the
    /// implementer/reviewer models that differ from the defaults and the picked
    /// implementer/reviewer thinking levels. Returns nil
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
                    coordinatorModel: coordinatorOverride,
                    coordinatorThinking: coordinatorThinking?.wireValue ?? "",
                    preset: "", taskID: taskID,
                    images: []
                ).sessionID
            } else {
                sessionID = try await source.startSession(
                    project: project, mode: "work", prompt: prompt,
                    coordinatorModel: coordinatorOverride,
                    coordinatorThinking: coordinatorThinking?.wireValue ?? "", images: [])
            }
        } catch {
            handle(error)
            return nil
        }
        errorMessage = nil
        let implementerOverride = implementer == defaults.implementer ? "" : implementer
        let reviewersOverride = reviewers == defaults.reviewers ? [] : reviewers
        guard !sessionID.isEmpty else { return StartedWork(sessionID: sessionID, roleError: nil) }
        do {
            if !implementerOverride.isEmpty || !reviewersOverride.isEmpty {
                try await source.setRoleConfig(
                    sessionId: sessionID, coordinator: "",
                    implementer: implementerOverride, reviewers: reviewersOverride)
            }
            // Levels attach to models, so these follow the role change.
            if let implementerThinking {
                try await source.setThinking(
                    sessionId: sessionID, level: implementerThinking.wireValue,
                    role: ThinkingRole.implementer.wireValue)
            }
            if let reviewersThinking {
                try await source.setThinking(
                    sessionId: sessionID, level: reviewersThinking.wireValue,
                    role: ThinkingRole.reviewers.wireValue)
            }
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
