import SwiftUI
import YccKit
import YccProto

/// Placeholder authenticated landing view: lists the daemon's projects as proof
/// of an authenticated round-trip (docs/design/ios-client.md §6). Later phases
/// replace this with the session list. A mid-session `.unauthorized` failure
/// routes back to the connect screen via ``AppModel/handleUnauthorized()``.
struct LandingView: View {
    @Environment(AppModel.self) private var model

    @State private var projects: [Ycc_V1_ProjectInfo] = []
    @State private var errorMessage: String?
    @State private var isLoading = false

    var body: some View {
        NavigationStack {
            Group {
                if isLoading && projects.isEmpty {
                    ProgressView()
                } else if let errorMessage {
                    ContentUnavailableView("Couldn’t load projects", systemImage: "exclamationmark.triangle", description: Text(errorMessage))
                } else if projects.isEmpty {
                    ContentUnavailableView("No projects", systemImage: "folder")
                } else {
                    List(projects, id: \.name) { project in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(project.name).font(.headline)
                            Text(project.path).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
            }
            .navigationTitle("Projects")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Disconnect") { model.disconnect() }
                }
            }
            .refreshable { await load() }
            .task { await load() }
        }
    }

    private func load() async {
        guard let client = model.client else { return }
        isLoading = true
        errorMessage = nil
        defer { isLoading = false }
        do {
            projects = try await client.listProjects()
        } catch YccError.unauthorized {
            // Token revoked/expired mid-session → back to the connect screen.
            model.handleUnauthorized()
        } catch let YccError.rpc(message) {
            errorMessage = message
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}
