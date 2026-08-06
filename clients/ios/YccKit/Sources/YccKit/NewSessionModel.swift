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
    /// Configured logical models + the current default role assignment
    /// (`ListModels`); drives the optional model picker.
    func listModels() async throws -> Ycc_V1_ListModelsResponse
    /// Start a new session; returns its id to subscribe from seq 0.
    /// `coordinatorModel` is empty for "use the configured default"; `images`
    /// attaches pictures to the opening prompt (spec §12).
    func startSession(
        project: String, mode: String, prompt: String, coordinatorModel: String,
        images: [MessageImage]
    ) async throws -> String
    /// Re-open a persisted session on its existing log; returns its id.
    func resumeSession(project: String, sessionId: String) async throws -> String
}

extension YccClient: NewSessionSource {}

/// Client-side memory of the last-used mode/project so a returning user
/// gets sensible defaults (docs/design/ios-client.md §6 phase 2 step 5).
/// Abstracted behind a protocol so tests can stub it without touching
/// `UserDefaults`.
public protocol SessionDefaultsStore: AnyObject {
    var lastMode: String? { get set }
    var lastProject: String? { get set }
}

/// The production ``SessionDefaultsStore``: `UserDefaults`-backed.
public final class UserDefaultsSessionDefaults: SessionDefaultsStore {
    private let defaults: UserDefaults
    private static let modeKey = "ycc.newSession.mode"
    private static let projectKey = "ycc.newSession.project"

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    public var lastMode: String? {
        get { defaults.string(forKey: Self.modeKey) }
        set { defaults.set(newValue, forKey: Self.modeKey) }
    }
    public var lastProject: String? {
        get { defaults.string(forKey: Self.projectKey) }
        set { defaults.set(newValue, forKey: Self.projectKey) }
    }
}

/// Drives the "new session" flow: loads ``ListModes`` + ``ListProjects`` +
/// ``ListModels``, holds the mode / project / model selections and the prompt
/// draft, validates, and starts the session (returning its id so the view can
/// navigate straight into the live stream). Last-used mode/project are remembered
/// via an injectable ``SessionDefaultsStore``; the model pick is a per-session
/// override and deliberately not remembered. `@MainActor` because it publishes
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
    /// Configured logical models; drives the optional model picker.
    public private(set) var models: [Ycc_V1_ModelInfo] = []
    /// The daemon's configured default coordinator model, shown as the "Default"
    /// option's subtitle so the user can see what they'd get without choosing.
    public private(set) var defaultModel: String = ""

    /// The selected mode name (e.g. `work`/`pm`/`chat`).
    public var selectedMode: String = ""
    /// The selected registered project. Empty means no choice has been made yet.
    public var selectedProject: String = ""
    /// The coordinator model override for this session. Empty (the default) means
    /// "use the daemon's configured coordinator" — picking one here affects ONLY
    /// the session being started; the persisted role defaults are untouched.
    public var selectedModel: String = ""
    /// The multiline prompt composer draft.
    public var prompt: String = ""
    /// Pictures attached to the opening prompt (at most
    /// ``PictureAttachments/maxCount``). They travel with `StartSession`, so the
    /// agent sees the screenshot on its FIRST turn rather than a turn later.
    public var images: [MessageImage] = []

    public private(set) var isLoading = false
    public private(set) var isStarting = false
    public private(set) var errorMessage: String?
    /// Set when a load/start failed with ``YccError/unauthorized`` — the view
    /// routes back to the connect screen via `AppModel.handleUnauthorized`.
    public private(set) var unauthorized = false

    private let source: NewSessionSource
    private let defaults: SessionDefaultsStore

    /// - Parameter initialProject: when non-nil, the named project to preselect.
    ///   It takes precedence over the remembered last-used project so a new
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
        self.selectedProject = initialProject ?? defaults.lastProject ?? ""
    }

    /// The picker is useful only when there is a real choice.
    public var showsProjectPicker: Bool { projects.count > 1 }

    /// The model chip is worth showing only when there is something to pick
    /// between (a single configured model leaves nothing to choose).
    public var showsModelPicker: Bool { models.count > 1 }

    /// The label for the current model choice: the picked model, or the daemon's
    /// default coordinator marked as such, or a bare prompt when unknown.
    public var selectedModelTitle: String {
        if !selectedModel.isEmpty { return selectedModel }
        return defaultModel.isEmpty ? "Model" : "\(defaultModel) (default)"
    }

    /// Whether the prompt may be left empty for the selected mode. Mirrors the
    /// TUI: plain `work` mode starts without a prompt — the agent picks up the
    /// next ready backlog task itself.
    public var promptIsOptional: Bool { selectedMode == "work" }

    /// A start is allowed once a mode is chosen, the prompt carries something
    /// (text, or a picture that speaks for itself, unless the mode makes the
    /// prompt optional), and no start is already in flight.
    public var canStart: Bool {
        !isStarting
            && !selectedMode.isEmpty
            && (promptIsOptional
                || !images.isEmpty
                || !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
    }

    /// The currently-selected mode's description, if any (shown under the picker).
    public var selectedModeDescription: String? {
        modes.first { $0.name == selectedMode }?.description_p.nilIfEmpty
    }

    /// Load modes + projects (+ the optional model list). Falls back to the first
    /// available mode when no remembered mode is still valid, so the picker is
    /// never empty. Unauthorized bubbles up via ``unauthorized`` for the view to
    /// handle.
    public func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            async let modesCall = source.listModes()
            async let projectList = source.listProjects()
            async let modelList = source.listModels()
            let ((loadedModes, loadedPresets), loadedProjects) = try await (modesCall, projectList)
            // The model picker is a convenience: a daemon that fails ListModels
            // must not block starting a session, so its failure is tolerated and
            // simply leaves the chip hidden.
            let loadedModels = try? await modelList
            modes = loadedModes
            presets = loadedPresets
            projects = loadedProjects
            models = loadedModels?.models ?? []
            defaultModel = loadedModels?.coordinator ?? ""
            // Never keep an override pointing at a model that is no longer
            // configured — fall back to the daemon's default.
            if !selectedModel.isEmpty, !models.contains(where: { $0.name == selectedModel }) {
                selectedModel = ""
            }
            // Keep a valid mode selected: honour the remembered one if it still
            // exists, otherwise default to the first mode.
            if selectedMode.isEmpty || !loadedModes.contains(where: { $0.name == selectedMode }) {
                selectedMode = loadedModes.first?.name ?? ""
            }
            // Drop a remembered project that no longer exists, then select the
            // sole project automatically. Never manufacture a "Default" choice.
            if !selectedProject.isEmpty,
               !loadedProjects.contains(where: { $0.name == selectedProject }) {
                selectedProject = ""
            }
            if selectedProject.isEmpty, loadedProjects.count == 1 {
                selectedProject = loadedProjects[0].name
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
                coordinatorModel: selectedModel,
                images: images)
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
        // Mode/project are sticky conveniences; the model choice deliberately is
        // NOT — it is a one-off override for this session, so the next composer
        // opens back on the daemon's configured default.
        defaults.lastMode = selectedMode
        defaults.lastProject = selectedProject
    }
}

private extension String {
    var nilIfEmpty: String? { isEmpty ? nil : self }
}
