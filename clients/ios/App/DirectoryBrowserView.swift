import SwiftUI
import YccKit
import YccProto

/// Drill-down browser for daemon-host directories. The shared model is loaded by
/// the add-project sheet so its initial suggestions and this browser use one RPC
/// result and one authorization signal.
struct DirectoryBrowserView: View {
    @Environment(\.dismiss) private var dismiss

    let model: DirectoryBrowserModel
    let onUse: (String) -> Void

    var body: some View {
        Form {
            if let message = model.errorMessage {
                Section {
                    Label(message, systemImage: "exclamationmark.triangle")
                        .foregroundStyle(.red)
                }
            }

            Section {
                if model.canGoUp {
                    Button {
                        Task { await model.navigateUp() }
                    } label: {
                        Label("Parent directory", systemImage: "arrow.up")
                    }
                    .disabled(model.isLoading)
                }

                ForEach(model.entries, id: \.name) { entry in
                    directoryRow(entry)
                }

                if model.isLoading {
                    HStack {
                        Spacer()
                        ProgressView()
                        Spacer()
                    }
                } else if model.entries.isEmpty && !model.path.isEmpty {
                    Text("No subdirectories")
                        .foregroundStyle(.secondary)
                }
            } header: {
                Text(model.path.isEmpty ? "Server home" : model.path)
                    .font(.caption.monospaced())
                    .textCase(nil)
            }
        }
        .navigationTitle("Browse server")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Use this folder") {
                    onUse(model.path)
                    dismiss()
                }
                .disabled(model.path.isEmpty || model.isLoading)
            }
        }
        .task {
            // Normally already loaded by AddProjectView. This also retries an
            // initial non-authorization failure when the browser is opened.
            await model.loadInitial()
        }
    }

    private func directoryRow(_ entry: Ycc_V1_DirEntry) -> some View {
        Button {
            Task { await model.open(childPath(entry.name)) }
        } label: {
            HStack(spacing: 10) {
                Image(systemName: entry.isGitRepo ? "folder.fill" : "folder")
                    .foregroundStyle(entry.isGitRepo ? Color.accentColor : Color.secondary)
                Text(entry.name)
                    .foregroundStyle(.primary)
                Spacer(minLength: 8)
                if entry.isGitRepo {
                    Text("git")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.tint)
                }
                if entry.isRegistered {
                    Image(systemName: "checkmark.circle.fill")
                        .foregroundStyle(.secondary)
                        .accessibilityLabel("Registered project")
                }
            }
            .opacity(entry.isRegistered ? 0.6 : 1)
        }
        .disabled(model.isLoading)
        .accessibilityHint(Text(verbatim: entry.isRegistered
            ? "Already registered. Opens this directory."
            : "Opens this directory."))
    }

    private func childPath(_ name: String) -> String {
        model.path == "/" ? "/\(name)" : "\(model.path)/\(name)"
    }
}
