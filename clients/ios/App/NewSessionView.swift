import SwiftUI
import YccKit
import YccProto

/// The "new session" composer (docs/design/ios-client.md §6 phase 2 step 5),
/// styled as a blank chat rather than a settings form: the prompt composer sits
/// at the bottom with a send arrow (like the live session's input bar), a row of
/// compact chips above it tucks away mode / interaction level / project, and
/// presets appear as tappable suggestion cards in the empty space. Sending calls
/// `StartSession` and hands the new session id back to the parent, which
/// navigates directly into the live streaming view (`Subscribe` from seq 0).
struct NewSessionView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    @State private var model: NewSessionModel
    @FocusState private var composerFocused: Bool

    /// Called with (sessionID, project) once a session starts successfully. The
    /// parent dismisses the sheet and pushes the live view.
    private let onStarted: (String, String) -> Void

    init(client: YccClient, onStarted: @escaping (String, String) -> Void) {
        _model = State(initialValue: NewSessionModel(source: client))
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
        if let errorMessage = model.errorMessage {
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
    /// its description as a subtitle in the menu), interaction level, and — when
    /// there is more than one project — the project.
    private var optionChips: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                modeChip
                levelChip
                if model.showsProjectPicker { projectChip }
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

    private var levelChip: some View {
        @Bindable var model = model
        return Menu {
            Picker("Interaction level", selection: $model.interactionLevel) {
                ForEach(InteractionLevel.allCases) { level in
                    Label {
                        Text(level.title)
                        Text(level.detail)
                    } icon: {
                        EmptyView()
                    }
                    .tag(level)
                }
            }
        } label: {
            chipLabel(model.interactionLevel.title, systemImage: "person.wave.2")
        }
    }

    private var projectChip: some View {
        @Bindable var model = model
        return Menu {
            Picker("Project", selection: $model.selectedProject) {
                Text("Default").tag("")
                ForEach(model.projects, id: \.name) { project in
                    Text(project.name).tag(project.name)
                }
            }
        } label: {
            chipLabel(
                model.selectedProject.isEmpty ? "Default" : model.selectedProject,
                systemImage: "folder")
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
    /// the live session's input bar). Sending starts the session. Work mode may
    /// start with an empty prompt (the agent picks the next ready backlog task),
    /// like the TUI.
    private var composer: some View {
        @Bindable var model = model
        return HStack(spacing: 8) {
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
                .disabled(!model.canStart)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }

    private func start() {
        Task {
            let project = model.selectedProject
            if let sessionID = await model.start() {
                onStarted(sessionID, project)
            }
        }
    }
}
