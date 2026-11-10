import SwiftUI
import YccKit
import YccProto

/// The "Add project" sheet: registers a workspace on the DAEMON's filesystem as
/// a named project via `AddProject`. Three modes: pick an existing directory
/// (suggestions, server browsing, or a typed path), create a new git
/// repository in a folder, or clone a repository. On success the
/// daemon-resolved project is handed back to the presenter, which refreshes
/// its picker and selects it.
struct AddProjectView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    @State private var model: AddProjectModel
    @State private var browserModel: DirectoryBrowserModel

    /// Called with the registered project once `AddProject` succeeds. The
    /// presenter dismisses the sheet, refreshes its project list, and selects
    /// the new project.
    private let onAdded: (Ycc_V1_ProjectInfo) -> Void

    init(client: YccClient, onAdded: @escaping (Ycc_V1_ProjectInfo) -> Void) {
        _model = State(initialValue: AddProjectModel(source: client))
        _browserModel = State(initialValue: DirectoryBrowserModel(source: client))
        self.onAdded = onAdded
    }

    var body: some View {
        @Bindable var model = model
        NavigationStack {
            Form {
                Section {
                    Picker("Mode", selection: $model.mode) {
                        Text("Existing").tag(AddProjectModel.Mode.existing)
                        Text("New folder").tag(AddProjectModel.Mode.newFolder)
                        Text("Clone").tag(AddProjectModel.Mode.clone)
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets())
                }
                .disabled(model.isSubmitting)

                Group {
                    switch model.mode {
                    case .existing: existingSections
                    case .newFolder: newFolderSections
                    case .clone: cloneSections
                    }
                }
                .disabled(model.isSubmitting)

                if model.isSubmitting && model.mode == .clone {
                    Section {
                        HStack(spacing: 10) {
                            ProgressView()
                            Text("Cloning…")
                                .foregroundStyle(.secondary)
                        }
                    }
                }
                if let message = model.errorMessage {
                    Section {
                        Label(message, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(Theme.danger)
                    }
                }
            }
            .navigationTitle("Add project")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if model.isSubmitting {
                        ProgressView()
                    } else {
                        Button(confirmTitle) { submit() }
                            .disabled(!model.canSubmit)
                    }
                }
            }
        }
        .task {
            await browserModel.loadInitial()
        }
        .onChange(of: browserModel.defaultParent, initial: true) { _, defaultParent in
            if model.location.isEmpty {
                model.location = defaultParent
            }
        }
        .onChange(of: model.unauthorized) { _, isUnauthorized in
            if isUnauthorized {
                dismissForUnauthorized()
            }
        }
        .onChange(of: browserModel.unauthorized) { _, isUnauthorized in
            if isUnauthorized {
                dismissForUnauthorized()
            }
        }
    }

    private var confirmTitle: String {
        switch model.mode {
        case .existing: "Add"
        case .newFolder: "Create"
        case .clone: "Clone"
        }
    }

    // MARK: - Existing

    @ViewBuilder
    private var existingSections: some View {
        @Bindable var model = model
        if !browserModel.suggestions.isEmpty && !model.isSubmitting {
            Section("Recent") {
                ForEach(browserModel.suggestions, id: \.self) { suggestion in
                    suggestionRow(suggestion)
                }
            }
        }

        Section {
            NavigationLink {
                DirectoryBrowserView(model: browserModel) { path in
                    self.model.path = path
                }
            } label: {
                Label("Browse server…", systemImage: "folder")
            }
        } footer: {
            Text("Browse directories on the server where the daemon runs.")
        }

        Section {
            TextField("/home/me/code/project", text: $model.path)
                .autocorrectionDisabled()
                .textInputAutocapitalization(.never)
                .font(.body.monospaced())
        } header: {
            Text("Workspace path")
        } footer: {
            Text("An absolute directory path on the server the daemon runs on — not on this phone.")
        }
        Section {
            TextField("Derived from the folder name", text: $model.name)
                .autocorrectionDisabled()
                .textInputAutocapitalization(.never)
        } header: {
            Text("Name (optional)")
        }
    }

    private func suggestionRow(_ suggestion: String) -> some View {
        let url = URL(fileURLWithPath: suggestion)
        let parent = browserModel.abbreviated(url.deletingLastPathComponent().path)
        return Button {
            model.path = suggestion
        } label: {
            HStack(spacing: 10) {
                Image(systemName: "folder")
                    .foregroundStyle(.tint)
                VStack(alignment: .leading, spacing: 2) {
                    Text(url.lastPathComponent)
                        .foregroundStyle(.primary)
                        .lineLimit(1)
                    Text(parent)
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                Spacer(minLength: 4)
                if model.path == suggestion {
                    Image(systemName: "checkmark")
                        .foregroundStyle(.tint)
                }
            }
        }
        .accessibilityLabel(Text(verbatim: browserModel.abbreviated(suggestion)))
        .accessibilityAddTraits(model.path == suggestion ? .isSelected : [])
    }

    // MARK: - New folder

    @ViewBuilder
    private var newFolderSections: some View {
        Section {
            locationRow
            folderNameField
        } footer: {
            if AddProjectModel.isValidFolderName(model.folderName) && !model.location.isEmpty {
                Text("Creates \(browserModel.abbreviated(model.targetPath)) and runs git init on the server.")
            } else {
                Text("Creates the folder and runs git init on the server.")
            }
        }
    }

    // MARK: - Clone

    @ViewBuilder
    private var cloneSections: some View {
        @Bindable var model = model
        Section {
            TextField("owner/repo or URL", text: $model.repository)
                .autocorrectionDisabled()
                .textInputAutocapitalization(.never)
                .keyboardType(.URL)
                .font(.body.monospaced())
        } header: {
            Text("Repository")
        } footer: {
            Text("Cloned on the server with its gh login or git credentials.")
        }
        Section {
            locationRow
            folderNameField
        } footer: {
            if AddProjectModel.isValidFolderName(model.folderName) && !model.location.isEmpty {
                Text("Clones into \(browserModel.abbreviated(model.targetPath)).")
            }
        }
    }

    // MARK: - Shared rows

    private var locationRow: some View {
        NavigationLink {
            DirectoryBrowserView(model: browserModel, startPath: model.location) { path in
                self.model.location = path
            }
        } label: {
            LabeledContent("Location") {
                Text(model.location.isEmpty ? "Choose…" : browserModel.abbreviated(model.location))
                    .font(.callout.monospaced())
                    .lineLimit(1)
                    .truncationMode(.head)
            }
        }
    }

    private var folderNameField: some View {
        @Bindable var model = model
        return LabeledContent("Folder name") {
            TextField("Folder name", text: $model.folderName)
                .autocorrectionDisabled()
                .textInputAutocapitalization(.never)
                .multilineTextAlignment(.trailing)
                .font(.body.monospaced())
        }
    }

    private func submit() {
        Task {
            if let project = await model.submit() {
                dismiss()
                onAdded(project)
            }
        }
    }

    private func dismissForUnauthorized() {
        dismiss()
        app.handleUnauthorized()
    }
}
