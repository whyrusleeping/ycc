import SwiftUI
import YccKit

/// A monospaced, syntax-tinted unified-diff viewer (task 0189/0140): additions
/// tinted green, deletions red, headers/hunks de-emphasised. Rows render lazily
/// (a `List` of pre-parsed ``DiffFormatter/Line`` values) so a large diff scrolls
/// without hanging the UI, and each line scrolls horizontally so long lines are
/// not truncated on a phone.
///
/// Loads its content two ways: eagerly from a diff string already in hand (a
/// merge preview), or by fetching `GetCommitDiff` for a `commit_made` sha.
struct DiffView: View {
    @Environment(AppModel.self) private var app

    /// How the diff is sourced.
    enum Content {
        /// Fetch `GetCommitDiff(project, sha)`.
        case commit(project: String, sha: String)
        /// A diff string already loaded (e.g. a merge preview).
        case inline(diff: String, truncated: Bool)
    }

    let title: String
    let content: Content

    @State private var lines: [DiffFormatter.Line] = []
    @State private var isLoading = false
    @State private var errorMessage: String?

    var body: some View {
        Group {
            if isLoading {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let errorMessage {
                ContentUnavailableView(
                    "Couldn’t load diff",
                    systemImage: "exclamationmark.triangle",
                    description: Text(errorMessage))
            } else if lines.isEmpty {
                ContentUnavailableView(
                    "Empty diff",
                    systemImage: "doc.plaintext",
                    description: Text("This commit has no textual changes."))
            } else {
                diffList
            }
        }
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
    }

    private var diffList: some View {
        List(lines) { line in
            DiffLineView(line: line)
                .listRowInsets(EdgeInsets(top: 0, leading: 8, bottom: 0, trailing: 8))
                .listRowSeparator(.hidden)
        }
        .listStyle(.plain)
        .environment(\.defaultMinListRowHeight, 0)
    }

    private func load() async {
        // Idempotent: don't reload if already populated.
        guard lines.isEmpty, errorMessage == nil, !isLoading else { return }
        switch content {
        case .inline(let diff, let truncated):
            lines = DiffFormatter.parse(diff, truncated: truncated)
        case .commit(let project, let sha):
            await loadCommit(project: project, sha: sha)
        }
    }

    private func loadCommit(project: String, sha: String) async {
        guard let client = app.client else { return }
        guard !sha.isEmpty else {
            errorMessage = "This commit has no recorded sha."
            return
        }
        isLoading = true
        defer { isLoading = false }
        do {
            let (diff, truncated) = try await client.getCommitDiff(project: project, sha: sha)
            lines = DiffFormatter.parse(diff, truncated: truncated)
        } catch YccError.unauthorized {
            app.handleUnauthorized()
        } catch let error as YccError {
            errorMessage = error.displayMessage
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}

/// A single monospaced diff line, tinted by kind and horizontally scrollable so
/// long lines aren't clipped on a narrow screen.
private struct DiffLineView: View {
    let line: DiffFormatter.Line

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            Text(line.text.isEmpty ? " " : line.text)
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(foreground)
                .textSelection(.enabled)
                .padding(.vertical, 1)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .listRowBackground(background)
    }

    private var foreground: Color {
        switch line.kind {
        case .addition: return .green
        case .deletion: return .red
        case .hunkHeader: return .cyan
        case .fileHeader: return .secondary
        case .truncationNotice: return .orange
        case .context: return .primary
        }
    }

    private var background: Color {
        switch line.kind {
        case .addition: return Color.green.opacity(0.10)
        case .deletion: return Color.red.opacity(0.10)
        case .hunkHeader: return Color.cyan.opacity(0.08)
        default: return .clear
        }
    }
}
