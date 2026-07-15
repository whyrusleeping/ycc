import Foundation
import Observation
import YccProto

/// The data source a ``NewSessionModel`` reads from and drives. Abstracting it
/// behind a protocol lets the start/resume logic be unit-tested headlessly with
/// an in-memory mock — no network, no simulator. ``YccClient`` is the production
/// conformer. (Mirrors the ``SessionListSource`` pattern.)
public protocol NewSessionSource: Sendable {
    /// List the daemon's session modes + presets (drives the pickers).
    func listModes() async throws -> (modes: [Ycc_V1_Mode], presets: [Ycc_V1_Preset])
    /// List the daemon's registered projects (drives the project picker).
    func listProjects() async throws -> [Ycc_V1_ProjectInfo]
    /// Start a new session; returns its id to subscribe from seq 0.
    func startSession(
        project: String, mode: String, prompt: String, interactionLevel: String
    ) async throws -> String
    /// Re-open a persisted session on its existing log; returns its id.
    func resumeSession(project: String, sessionId: String) async throws -> String
}

extension YccClient: NewSessionSource {}

/// A session's interaction level (spec §11): how much autonomy the agent has
/// before it must consult the human. Kept as a typed enum so the picker is
/// exhaustive and the wire value (`interactive` | `judgement` | `autonomous`)
/// is a single source of truth.
public enum InteractionLevel: String, CaseIterable, Sendable, Identifiable {
    /// Asks before most actions — every `ask_user` gate blocks for a human.
    case interactive
    /// Asks only for judgement calls (the daemon default).
    case judgement
    /// Runs to completion without asking (auto-answers gates).
    case autonomous

    public var id: String { rawValue }

    /// A human-facing label for the picker.
    public var title: String {
        switch self {
        case .interactive: return "Interactive"
        case .judgement: return "Judgement"
        case .autonomous: return "Autonomous"
        }
    }

    /// A one-line description of what the level does.
    public var detail: String {
        switch self {
        case .interactive: return "Asks before most actions"
        case .judgement: return "Asks only for judgement calls"
        case .autonomous: return "Runs to completion without asking"
        }
    }
}

/// Client-side memory of the last-used mode/level/project so a returning user
/// gets sensible defaults (docs/design/ios-client.md §6 phase 2 step 5).
/// Abstracted behind a protocol so tests can stub it without touching
/// `UserDefaults`.
public protocol SessionDefaultsStore: AnyObject {
    var lastMode: String? { get set }
    var lastInteractionLevel: String? { get set }
    var lastProject: String? { get set }
}

/// The production ``SessionDefaultsStore``: `UserDefaults`-backed.
public final class UserDefaultsSessionDefaults: SessionDefaultsStore {
    private let defaults: UserDefaults
    private static let modeKey = "ycc.newSession.mode"
    private static let levelKey = "ycc.newSession.interactionLevel"
    private static let projectKey = "ycc.newSession.project"

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    public var lastMode: String? {
        get { defaults.string(forKey: Self.modeKey) }
        set { defaults.set(newValue, forKey: Self.modeKey) }
    }
    public var lastInteractionLevel: String? {
        get { defaults.string(forKey: Self.levelKey) }
        set { defaults.set(newValue, forKey: Self.levelKey) }
    }
    public var lastProject: String? {
        get { defaults.string(forKey: Self.projectKey) }
        set { defaults.set(newValue, forKey: Self.projectKey) }
    }
}

/// Drives the "new session" flow: loads ``ListModes`` + ``ListProjects``, holds
/// the mode / interaction-level / project selections and the prompt draft,
/// validates, and starts the session (returning its id so the view can navigate
/// straight into the live stream). Last-used selections are remembered via an
/// injectable ``SessionDefaultsStore``. `@MainActor` because it publishes
/// observable UI state; the source is injected so the logic is testable
/// headlessly.
@MainActor
@Observable
public final class NewSessionModel {
    /// Available modes (name/title/description) from the last successful load.
    public private(set) var modes: [Ycc_V1_Mode] = []
    /// Available presets (mode + tailored opening prompt).
    public private(set) var presets: [Ycc_V1_Preset] = []
    /// Registered projects; drives the project picker.
    public private(set) var projects: [Ycc_V1_ProjectInfo] = []

    /// The selected mode name (e.g. `work`/`pm`/`chat`).
    public var selectedMode: String = ""
    /// The selected interaction level.
    public var interactionLevel: InteractionLevel = .judgement
    /// The selected project (`""` => daemon default workspace).
    public var selectedProject: String = ""
    /// The multiline prompt composer draft.
    public var prompt: String = ""

    public private(set) var isLoading = false
    public private(set) var isStarting = false
    public private(set) var errorMessage: String?
    /// Set when a load/start failed with ``YccError/unauthorized`` — the view
    /// routes back to the connect screen via `AppModel.handleUnauthorized`.
    public private(set) var unauthorized = false

    private let source: NewSessionSource
    private let defaults: SessionDefaultsStore

    /// - Parameter initialProject: when non-nil, the project to preselect
    ///   (e.g. the landing screen's current filter, `""` = default workspace) —
    ///   it takes precedence over the remembered last-used project so a new
    ///   session lands in the workspace the user is looking at.
    public init(
        source: NewSessionSource,
        defaults: SessionDefaultsStore = UserDefaultsSessionDefaults(),
        initialProject: String? = nil
    ) {
        self.source = source
        self.defaults = defaults
        // Recall last-used selections up front so the pickers open on them.
        self.selectedMode = defaults.lastMode ?? ""
        self.interactionLevel = defaults.lastInteractionLevel
            .flatMap(InteractionLevel.init(rawValue:)) ?? .judgement
        self.selectedProject = initialProject ?? defaults.lastProject ?? ""
    }

    /// Whether the project picker is worth showing: whenever any project is
    /// registered, since the picker always offers the implicit "Default"
    /// workspace (`""`) too — even a single registered project gives two choices.
    public var showsProjectPicker: Bool { !projects.isEmpty }

    /// Whether the prompt may be left empty for the selected mode. Mirrors the
    /// TUI: plain `work` mode starts without a prompt — the agent picks up the
    /// next ready backlog task itself.
    public var promptIsOptional: Bool { selectedMode == "work" }

    /// A start is allowed once a mode is chosen, the prompt is non-empty
    /// (unless the mode makes it optional), and no start is already in flight.
    public var canStart: Bool {
        !isStarting
            && !selectedMode.isEmpty
            && (promptIsOptional
                || !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
    }

    /// The currently-selected mode's description, if any (shown under the picker).
    public var selectedModeDescription: String? {
        modes.first { $0.name == selectedMode }?.description_p.nilIfEmpty
    }

    /// Load modes + projects. Falls back to the first available mode when no
    /// remembered mode is still valid, so the picker is never empty. Unauthorized
    /// bubbles up via ``unauthorized`` for the view to handle.
    public func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            async let modesCall = source.listModes()
            async let projectList = source.listProjects()
            let ((loadedModes, loadedPresets), loadedProjects) = try await (modesCall, projectList)
            modes = loadedModes
            presets = loadedPresets
            projects = loadedProjects
            // Keep a valid mode selected: honour the remembered one if it still
            // exists, otherwise default to the first mode.
            if selectedMode.isEmpty || !loadedModes.contains(where: { $0.name == selectedMode }) {
                selectedMode = loadedModes.first?.name ?? ""
            }
            // Drop a remembered project that no longer exists (keep "" default).
            if !selectedProject.isEmpty,
               !loadedProjects.contains(where: { $0.name == selectedProject }) {
                selectedProject = ""
            }
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch let YccError.rpc(message) {
            errorMessage = message
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    /// Apply a preset: adopt its mode and seed the composer with its opening
    /// prompt (the user can then edit before starting).
    public func apply(preset: Ycc_V1_Preset) {
        selectedMode = preset.mode
        prompt = preset.openingPrompt
    }

    /// Start the session. On success remembers the selections and returns the new
    /// session id (to `Subscribe` from seq 0). On failure sets ``errorMessage`` /
    /// ``unauthorized`` and returns `nil`.
    public func start() async -> String? {
        guard canStart else { return nil }
        isStarting = true
        defer { isStarting = false }
        let trimmedPrompt = prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        do {
            let sessionId = try await source.startSession(
                project: selectedProject,
                mode: selectedMode,
                prompt: trimmedPrompt,
                interactionLevel: interactionLevel.rawValue)
            rememberSelections()
            errorMessage = nil
            return sessionId
        } catch YccError.unauthorized {
            unauthorized = true
            return nil
        } catch let YccError.rpc(message) {
            errorMessage = message
            return nil
        } catch let YccError.notFound(message) {
            errorMessage = message
            return nil
        } catch let YccError.failedPrecondition(message) {
            errorMessage = message
            return nil
        } catch {
            errorMessage = error.localizedDescription
            return nil
        }
    }

    private func rememberSelections() {
        defaults.lastMode = selectedMode
        defaults.lastInteractionLevel = interactionLevel.rawValue
        defaults.lastProject = selectedProject
    }
}

private extension String {
    var nilIfEmpty: String? { isEmpty ? nil : self }
}
