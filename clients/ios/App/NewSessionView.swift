import SwiftUI
import YccKit
import YccProto

/// The "new session" composer is styled as a blank chat rather than a settings form: the prompt composer sits
/// at the bottom with a send arrow (like the live session's input bar), and a row of
/// compact chips above it tucks away project and options. A new session starts in
/// the daemon's default mode (chat); the mode choice sits behind a trailing
/// advanced chip until a non-default mode is selected. The only suggestion card is
/// project onboarding, for a project that needs it; other presets live where they
/// apply (project overflow, backlog, memory). Sending calls
/// `StartSession` and hands the new session id back to the parent, which
/// navigates directly into the live streaming view (`Subscribe` from seq 0).
struct NewSessionView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    @State private var model: NewSessionModel
    /// Focus mirror for the composer field (a plain Bool rather than
    /// `@FocusState` because the field is the UIKit-backed `ComposerTextField`,
    /// which exists so an image on the clipboard can be pasted in-field).
    @State private var composerFocused = false
    /// Keyboard overlap for manual avoidance of the bottom chrome — see
    /// KeyboardObserver for why the automatic keyboard safe area is not used.
    @StateObject private var keyboard = KeyboardObserver()
    /// Whether the "add project" sheet is shown (from the project chip).
    @State private var showAddProject = false
    /// Pictures staged for the OPENING prompt (shared composer affordances in
    /// `PictureComposer.swift`). They are mirrored into `model.images` so the
    /// send button unlocks on a picture-only draft.
    @State private var pictures: [DraftPicture] = []
    @State private var loadingPictures = false
    /// A picture-loading failure (the model owns only RPC errors).
    @State private var pictureError: String?

    /// Called with (sessionID, project, title) once a session starts
    /// successfully. The parent dismisses the sheet and pushes the live view;
    /// `title` is the prompt's first line (the list titles sessions by their
    /// opening prompt too), or empty for a prompt-less start.
    private let onStarted: (String, String, String) -> Void

    /// - Parameter initialProject: the project to preselect (the landing
    ///   screen's current named-project filter). Overrides the
    ///   remembered last-used project so the session starts where the user is
    ///   currently looking.
    init(
        client: YccClient,
        initialProject: String? = nil,
        cache: AppDataCache? = nil,
        onStarted: @escaping (String, String, String) -> Void
    ) {
        // A cached catalog/project list renders the composer without a
        // spinner; load() still revalidates in the background.
        _model = State(initialValue: NewSessionModel(
            source: client, initialProject: initialProject, cache: cache))
        self.onStarted = onStarted
    }

    var body: some View {
        NavigationStack {
            Group {
                if model.isLoading && model.modes.isEmpty {
                    ProgressView()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    suggestionArea
                }
            }
            .navigationTitle("New session")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
            .safeAreaInset(edge: .bottom) { bottomChrome }
            // Manual keyboard avoidance, same as SessionView: the automatic
            // keyboard safe area under a bottom `safeAreaInset` can wedge
            // (FB13296535), leaving the composer floating with no keyboard.
            .ignoresSafeArea(.keyboard, edges: .bottom)
        }
        .task {
            // Focus first: typing never waits on the catalog round trip (the
            // composer field defers focus until it is in a window).
            composerFocused = true
            await model.load()
        }
        .sheet(isPresented: $showAddProject) {
            if let client = app.client {
                AddProjectView(client: client) { project in
                    Task {
                        // Reload so the chip lists the new project, then select
                        // it — the likely reason the user added it here.
                        await model.load(refreshProjects: true)
                        model.selectedProject = project.name
                    }
                }
                .trackedFlow("add_project")
            }
        }
        .onChange(of: model.unauthorized) { _, isUnauthorized in
            if isUnauthorized {
                dismiss()
                app.handleUnauthorized()
            }
        }
    }

    // MARK: - Empty space (preset suggestions)

    /// The would-be transcript area of the blank chat. The onboarding preset,
    /// when the selected project needs it, renders as a tappable suggestion card
    /// (picks its mode + seeds the composer); otherwise it stays a quiet
    /// placeholder.
    private var suggestionArea: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Spacer(minLength: 24)
                if !model.suggestedPresets.isEmpty {
                    Text("Suggestions")
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(.secondary)
                    ForEach(model.suggestedPresets, id: \.name) { preset in
                        presetCard(preset)
                    }
                }
            }
            .padding()
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .scrollDismissesKeyboard(.interactively)
    }

    private func presetCard(_ preset: Ycc_V1_Preset) -> some View {
        Button {
            model.apply(preset: preset)
            composerFocused = true
        } label: {
            VStack(alignment: .leading, spacing: 3) {
                Text(preset.title.isEmpty ? preset.name : preset.title)
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(.primary)
                if !preset.description_p.isEmpty {
                    Text(preset.description_p)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(12)
            .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 12))
        }
        .buttonStyle(.plain)
    }

    // MARK: - Bottom chrome (error + option chips + composer)

    private var bottomChrome: some View {
        VStack(spacing: 0) {
            errorBanner
            workstreamNote
            optionChips
            composer
        }
        // Ride the keyboard by measured overlap (KeyboardObserver); padding
        // sits inside the bar background so the material covers the gap while
        // the keyboard animates.
        .padding(.bottom, keyboard.overlap)
        .background(.bar)
    }

    @ViewBuilder
    private var errorBanner: some View {
        if let errorMessage = pictureError ?? model.errorMessage {
            HStack(spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(Theme.danger)
                Text(errorMessage)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.danger.opacity(0.08))
        }
    }

    /// What "Separate workstream" means, shown while it is on.
    @ViewBuilder
    private var workstreamNote: some View {
        if model.separateWorkstream {
            Text(model.selectedProject.isEmpty
                ? "Choose a project for the workstream."
                : "Gets its own git worktree and branch, so it can’t collide with other sessions; it merges back using the project’s integration setting.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 12)
                .padding(.top, 8)
        }
    }

    /// A compact, scrollable row of chips just above the composer: the project,
    /// the workstream toggle, the model when there is a choice, and the mode
    /// (with its description as a subtitle in the menu). The mode chip leads
    /// the row while a non-default mode is selected (e.g. after the onboarding
    /// card); in the default mode it folds into a trailing icon-only chip.
    private var optionChips: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                if !model.isDefaultMode {
                    modeChip
                }
                // Always shown: the chip menu is also the home of the "Add
                // project…" affordance, which must be reachable on
                // a daemon with no registered projects yet.
                projectChip
                // A workstream needs a registered project to branch from.
                if !model.projects.isEmpty {
                    workstreamChip
                    if model.separateWorkstream {
                        taskChip
                    }
                }
                if model.showsModelPicker {
                    modelChip
                }
                if model.isDefaultMode {
                    advancedModeChip
                }
            }
            .padding(.horizontal, 12)
            .padding(.top, 8)
            .padding(.bottom, 2)
        }
        // (Re)load the task choices whenever the workstream toggle turns on
        // or the project changes under it.
        .task(id: "\(model.separateWorkstream)|\(model.selectedProject)") {
            await model.loadWorkstreamTasks()
        }
    }

    /// The "Separate workstream" toggle, drawn as a chip that turns into the
    /// filled accent while on. Its visible label is just "Workstream" to keep
    /// the chip row narrow; the note above the chips explains it while on.
    private var workstreamChip: some View {
        Button {
            model.separateWorkstream.toggle()
            Analytics.action(
                "new_session.workstream",
                attrs: ["on": model.separateWorkstream ? "true" : "false"])
        } label: {
            HStack(spacing: 4) {
                Image(systemName: model.separateWorkstream ? "checkmark" : "arrow.triangle.branch")
                    .font(.caption2)
                Text("Workstream")
                    .font(.caption.weight(.medium))
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            .background(
                model.separateWorkstream ? Theme.accentFill : Color(.secondarySystemBackground),
                in: RoundedRectangle(cornerRadius: 6, style: .continuous))
            // Off, it matches the neighbouring menu chips' tinted labels.
            .foregroundStyle(model.separateWorkstream ? Theme.onAccentFill : Color.accentColor)
        }
        .buttonStyle(.plain)
        .disabled(model.isStarting)
        .accessibilityLabel("Separate workstream")
        .accessibilityAddTraits(model.separateWorkstream ? .isSelected : [])
        .accessibilityHint("Runs the session in its own git worktree and branch")
    }

    /// Workstream only: the optional backlog task it targets.
    private var taskChip: some View {
        @Bindable var model = model
        return Menu {
            Picker("Task", selection: $model.selectedTaskID) {
                Text("No task").tag("")
                ForEach(model.workstreamTaskChoices, id: \.id) { task in
                    Text("\(task.id) · \(task.title)").tag(task.id)
                }
            }
        } label: {
            chipLabel(
                model.chosenWorkstreamTask.map { "Task \($0.id)" } ?? "Task (optional)",
                systemImage: "checklist")
        }
        .disabled(model.isStarting)
    }

    private var modeChip: some View {
        Menu {
            modePicker
        } label: {
            chipLabel(selectedModeTitle, systemImage: "circle.grid.2x2")
        }
    }

    /// The mode choice while the draft is in the default mode: an icon-only
    /// chip at the end of the row (the web's collapsed "Advanced: mode").
    private var advancedModeChip: some View {
        Menu {
            modePicker
        } label: {
            Image(systemName: "slider.horizontal.3")
                .font(.caption.weight(.medium))
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 6, style: .continuous))
                .foregroundStyle(.primary)
        }
        .accessibilityLabel("Advanced: mode")
        .accessibilityValue(selectedModeTitle)
    }

    private var modePicker: some View {
        @Bindable var model = model
        return Picker("Mode", selection: $model.selectedMode) {
            ForEach(model.modes, id: \.name) { mode in
                if mode.description_p.isEmpty {
                    Text(mode.title.isEmpty ? mode.name : mode.title).tag(mode.name)
                } else {
                    Label {
                        Text(mode.title.isEmpty ? mode.name : mode.title)
                        Text(mode.description_p)
                    } icon: {
                        EmptyView()
                    }
                    .tag(mode.name)
                }
            }
        }
    }

    private var projectChip: some View {
        @Bindable var model = model
        return Menu {
            Picker("Project", selection: $model.selectedProject) {
                ForEach(model.projects, id: \.name) { project in
                    Text(project.name).tag(project.name)
                }
            }
            Divider()
            Button {
                showAddProject = true
            } label: {
                Label("Add project…", systemImage: "folder.badge.plus")
            }
        } label: {
            chipLabel(
                model.selectedProject.isEmpty ? "Choose project" : model.selectedProject,
                systemImage: "folder")
        }
    }

    /// The optional model chip: picks the coordinator model FOR THIS SESSION
    /// ONLY (the persisted role defaults, edited in Settings, are untouched).
    /// "Default" leaves the choice to the daemon's configuration.
    private var modelChip: some View {
        @Bindable var model = model
        return Menu {
            Picker("Model", selection: $model.selectedModel) {
                if model.defaultModelDisabled {
                    Text("Choose model…").tag("")
                } else {
                    Label {
                        Text("Default")
                        if !model.defaultModel.isEmpty {
                            Text(model.defaultModel)
                        }
                    } icon: {
                        EmptyView()
                    }
                    .tag("")
                }
                ForEach(model.models, id: \.name) { info in
                    Label {
                        Text(info.name)
                        Text(info.model.isEmpty ? info.backend : info.model)
                    } icon: {
                        EmptyView()
                    }
                    .tag(info.name)
                }
            }
        } label: {
            chipLabel(model.selectedModelTitle, systemImage: "cpu")
        }
    }

    private func chipLabel(_ title: String, systemImage: String) -> some View {
        HStack(spacing: 4) {
            Image(systemName: systemImage)
                .font(.caption2)
            Text(title)
                .font(.caption.weight(.medium))
            Image(systemName: "chevron.up.chevron.down")
                .font(.system(size: 8, weight: .semibold))
                .foregroundStyle(.tertiary)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 6, style: .continuous))
        .foregroundStyle(.primary)
    }

    private var selectedModeTitle: String {
        guard let mode = model.modes.first(where: { $0.name == model.selectedMode }) else {
            return model.selectedMode.isEmpty ? "Mode" : model.selectedMode
        }
        return mode.title.isEmpty ? mode.name : mode.title
    }

    /// The message-style composer: multiline field plus a send arrow (mirrors
    /// the live session's input bar), including the same Photos picker — an
    /// opening prompt may carry pictures, so a session about a
    /// screenshot does not have to burn its first turn asking for it.
    /// Sending starts the session. Work mode may start with an empty prompt (the
    /// agent picks the next ready backlog task), like the TUI.
    private var composer: some View {
        @Bindable var model = model
        return VStack(alignment: .leading, spacing: 6) {
            PictureStrip(pictures: $pictures)
            HStack(spacing: 8) {
                PicturePickerButton(pictures: $pictures, isLoading: $loadingPictures) { message in
                    pictureError = message
                }
                .disabled(model.isStarting)
                ComposerTextField(
                    placeholder: model.promptIsOptional || model.chosenWorkstreamTask != nil
                        ? "What should the agent do? (optional)"
                        : "What should the agent do?",
                    text: $model.prompt,
                    focused: $composerFocused,
                    maxLines: 6,
                    onPasteImages: { images in
                        pictures = PictureComposer.load(
                            pasted: images, current: pictures
                        ) { message in
                            pictureError = message
                        }
                    }
                )
                .disabled(model.isStarting)
                if model.isStarting {
                    ProgressView()
                        .frame(width: 28, height: 28)
                } else {
                    Button(action: start) {
                        Image(systemName: "arrow.up.circle.fill")
                            .font(.title2)
                    }
                    // Otherwise VoiceOver reads the SF Symbol name.
                    .accessibilityLabel(model.separateWorkstream ? "Start workstream" : "Start session")
                    .disabled(!model.canStart || loadingPictures)
                }
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        // Mirror the draft into the model so `canStart` sees a picture-only
        // prompt, and clear a stale picture error once the draft changes.
        .onChange(of: pictures.map(\.id)) { _, _ in
            model.images = pictures.map(\.image)
            pictureError = nil
        }
    }

    private func start() {
        // Hand the staged pictures to the model right before starting, so
        // `canStart` and the request see the same draft.
        model.images = pictures.map(\.image)
        let title = model.prompt
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .split(separator: "\n", maxSplits: 1).first.map(String.init) ?? ""
        Task {
            let project = model.selectedProject
            let workstream = model.separateWorkstream
            if let sessionID = await model.start() {
                Analytics.submit("new_session", attrs: ["workstream": workstream ? "true" : "false"])
                onStarted(sessionID, project, title)
            }
        }
    }
}
