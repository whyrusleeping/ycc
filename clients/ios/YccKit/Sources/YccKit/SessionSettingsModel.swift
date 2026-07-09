import Foundation
import Observation
import YccProto

/// The data source a ``SessionSettingsModel`` reads from and drives. Abstracted
/// behind a protocol so the seeding / apply logic is unit-testable headlessly
/// with an in-memory mock — no network, no simulator. ``YccClient`` is the
/// production conformer. (Mirrors the ``UsageSource`` / ``NewSessionSource``
/// patterns.)
public protocol SessionSettingsSource: Sendable {
    /// Configured logical models + CURRENT per-role assignments and per-role
    /// thinking levels (`ListModels`); seeds the pickers with reality.
    func listModels() async throws -> Ycc_V1_ListModelsResponse
    /// Change the session's interaction level (`SetInteractionLevel`).
    func setInteractionLevel(sessionId: String, level: String) async throws
    /// Reassign per-role logical models (`SetRoleConfig`); empty fields unchanged.
    func setRoleConfig(
        sessionId: String, coordinator: String, implementer: String, reviewers: [String]
    ) async throws
    /// Change a thinking/effort level (`SetThinking`); empty role = all roles.
    func setThinking(sessionId: String, level: String, role: String) async throws
}

extension YccClient: SessionSettingsSource {}

/// A reasoning/effort level (spec §7.4/§18.2). The wire value is the single
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

/// The role a thinking-level change targets (spec §7.4/§18.2). `all` maps to an
/// empty wire role (the daemon applies it to every role); the others map to
/// their name. Kept typed so the scope picker is exhaustive.
public enum ThinkingRole: String, CaseIterable, Sendable, Identifiable {
    case all
    case coordinator
    case implementer
    case reviewers

    public var id: String { rawValue }

    /// The value sent in `SetThinkingRequest.role` (`""` = all roles).
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

/// Drives the per-session settings sheet (spec §18.2 analog; docs/design/
/// ios-client.md §6 phase 3 step 8): the phone analog of the TUI settings
/// overlay. Seeds its pickers from ``ListModels`` (per-role model assignments +
/// per-role thinking) and the session's current interaction level (threaded from
/// the live projection), then applies each change against the live session via
/// the injected ``SessionSettingsSource`` — surfacing the daemon's error verbatim
/// on an invalid combination. `@MainActor` because it publishes observable UI
/// state; the source is injected so the logic is testable headlessly.
@MainActor
@Observable
public final class SessionSettingsModel {
    /// Available logical models (drives the role pickers).
    public private(set) var models: [Ycc_V1_ModelInfo] = []

    /// The selected interaction level (bound to the picker).
    public var interactionLevel: InteractionLevel
    /// The selected coordinator model (`""` until seeded / when unset).
    public var coordinator: String = ""
    /// The selected implementer model.
    public var implementer: String = ""
    /// The selected reviewer models (multi-select).
    public var reviewers: [String] = []
    /// The scope a thinking change targets (all/coordinator/implementer/reviewers).
    public var thinkingRole: ThinkingRole = .all
    /// The selected thinking level for ``thinkingRole``.
    public var thinkingLevel: ThinkingLevel = .medium

    /// Per-role thinking levels last known from the daemon, so switching the
    /// scope picker reflects that role's current level.
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
    private var committedInteractionLevel: InteractionLevel
    private var committedCoordinator = ""
    private var committedImplementer = ""
    private var committedReviewers: [String] = []

    public init(
        source: SessionSettingsSource,
        sessionId: String,
        currentInteractionLevel: String? = nil
    ) {
        self.source = source
        self.sessionId = sessionId
        let level = currentInteractionLevel
            .flatMap(InteractionLevel.init(rawValue:)) ?? .judgement
        self.interactionLevel = level
        self.committedInteractionLevel = level
    }

    /// Load the model list and seed every picker from the daemon's CURRENT
    /// per-role assignments + per-role thinking. The interaction level is seeded
    /// at init from the live projection. Unauthorized bubbles up via
    /// ``unauthorized`` for the view to handle.
    public func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let response = try await source.listModels()
            models = response.models
            coordinator = response.coordinator
            implementer = response.implementer
            reviewers = response.reviewers
            committedCoordinator = response.coordinator
            committedImplementer = response.implementer
            committedReviewers = response.reviewers
            coordinatorThinking = ThinkingLevel.parse(response.coordinatorThinking)
            implementerThinking = ThinkingLevel.parse(response.implementerThinking)
            reviewersThinking = ThinkingLevel.parse(response.reviewersThinking)
            // Seed the thinking picker for the current scope (default: all →
            // coordinator's level as a representative starting point).
            thinkingLevel = thinkingLevelFor(thinkingRole)
            errorMessage = nil
        } catch {
            handle(error)
        }
    }

    /// The known thinking level for a role scope (`all` → coordinator's, as a
    /// representative). Drives the picker when the scope changes.
    public func thinkingLevelFor(_ role: ThinkingRole) -> ThinkingLevel {
        switch role {
        case .all, .coordinator: return coordinatorThinking
        case .implementer: return implementerThinking
        case .reviewers: return reviewersThinking
        }
    }

    /// Reflect the current-per-role thinking level when the scope picker changes.
    public func selectThinkingRole(_ role: ThinkingRole) {
        thinkingRole = role
        thinkingLevel = thinkingLevelFor(role)
    }

    /// Whether applying `level` at `role` scope would change anything. For the
    /// `all` scope every role is checked, so unifying divergent per-role levels
    /// is never suppressed.
    private func needsThinkingApply(role: ThinkingRole, level: ThinkingLevel) -> Bool {
        switch role {
        case .all:
            return level != coordinatorThinking
                || level != implementerThinking
                || level != reviewersThinking
        default:
            return level != thinkingLevelFor(role)
        }
    }

    /// Apply the selected interaction level (`SetInteractionLevel`). On failure
    /// reverts the picker to the last committed value and surfaces the error.
    public func applyInteractionLevel() async {
        guard interactionLevel != committedInteractionLevel else { return }
        await apply {
            try await self.source.setInteractionLevel(
                sessionId: self.sessionId, level: self.interactionLevel.rawValue)
        } onSuccess: {
            self.committedInteractionLevel = self.interactionLevel
        } onFailure: {
            self.interactionLevel = self.committedInteractionLevel
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

    /// Apply the selected thinking level for the selected scope (`SetThinking`).
    /// On success updates the cached per-role level(s) so the scope picker stays
    /// consistent.
    public func applyThinking() async {
        let role = thinkingRole
        let level = thinkingLevel
        // No-op when the level already matches the scope's known level — this also
        // suppresses the redundant apply that a scope-picker change would trigger
        // (selecting a scope seeds the level picker from that role's level). For
        // the `all` scope, any divergent role counts as a change so unifying the
        // roles at the coordinator's current level is not suppressed.
        guard needsThinkingApply(role: role, level: level) else { return }
        await apply {
            try await self.source.setThinking(
                sessionId: self.sessionId, level: level.wireValue, role: role.wireValue)
        } onSuccess: {
            switch role {
            case .all:
                self.coordinatorThinking = level
                self.implementerThinking = level
                self.reviewersThinking = level
            case .coordinator: self.coordinatorThinking = level
            case .implementer: self.implementerThinking = level
            case .reviewers: self.reviewersThinking = level
            }
        } onFailure: {
            self.thinkingLevel = self.thinkingLevelFor(role)
        }
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
