import SwiftUI
import YccKit
import YccProto

/// A project's git history (`ListCommits`): one local branch's commits,
/// newest first, paged as the list scrolls. The bar under the title picks the
/// branch (workstream branches grouped and marked); a row opens the commit's
/// diff, and a commit a ycc session recorded links to that session and its task.
struct CommitHistoryView: View {
    @Environment(AppModel.self) private var app
    @Environment(HomeRouter.self) private var router

    private let client: YccClient
    private let project: String
    /// The home list, to tell whether a linked session is still live.
    private let sessionList: SessionListModel?

    @State private var model: CommitHistoryModel?

    init(client: YccClient, project: String, sessionList: SessionListModel?) {
        self.client = client
        self.project = project
        self.sessionList = sessionList
    }

    var body: some View {
        Group {
            if let model {
                content(model)
                    .safeAreaInset(edge: .top, spacing: 0) {
                        if !model.branches.isEmpty { branchBar(model) }
                    }
            } else {
                ProgressView()
            }
        }
        .navigationTitle("History")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            // Once: coming Back from a diff keeps the loaded pages.
            guard model == nil else { return }
            let created = CommitHistoryModel(project: project, source: client)
            model = created
            await created.refresh()
        }
        .onChange(of: model?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
    }

    @ViewBuilder
    private func content(_ model: CommitHistoryModel) -> some View {
        if model.notGit {
            ContentUnavailableView(
                "No git history",
                systemImage: "clock",
                description: Text("\(project) is not a git repository."))
        } else if !model.hasLoaded {
            if let error = model.errorMessage {
                ContentUnavailableView {
                    Label("Couldn’t load history", systemImage: "exclamationmark.triangle")
                } description: {
                    Text(error)
                } actions: {
                    if !model.branch.isEmpty {
                        Button("Show the checked-out branch") {
                            Analytics.action("history.show_head")
                            Task { await model.selectBranch("") }
                        }
                        .buttonStyle(.accent)
                    }
                }
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        } else if model.commits.isEmpty {
            ContentUnavailableView(
                "No commits yet",
                systemImage: "clock",
                description: Text(model.selectedBranch.isEmpty
                    ? "The checked-out HEAD has no commits."
                    : "\(model.selectedBranch) has no commits."))
        } else {
            List {
                if let error = model.errorMessage {
                    Label("Couldn’t refresh: \(error)", systemImage: "exclamationmark.triangle.fill")
                        .font(.footnote)
                        .foregroundStyle(Theme.attention)
                }
                ForEach(model.commits, id: \.sha) { commit in
                    row(commit)
                        .onAppear {
                            Task { await model.loadMoreIfNeeded(after: commit) }
                        }
                }
                if model.hasMore { footer(model) }
            }
            .listStyle(.plain)
            .refreshable { Analytics.action("refresh", via: .pull); await model.refresh() }
        }
    }

    // MARK: - Branch picker

    private func branchBar(_ model: CommitHistoryModel) -> some View {
        let groups = CommitFormat.groupBranches(model.branches)
        let selected = model.selectedBranch
        return HStack(spacing: 10) {
            Menu {
                Section {
                    ForEach(groups.local, id: \.name) { branch in
                        branchButton(model, branch, selected: selected)
                    }
                }
                if !groups.workstreams.isEmpty {
                    Section("Workstreams") {
                        ForEach(groups.workstreams, id: \.name) { branch in
                            branchButton(model, branch, selected: selected)
                        }
                    }
                }
            } label: {
                HStack(spacing: 5) {
                    Image(systemName: "arrow.triangle.branch")
                    Text(selected.isEmpty ? "HEAD (detached)" : selected)
                        .font(.subheadline.monospaced())
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Image(systemName: "chevron.up.chevron.down")
                        .font(.caption2.weight(.semibold))
                }
            }
            .accessibilityLabel("Branch \(selected.isEmpty ? "HEAD, detached" : selected)")
            if let ref = model.selectedRef, !ref.workstreamID.isEmpty {
                Text("workstream")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                if !ref.taskID.isEmpty {
                    taskButton(ref.taskID).imageScale(.small)
                }
            }
            Spacer(minLength: 0)
            if model.isLoading {
                ProgressView().controlSize(.small)
            }
        }
        .padding(.horizontal)
        .padding(.vertical, 8)
        .background(.bar)
    }

    private func branchButton(
        _ model: CommitHistoryModel, _ branch: Ycc_V1_BranchRef, selected: String
    ) -> some View {
        Button {
            Analytics.action(
                "history.branch", via: .menu,
                attrs: ["workstream": branch.workstreamID.isEmpty ? "false" : "true"])
            Task { await model.selectBranch(branch.name) }
        } label: {
            let note = CommitFormat.branchNote(branch)
            if branch.name == selected {
                Label(branch.name, systemImage: "checkmark")
                if !note.isEmpty { Text(note) }
            } else {
                Text(branch.name)
                if !note.isEmpty { Text(note) }
            }
        }
    }

    // MARK: - Rows

    private func row(_ commit: Ycc_V1_CommitSummary) -> some View {
        VStack(alignment: .leading, spacing: 5) {
            Button {
                Analytics.action("history.open_commit")
                router.path.append(.commitDiff(project: project, sha: commit.sha))
            } label: {
                HStack(spacing: 8) {
                    CommitSummaryLabel(commit: commit)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    Image(systemName: "chevron.right")
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(Color(.tertiaryLabel))
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityHint("Opens the commit's diff")
            if !commit.taskID.isEmpty || !commit.sessionID.isEmpty {
                HStack(spacing: 12) {
                    if !commit.taskID.isEmpty { taskButton(commit.taskID) }
                    if !commit.sessionID.isEmpty { sessionButton(commit) }
                }
                .imageScale(.small)
            }
        }
        .padding(.vertical, 2)
        // The badges' icons would otherwise pull the separator in under them.
        .alignmentGuide(.listRowSeparatorLeading) { _ in 0 }
    }

    /// A backlog task badge, pushed so Back returns to the history.
    private func taskButton(_ taskID: String) -> some View {
        Button {
            Analytics.action("history.open_task")
            router.path.append(.taskDetail(project: project, taskID: taskID, title: ""))
        } label: {
            Label(taskID, systemImage: "checklist")
                .font(.caption.monospaced())
        }
        .buttonStyle(.borderless)
        .accessibilityLabel("Task \(taskID)")
    }

    /// The session that made the commit. A session the home list knows to be
    /// live opens live; otherwise its persisted transcript.
    private func sessionButton(_ commit: Ycc_V1_CommitSummary) -> some View {
        let known = sessionList?.session(sessionID: commit.sessionID)
        let title = commit.sessionTitle.isEmpty
            ? (known.map(SessionListModel.displayTitle(for:)) ?? commit.sessionID)
            : commit.sessionTitle
        return Button {
            Analytics.action("history.open_session")
            router.path.append(.session(
                id: commit.sessionID, project: project,
                live: known?.live ?? false, title: title))
        } label: {
            Label(title, systemImage: "bubble.left")
                .font(.caption)
                .lineLimit(1)
        }
        .buttonStyle(.borderless)
        .accessibilityLabel("Made in session \(title)")
    }

    @ViewBuilder
    private func footer(_ model: CommitHistoryModel) -> some View {
        if let error = model.loadMoreError {
            VStack(alignment: .leading, spacing: 6) {
                Text(error)
                    .font(.footnote)
                    .foregroundStyle(Theme.danger)
                Button("Load older commits") {
                    Analytics.action("history.load_more")
                    Task { await model.loadMore() }
                }
                .buttonStyle(.borderless)
            }
        } else {
            ProgressView()
                .frame(maxWidth: .infinity)
                .listRowSeparator(.hidden)
        }
    }
}

/// A commit's subject, then its short sha, author, age and +/- line counts.
private struct CommitSummaryLabel: View {
    let commit: Ycc_V1_CommitSummary

    var body: some View {
        let stats = CommitFormat.stats(commit)
        VStack(alignment: .leading, spacing: 3) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(commit.subject.isEmpty ? "(no subject)" : commit.subject)
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(commit.subject.isEmpty ? .secondary : .primary)
                    .lineLimit(2)
                if CommitFormat.isMerge(commit) {
                    StatusWord(text: "merge", color: .secondary)
                }
            }
            HStack(spacing: 6) {
                Text(CommitFormat.shortSha(commit.sha))
                    .font(.caption.monospaced())
                if !commit.authorName.isEmpty {
                    Text(commit.authorName).lineLimit(1)
                }
                if let date = CommitFormat.date(commit) {
                    Text(date.formatted(.relative(presentation: .named)))
                        .lineLimit(1)
                }
                Spacer(minLength: 4)
                if let stats {
                    HStack(spacing: 4) {
                        Text(stats.additions).foregroundStyle(Theme.live)
                        Text(stats.deletions).foregroundStyle(Theme.danger)
                    }
                    .font(.caption.monospacedDigit())
                    .fixedSize()
                    .accessibilityLabel("\(stats.files), \(stats.additions) \(stats.deletions)")
                }
            }
            .font(.caption)
            .foregroundStyle(.secondary)
        }
    }
}
