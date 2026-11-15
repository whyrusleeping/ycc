import Foundation
import Observation
import YccProto

/// The data source a ``SessionSettingsModel`` reads from and drives. Abstracted
/// behind a protocol so the seeding / apply logic is unit-testable headlessly
/// with an in-memory mock — no network, no simulator. ``YccClient`` is the
/// production conformer. (Mirrors the ``UsageSource`` / ``NewSessionSource``
/// patterns.)
public protocol SessionSettingsSource: Sendable {
    /// Configured logical models + this live session's role assignments and each
    /// assigned model's thinking level (`ListModels`); seeds pickers with reality.
    func listModels(sessionId: String) async throws -> Ycc_V1_ListModelsResponse
    /// Reassign per-role logical models (`SetRoleConfig`); empty fields unchanged.
    func setRoleConfig(
        sessionId: String, coordinator: String, implementer: String, reviewers: [String]
    ) async throws
    /// Change assigned model thinking (`SetThinking`); empty role = all role models.
    func setThinking(sessionId: String, level: String, role: String) async throws
}

extension YccClient: SessionSettingsSource {}

/// A reasoning/effort level. The wire value is the single
/// source of truth for both the picker and the `SetThinking` request. `off`
/// disables reasoning; the effort levels enable adaptive thinking at that effort.
public enum ThinkingLevel: String, CaseIterable, Sendable, Identifiable {
    case off
    case low
    case medium
    case high
    case xhigh
    case max

    public var id: String { rawValue }

    /// The value sent in `SetThinkingRequest.level`.
    public var wireValue: String { rawValue }

    /// A human-facing label for the picker.
    public var title: String {
        switch self {
        case .off: return "Off"
        case .low: return "Low"
        case .medium: return "Medium"
        case .high: return "High"
        case .xhigh: return "X-High"
        case .max: return "Max"
        }
    }

    /// Parse a wire value, defaulting to ``medium`` for an unknown/empty string so
    /// a picker is never left without a selection.
    public static func parse(_ value: String) -> ThinkingLevel {
        ThinkingLevel(rawValue: value.lowercased()) ?? .medium
    }
}

/// The role whose assigned model(s) a thinking-level change targets. `all` maps
/// to an empty wire role (all assigned models); the
/// others map to their name. Kept typed so the scope picker is exhaustive.
public enum ThinkingRole: String, CaseIterable, Sendable, Identifiable {
    case all
    case coordinator
    case implementer
    case reviewers

    public var id: String { rawValue }

    /// The value sent in `SetThinkingRequest.role` (`""` = all assigned models).
    public var wireValue: String { self == .all ? "" : rawValue }

    /// A human-facing label for the picker.
    public var title: String {
        switch self {
        case .all: return "All roles"
        case .coordinator: return "Coordinator"
        case .implementer: return "Implementer"
        case .reviewers: return "Reviewers"
        }
    }
}

/// Drives the per-session settings sheet, the phone analog of the TUI settings
/// overlay. Seeds its pickers from ``ListModels`` (role model assignments + each
/// assigned model's thinking), then applies each change against the live session via
/// the injected ``SessionSettingsSource`` — surfacing the daemon's error verbatim
/// on an invalid combination. Every change applies to this session only.
/// `@MainActor` because it publishes observable UI state; the source is
/// injected so the logic is testable headlessly.
@MainActor
@Observable
public final class SessionSettingsModel {
    /// Available logical models (drives the role pickers).
    public private(set) var models: [Ycc_V1_ModelInfo] = []

    /// The selected coordinator model (`""` until seeded / when unset).
    public var coordinator: String = ""
    /// The selected implementer model.
    public var implementer: String = ""
    /// The selected reviewer models (multi-select).
    public var reviewers: [String] = []

    /// Each role's thinking level in this session, as last known from the
    /// daemon (or just picked). Roles sharing a model share its level.
    public private(set) var coordinatorThinking: ThinkingLevel = .medium
    public private(set) var implementerThinking: ThinkingLevel = .medium
    public private(set) var reviewersThinking: ThinkingLevel = .medium

    public private(set) var isLoading = false
    /// True while any apply is in flight (drives a progress indicator + disables
    /// controls to serialize changes).
    public private(set) var isApplying = false
    /// The daemon's error from the last failed load/apply, surfaced verbatim.
    public private(set) var errorMessage: String?
    /// Set when a call failed with ``YccError/unauthorized`` — the view routes
    /// back to the connect screen via `AppModel.handleUnauthorized`.
    public private(set) var unauthorized = false

    private let source: SessionSettingsSource
    private let sessionId: String

    // Committed (daemon-confirmed) values, so a failed apply can revert the
    // corresponding picker back to reality rather than lie about the state.
    private var committedCoordinator = ""
    private var committedImplementer = ""
    private var committedReviewers: [String] = []

    public init(source: SessionSettingsSource, sessionId: String) {
        self.source = source
        self.sessionId = sessionId
    }

    /// Load the model list and seed every picker from the daemon's CURRENT role
    /// assignments + assigned-model thinking. Unauthorized bubbles up via
    /// ``unauthorized`` for the view to handle.
    public func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let response = try await source.listModels(sessionId: sessionId)
            // Keep the complete list: the view creates role-specific enabled
            // choices while retaining each role's disabled current value for migration.
            models = response.models
            coordinator = response.coordinator
            implementer = response.implementer
            reviewers = response.reviewers
            committedCoordinator = response.coordinator
            committedImplementer = response.implementer
            committedReviewers = response.reviewers
            applyThinking(from: response)
            errorMessage = nil
        } catch {
            handle(error)
        }
    }

    private func applyThinking(from response: Ycc_V1_ListModelsResponse) {
        coordinatorThinking = ThinkingLevel.parse(response.coordinatorThinking)
        implementerThinking = ThinkingLevel.parse(response.implementerThinking)
        reviewersThinking = ThinkingLevel.parse(response.reviewersThinking)
    }

    /// Re-read the resolved levels after a change: a new model brings its own
    /// level, and a level change reaches every role sharing the model. A
    /// failure keeps the current values (the change itself succeeded).
    private func refreshThinking() async {
        guard let response = try? await source.listModels(sessionId: sessionId) else { return }
        applyThinking(from: response)
    }

    /// A role's known thinking level (`all` → the coordinator's).
    public func thinking(for role: ThinkingRole) -> ThinkingLevel {
        switch role {
        case .all, .coordinator: return coordinatorThinking
        case .implementer: return implementerThinking
        case .reviewers: return reviewersThinking
        }
    }

    /// The levels a role's selected model(s) support (the reviewers: any
    /// reviewer model's; the daemon clamps per model).
    public func thinkingLevels(for role: ThinkingRole) -> [ThinkingLevel] {
        switch role {
        case .all: return ThinkingLevel.supported(anyOf: [coordinator, implementer] + reviewers, in: models)
        case .coordinator: return ThinkingLevel.supported(named: coordinator, in: models)
        case .implementer: return ThinkingLevel.supported(named: implementer, in: models)
        case .reviewers: return ThinkingLevel.supported(anyOf: reviewers, in: models)
        }
    }

    private func setLocalThinking(_ level: ThinkingLevel, for role: ThinkingRole) {
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

    /// Apply the selected role assignment (`SetRoleConfig`). The full current
    /// selection is sent (the daemon treats empty fields as "leave unchanged").
    /// On failure reverts the pickers to the last committed values.
    public func applyRoleConfig() async {
        await apply {
            try await self.source.setRoleConfig(
                sessionId: self.sessionId,
                coordinator: self.coordinator,
                implementer: self.implementer,
                reviewers: self.reviewers)
            await self.refreshThinking()
        } onSuccess: {
            self.committedCoordinator = self.coordinator
            self.committedImplementer = self.implementer
            self.committedReviewers = self.reviewers
        } onFailure: {
            self.coordinator = self.committedCoordinator
            self.implementer = self.committedImplementer
            self.reviewers = self.committedReviewers
        }
    }

    /// Apply `level` to `role`'s model(s) in this session (`SetThinking` with
    /// the session id; `ycc.toml` is unchanged). The picker shows the level at
    /// once and reverts on failure.
    public func applyThinking(_ level: ThinkingLevel, for role: ThinkingRole) async {
        await applyThinking(level, for: role, previous: (coordinatorThinking, implementerThinking, reviewersThinking))
    }

    private func applyThinking(
        _ level: ThinkingLevel, for role: ThinkingRole,
        previous: (ThinkingLevel, ThinkingLevel, ThinkingLevel)
    ) async {
        await apply {
            self.setLocalThinking(level, for: role)
            try await self.source.setThinking(
                sessionId: self.sessionId, level: level.wireValue, role: role.wireValue)
            await self.refreshThinking()
        } onSuccess: {
        } onFailure: {
            (self.coordinatorThinking, self.implementerThinking, self.reviewersThinking) = previous
        }
    }

    // MARK: - User picks
    //
    // The view binds its pickers through these instead of observing value
    // changes: ``load()`` seeds the same properties programmatically, and an
    // `onChange` observer cannot tell a seed from a user pick — it used to
    // fire a spurious `SetThinking` and a redundant `SetRoleConfig` every time
    // the sheet opened. Each method updates the picker synchronously and
    // returns the apply task (nil when nothing changed).

    /// The user picked a thinking level for a role.
    @discardableResult
    public func chooseThinking(_ level: ThinkingLevel, for role: ThinkingRole) -> Task<Void, Never>? {
        guard level != thinking(for: role), !isApplying else { return nil }
        let previous = (coordinatorThinking, implementerThinking, reviewersThinking)
        setLocalThinking(level, for: role)
        return Task { @MainActor [weak self] in
            await self?.applyThinking(level, for: role, previous: previous)
        }
    }

    /// The user picked a coordinator model.
    @discardableResult
    public func chooseCoordinator(_ name: String) -> Task<Void, Never>? {
        guard name != coordinator else { return nil }
        coordinator = name
        return Task { @MainActor [weak self] in await self?.applyRoleConfig() }
    }

    /// The user picked an implementer model.
    @discardableResult
    public func chooseImplementer(_ name: String) -> Task<Void, Never>? {
        guard name != implementer else { return nil }
        implementer = name
        return Task { @MainActor [weak self] in await self?.applyRoleConfig() }
    }

    /// Whether a reviewer is currently selected (for the multi-select UI).
    public func isReviewerSelected(_ name: String) -> Bool {
        reviewers.contains(name)
    }

    /// Toggle a reviewer in the selection (does not apply — call
    /// ``applyRoleConfig()`` after editing the set).
    public func toggleReviewer(_ name: String) {
        if let idx = reviewers.firstIndex(of: name) {
            reviewers.remove(at: idx)
        } else {
            reviewers.append(name)
        }
    }

    // MARK: - Apply plumbing

    private func apply(
        _ body: @escaping () async throws -> Void,
        onSuccess: @escaping () -> Void,
        onFailure: @escaping () -> Void
    ) async {
        guard !isApplying else { return }
        isApplying = true
        defer { isApplying = false }
        do {
            try await body()
            onSuccess()
            errorMessage = nil
        } catch {
            onFailure()
            handle(error)
        }
    }

    private func handle(_ error: Error) {
        switch error {
        case YccError.unauthorized:
            unauthorized = true
        case let YccError.rpc(message):
            errorMessage = message
        case let YccError.notFound(message):
            errorMessage = message
        case let YccError.failedPrecondition(message):
            errorMessage = message
        default:
            errorMessage = error.localizedDescription
        }
    }
}
