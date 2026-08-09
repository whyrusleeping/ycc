import SwiftUI
import YccKit
import YccProto

/// Read-only viewer for the project's agent memory (memory.md, spec §6.5): the
/// advisory operational notes agents record across sessions via the `remember`
/// tool — environment quirks, codebase gotchas, user preferences, lessons.
/// Reached from the project overflow menu (landing + session views). The file
/// is small by design (it is injected into every agent's system prompt), so a
/// single markdown render of the whole document is the right shape — no
/// pagination or search needed.
struct MemoryView: View {
    @Environment(AppModel.self) private var app

    private let client: YccClient
    private let project: String

    @State private var content: String?
    @State private var path = ""
    @State private var errorMessage: String?

    init(client: YccClient, project: String) {
        self.client = client
        self.project = project
    }

    var body: some View {
        Group {
            if let content {
                if content.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    emptyState
                } else {
                    loaded(content)
                }
            } else if let errorMessage {
                ContentUnavailableView(
                    "Couldn’t load memory",
                    systemImage: "exclamationmark.triangle",
                    description: Text(errorMessage))
            } else {
                ProgressView()
            }
        }
        .navigationTitle("Memory")
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
    }

    private var emptyState: some View {
        ContentUnavailableView(
            "No memory yet",
            systemImage: "brain",
            description: Text(
                "Agents record operational notes about working on this project "
                    + "here as they learn them — environment quirks, gotchas, and "
                    + "your preferences."))
    }

    private func loaded(_ content: String) -> some View {
        List {
            Section {
                MarkdownText(text: content)
            } footer: {
                if !path.isEmpty {
                    // Where the notes live on the daemon host, for anyone who
                    // wants to groom them from a real editor.
                    Text(path)
                        .font(.caption2.monospaced())
                }
            }
        }
        .refreshable { await load() }
    }

    private func load() async {
        // Keep stale content on screen during a pull-to-refresh; only the very
        // first load shows the spinner (the `content == nil` branch of `body`).
        do {
            let response = try await client.getMemory(project: project)
            content = response.content
            path = response.path
            errorMessage = nil
        } catch YccError.unauthorized {
            app.handleUnauthorized()
        } catch {
            // A failed refresh over good content: keep the content.
            if content == nil {
                errorMessage = (error as? YccError)?.displayMessage
                    ?? error.localizedDescription
            }
        }
    }
}
