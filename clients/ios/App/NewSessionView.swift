import SwiftUI
import YccKit
import YccProto

/// The "new session" composer (docs/design/ios-client.md §6 phase 2 step 5),
/// styled as a blank chat rather than a settings form: the prompt composer sits
/// at the bottom with a send arrow (like the live session's input bar), a row of
/// compact chips above it tucks away mode / project, and
/// presets appear as tappable suggestion cards in the empty space. Sending calls
/// `StartSession` and hands the new session id back to the parent, which
/// navigates directly into the live streaming view (`Subscribe` from seq 0).
struct NewSessionView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    @State private var model: NewSessionModel
    @FocusState private var composerFocused: Bool
    /// Whether the "add project" sheet is shown (from the project chip).
    @State private var showAddProject = false
    /// Pictures staged for the OPENING prompt (shared composer affordances in
    /// `PictureComposer.swift`). They are mirrored into `model.images` so the
    /// send button unlocks on a picture-only draft.
    @State private var pictures: [DraftPicture] = []
    @State private var loadingPictures = false
    /// A picture-loading failure (the model owns only RPC errors).
    @State private var pictureError: String?

    /// Called with (sessionID, project) once a session starts successfully. The
    /// parent dismisses the sheet and pushes the live view.
    private let onStarted: (String, String) -> Void

    /// - Parameter initialProject: the project to preselect (the landing
    ///   screen's current named-project filter). Overrides the
    ///   remembered last-used project so the session starts where the user is
    ///   currently looking.
    init(
        client: YccClient,
        initialProject: String? = nil,
        onStarted: @escaping (String, String) -> Void
    ) {
        _model = State(initialValue: NewSessionModel(
            source: client, initialProject: initialProject))
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
        }
        .task {
            await model.load()
            composerFocused = true
        }
        .sheet(isPresented: $showAddProject) {
            if let client = app.client {
                AddProjectView(client: client) { project in
                    Task {
                        // Reload so the chip lists the new project, then select
                        // it — the likely reason the user added it here.
                        await model.load()
                        model.selectedProject = project.name
                    }
                }
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

    /// The would-be transcript area of the blank chat. Presets render as
    /// tappable suggestion cards (each picks a mode + seeds the composer);
    /// with no presets it stays a quiet placeholder.
    private var suggestionArea: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Spacer(minLength: 24)
                if !model.presets.isEmpty {
                    Text("Suggestions")
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(.secondary)
                    ForEach(model.presets, id: \.name) { preset in
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
            optionChips
            composer
        }
        .background(.bar)
    }

    @ViewBuilder
    private var errorBanner: some View {
        if let errorMessage = pictureError ?? model.errorMessage {
            HStack(spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(.red)
                Text(errorMessage)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color.red.opacity(0.08))
        }
    }

    /// A compact, scrollable row of chips just above the composer: mode (with
    /// its description as a subtitle in the menu), and — when
    /// there is more than one project — the project.
    private var optionChips: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                modeChip
                // Always shown: the chip menu is also the home of the "Add
                // project…" affordance (task 0192), which must be reachable on
                // a daemon with no registered projects yet.
                projectChip
                if model.showsModelPicker {
                    modelChip
                }
            }
            .padding(.horizontal, 12)
            .padding(.top, 8)
            .padding(.bottom, 2)
        }
    }

    private var modeChip: some View {
        @Bindable var model = model
        return Menu {
            Picker("Mode", selection: $model.selectedMode) {
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
        } label: {
            chipLabel(selectedModeTitle, systemImage: "circle.grid.2x2")
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
                Label {
                    Text("Default")
                    if !model.defaultModel.isEmpty {
                        Text(model.defaultModel)
                    }
                } icon: {
                    EmptyView()
                }
                .tag("")
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
        .background(Color(.secondarySystemBackground), in: Capsule())
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
    /// opening prompt may carry pictures (spec §12), so a session about a
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
                TextField(
                    model.promptIsOptional
                        ? "What should the agent do? (optional)"
                        : "What should the agent do?",
                    text: $model.prompt, axis: .vertical)
                    .textFieldStyle(.roundedBorder)
                    .lineLimit(1...6)
                    .focused($composerFocused)
                    .disabled(model.isStarting)
                if model.isStarting {
                    ProgressView()
                        .frame(width: 28, height: 28)
                } else {
                    Button(action: start) {
                        Image(systemName: "arrow.up.circle.fill")
                            .font(.title2)
                    }
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
        Task {
            let project = model.selectedProject
            if let sessionID = await model.start() {
                onStarted(sessionID, project)
            }
        }
    }
}
