import SwiftUI
import YccKit
import YccProto

/// The "Add project" sheet (task 0192): registers a workspace on the DAEMON's
/// filesystem as a named project via `AddProject`. The path is typed manually
/// for now — a server-backed directory picker layers on later (task 0194).
/// On success the daemon-resolved project is handed back to the presenter,
/// which refreshes its picker and selects it.
struct AddProjectView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    @State private var model: AddProjectModel

    /// Called with the registered project once `AddProject` succeeds. The
    /// presenter dismisses the sheet, refreshes its project list, and selects
    /// the new project.
    private let onAdded: (Ycc_V1_ProjectInfo) -> Void

    init(client: YccClient, onAdded: @escaping (Ycc_V1_ProjectInfo) -> Void) {
        _model = State(initialValue: AddProjectModel(source: client))
        self.onAdded = onAdded
    }

    var body: some View {
        @Bindable var model = model
        NavigationStack {
            Form {
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
        .onChange(of: model.unauthorized) { _, isUnauthorized in
            if isUnauthorized {
                dismiss()
                app.handleUnauthorized()
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
}
