import SwiftUI
import YccKit
import YccProto

/// The "Add project" sheet: registers a workspace on the DAEMON's filesystem as
/// a named project via `AddProject`. A `ListDir`-backed picker offers likely
/// project suggestions and directory browsing, while manual path entry remains
/// available as a fallback. On success the daemon-resolved project is handed
/// back to the presenter, which refreshes its picker and selects it.
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
                if !browserModel.suggestions.isEmpty && !model.isSubmitting {
                    Section("Suggestions") {
                        ForEach(browserModel.suggestions, id: \.self) { suggestion in
                            Button {
                                model.path = suggestion
                            } label: {
                                HStack(spacing: 10) {
                                    Image(systemName: "folder.badge.plus")
                                        .foregroundStyle(.tint)
                                    Text(suggestion)
                                        .font(.callout.monospaced())
                                        .lineLimit(1)
                                        .truncationMode(.middle)
                                        .foregroundStyle(.primary)
                                    Spacer(minLength: 4)
                                    if model.path == suggestion {
                                        Image(systemName: "checkmark")
                                            .foregroundStyle(.tint)
                                    }
                                }
                            }
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
                    .disabled(model.isSubmitting)
                } footer: {
                    Text("Browse directories on the server where the daemon runs.")
                }

                Section {
                    TextField("/home/me/code/project", text: $model.path)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                        .font(.body.monospaced())
                        .disabled(model.isSubmitting)
                } header: {
                    Text("Workspace path")
                } footer: {
                    Text("An absolute directory path on the server the daemon runs on — not on this phone.")
                }
                Section {
                    TextField("Derived from the folder name", text: $model.name)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                        .disabled(model.isSubmitting)
                } header: {
                    Text("Name (optional)")
                }
                if let message = model.errorMessage {
                    Section {
                        Label(message, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.red)
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
                        Button("Add") { submit() }
                            .disabled(!model.canSubmit)
                    }
                }
            }
        }
        .task {
            await browserModel.loadInitial()
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
