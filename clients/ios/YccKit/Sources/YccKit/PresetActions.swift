import Foundation
import Observation
import YccProto

/// Where the daemon says a preset is offered (`ListModes` `Preset.placement`).
/// Clients place presets by this, never by preset name (mirrors the web
/// client's `PLACEMENT`).
public enum PresetPlacement {
    /// New session for a project that needs onboarding; re-onboarding from
    /// the project overflow and the backlog.
    public static let onboarding = "onboarding"
    /// A contextual action on the project (its overflow menu).
    public static let project = "project"
    /// An action on the backlog screen.
    public static let backlog = "backlog"
    /// An action on the memory screen.
    public static let memory = "memory"

    /// Presets the daemon places at any of `placements`, in the order of
    /// `placements`, then the daemon's.
    public static func presets(_ presets: [Ycc_V1_Preset], at placements: [String]) -> [Ycc_V1_Preset] {
        placements.flatMap { placement in presets.filter { $0.placement == placement } }
    }

    /// The label of a contextual preset action: an onboarding preset for a
    /// project that is already onboarded re-runs onboarding. `nil` means the
    /// daemon did not report the project's onboarding status.
    public static func actionLabel(_ preset: Ycc_V1_Preset, needsOnboarding: Bool?) -> String {
        if preset.placement == onboarding, needsOnboarding == false { return "Re-onboard" }
        return preset.title.isEmpty ? preset.name : preset.title
    }
}

extension Ycc_V1_ProjectInfo {
    /// Whether the project needs onboarding, or `nil` when an older daemon
    /// did not say (for ``PresetPlacement/actionLabel(_:needsOnboarding:)``).
    public var needsOnboardingStatus: Bool? {
        hasNeedsOnboarding ? needsOnboarding : nil
    }
}

/// What ``PresetActionsModel`` reads and drives. ``YccClient`` conforms.
public protocol PresetActionSource: Sendable {
    func listModes() async throws -> Ycc_V1_ListModesResponse
    func startPresetSession(project: String, preset: String, mode: String) async throws -> String
}

extension YccClient: PresetActionSource {}

/// The contextual preset actions of a project screen (the project overflow's
/// onboarding and docs check, the backlog's grooming and re-onboarding): loads
/// the daemon's presets and starts one with its own opening prompt.
@MainActor
@Observable
public final class PresetActionsModel {
    public private(set) var presets: [Ycc_V1_Preset] = []
    public private(set) var isStarting = false
    /// The last start failure, for an alert; the view clears it.
    public var errorMessage: String?
    /// Set when a load/start failed with ``YccError/unauthorized``.
    public private(set) var unauthorized = false

    private let source: PresetActionSource

    /// - Parameter cache: a cached new-session catalog seeds the presets so
    ///   the actions are there before ``load()`` answers.
    public init(source: PresetActionSource, cache: AppDataCache? = nil) {
        self.source = source
        if let catalog = cache?.value(.newSessionCatalog, as: NewSessionModel.Catalog.self) {
            presets = catalog.presets
        }
    }

    /// Presets placed at any of `placements` (see ``PresetPlacement``).
    public func presets(at placements: [String]) -> [Ycc_V1_Preset] {
        PresetPlacement.presets(presets, at: placements)
    }

    /// Fetch the presets. A failure keeps what is shown (the actions are
    /// conveniences), except that unauthorized is surfaced.
    public func load() async {
        do {
            presets = try await source.listModes().presets
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            // Keep the cached presets, if any.
        }
    }

    /// Start `preset` in `project` with the daemon's opening prompt (and any
    /// preset model binding). Returns the new session id, or `nil` after
    /// recording the failure.
    public func start(_ preset: Ycc_V1_Preset, project: String) async -> String? {
        guard !isStarting else { return nil }
        isStarting = true
        defer { isStarting = false }
        do {
            return try await source.startPresetSession(project: project, preset: preset.name, mode: preset.mode)
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            let message = (error as? YccError)?.displayMessage ?? error.localizedDescription
            errorMessage = "Couldn’t start \(PresetPlacement.actionLabel(preset, needsOnboarding: nil)): \(message)"
        }
        return nil
    }
}
