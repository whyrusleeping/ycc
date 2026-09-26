import SwiftUI
import YccKit

/// A monospaced, syntax-tinted unified-diff viewer: additions
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
        case workingChanges(project: String, session: String, task: String, knownSnapshot: String)
        /// A diff string already loaded (e.g. a merge preview).
        case inline(diff: String, truncated: Bool)
    }

    let title: String
    let content: Content

    @State private var lines: [DiffFormatter.Line] = []
    @State private var isLoading = false
    @State private var errorMessage: String?
    @State private var scopeNotice: String?

    private var emptyDescription: String {
        if case .workingChanges = content {
            return "No uncommitted changes in this session's scope."
        }
        return "This commit has no textual changes."
    }

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
                    description: Text(emptyDescription))
            } else {
                diffList
            }
        }
        .safeAreaInset(edge: .top) {
            if let scopeNotice {
                Text(scopeNotice).font(.caption).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading).padding(8)
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
        case .workingChanges(let project, let session, let task, let knownSnapshot):
            guard let client = app.client else { return }
            isLoading = true
            defer { isLoading = false }
            do {
                let result = try await client.getWorkingChanges(
                    project: project, session: session, task: task, knownSnapshot: knownSnapshot)
                scopeNotice = "\(result.changedSinceKnown ? "Changed since review · " : "")\(result.scope) · current snapshot \(String(result.snapshotID.prefix(12))) · \(result.excludedDirtyPaths) unrelated dirty paths excluded\(result.truncated ? " · truncated" : "")"
                lines = DiffFormatter.parse(result.diff, truncated: result.truncated)
            } catch YccError.unauthorized {
                app.handleUnauthorized()
            } catch let error as YccError {
                errorMessage = error.displayMessage
            } catch {
                errorMessage = error.localizedDescription
            }
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
