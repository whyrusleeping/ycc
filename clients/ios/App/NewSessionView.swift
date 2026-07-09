import SwiftUI
import YccKit
import YccProto

/// The "new session" composer (docs/design/ios-client.md §6 phase 2 step 5): a
/// mode picker (with preset shortcuts), an interaction-level picker, a project
/// picker, and a multiline prompt composer. On **Start** it calls `StartSession`
/// and hands the new session id back to the parent, which navigates directly
/// into the live streaming view (`Subscribe` from seq 0).
struct NewSessionView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    @State private var model: NewSessionModel

    /// Called with (sessionID, project) once a session starts successfully. The
    /// parent dismisses the sheet and pushes the live view.
    private let onStarted: (String, String) -> Void

    init(client: YccClient, onStarted: @escaping (String, String) -> Void) {
        _model = State(initialValue: NewSessionModel(source: client))
        self.onStarted = onStarted
    }

    var body: some View {
        NavigationStack {
            Form {
                if model.isLoading && model.modes.isEmpty {
                    HStack { Spacer(); ProgressView(); Spacer() }
                } else {
                    modeSection
                    if !model.presets.isEmpty { presetSection }
                    levelSection
                    if model.showsProjectPicker { projectSection }
                    promptSection
                    if let errorMessage = model.errorMessage {
                        Section {
                            Label(errorMessage, systemImage: "exclamationmark.triangle")
                                .foregroundStyle(.red)
                                .font(.callout)
                        }
                    }
                }
            }
            .navigationTitle("New session")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if model.isStarting {
                        ProgressView()
                    } else {
                        Button("Start") { start() }
                            .disabled(!model.canStart)
                    }
                }
            }
        }
        .task { await model.load() }
        .onChange(of: model.unauthorized) { _, isUnauthorized in
            if isUnauthorized {
                dismiss()
                app.handleUnauthorized()
            }
        }
    }

    private var modeSection: some View {
        @Bindable var model = model
        return Section {
            Picker("Mode", selection: $model.selectedMode) {
                ForEach(model.modes, id: \.name) { mode in
                    Text(mode.title.isEmpty ? mode.name : mode.title).tag(mode.name)
                }
            }
        } header: {
            Text("Mode")
        } footer: {
            if let description = model.selectedModeDescription {
                Text(description)
            }
        }
    }

    private var presetSection: some View {
        Section {
            ForEach(model.presets, id: \.name) { preset in
                Button {
                    model.apply(preset: preset)
                } label: {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(preset.title.isEmpty ? preset.name : preset.title)
                            .foregroundStyle(.primary)
                        if !preset.description_p.isEmpty {
                            Text(preset.description_p)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                }
            }
        } header: {
            Text("Presets")
        } footer: {
            Text("A preset picks a mode and seeds the prompt below — edit before starting.")
        }
    }

    private var levelSection: some View {
        @Bindable var model = model
        return Section {
            Picker("Interaction level", selection: $model.interactionLevel) {
                ForEach(InteractionLevel.allCases) { level in
                    Text(level.title).tag(level)
                }
            }
        } header: {
            Text("Interaction level")
        } footer: {
            Text(model.interactionLevel.detail)
        }
    }

    private var projectSection: some View {
        @Bindable var model = model
        return Section("Project") {
            Picker("Project", selection: $model.selectedProject) {
                Text("Default").tag("")
                ForEach(model.projects, id: \.name) { project in
                    Text(project.name).tag(project.name)
                }
            }
        }
    }

    private var promptSection: some View {
        @Bindable var model = model
        return Section("Prompt") {
            TextField("What should the agent do?", text: $model.prompt, axis: .vertical)
                .lineLimit(3...10)
        }
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
