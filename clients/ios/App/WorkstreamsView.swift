import SwiftUI
import YccKit
import YccProto

/// The workstreams pane lists a project's parallel
/// worktrees with per-stream status, and runs the review-gated Preview / Merge /
/// Discard actions plus a jump into the workstream's live session. Destructive
/// actions confirm first; the merge accept-gate shows the integrated diff before
/// committing. A mid-screen `.unauthorized` failure routes back to the connect
/// screen via ``AppModel/handleUnauthorized()``.
struct WorkstreamsView: View {
    @Environment(AppModel.self) private var app
    @Environment(HomeRouter.self) private var router

    @State private var model: WorkstreamsModel?
    /// A preview result to present (clean diff or conflicts list).
    @State private var previewSheet: PreviewSheet?
    /// A merge accept-gate / conflict / success result to present.
    @State private var mergeSheet: MergeSheet?
    /// The workstream pending a destructive discard confirmation.
    @State private var discardTarget: Ycc_V1_WorkstreamInfo?
    @State private var confirmMergeAll = false
    @State private var mergeAllSummary: String?

    private let initialProject: String

    init(initialProject: String) {
        self.initialProject = initialProject
    }

    var body: some View {
        Group {
            if let model {
                content(model)
            } else {
                ProgressView()
            }
        }
        .navigationTitle("Workstreams")
        .toolbar {
            if let model, model.showsProjectFilter {
                ToolbarItem(placement: .topBarLeading) { projectFilter(model) }
            }
            if let model, !model.gatedWorkstreams.isEmpty {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        confirmMergeAll = true
                    } label: {
                        Label("Merge all ready", systemImage: "arrow.triangle.merge")
                    }
                    .disabled(model.busyWorkstreamID != nil)
                }
            }
        }
        .sheet(item: $previewSheet) { sheet in
            previewSheetView(sheet)
        }
        .sheet(item: $mergeSheet) { sheet in
            mergeSheetView(sheet)
        }
        .confirmationDialog(
            "Discard this workstream?",
            isPresented: Binding(
                get: { discardTarget != nil },
                set: { if !$0 { discardTarget = nil } }),
            titleVisibility: .visible,
            presenting: discardTarget
        ) { workstream in
            Button("Discard", role: .destructive) {
                Task { await model?.discard(workstream) }
                discardTarget = nil
            }
            Button("Cancel", role: .cancel) { discardTarget = nil }
        } message: { _ in
            Text("This stops the session and deletes the worktree + branch. It cannot be undone.")
        }
        .confirmationDialog(
            "Merge all ready workstreams?",
            isPresented: $confirmMergeAll,
            titleVisibility: .visible
        ) {
            Button("Merge all ready") {
                guard let model else { return }
                Task {
                    mergeAllSummary = await model.mergeAllReady().message
                }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Ready workstreams will be merged sequentially. The operation stops at the first failure.")
        }
        .alert(
            "Merge ready workstreams",
            isPresented: Binding(
                get: { mergeAllSummary != nil },
                set: { if !$0 { mergeAllSummary = nil } }),
            presenting: mergeAllSummary
        ) { _ in
            Button("OK", role: .cancel) { mergeAllSummary = nil }
        } message: { summary in
            Text(summary)
        }
        .alert(
            "Action failed",
            isPresented: Binding(
                get: { model?.actionError != nil },
                set: { if !$0 { model?.actionError = nil } }),
            presenting: model?.actionError
        ) { _ in
            Button("OK", role: .cancel) { model?.actionError = nil }
        } message: { message in
            Text(message)
        }
        .task { await ensureLoaded() }
        .onChange(of: model?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
    }

    @ViewBuilder
    private func content(_ model: WorkstreamsModel) -> some View {
        if model.isLoading && !model.hasWorkstreams {
            ProgressView()
        } else if let errorMessage = model.errorMessage, !model.hasWorkstreams {
            ContentUnavailableView(
                "Couldn’t load workstreams",
                systemImage: "exclamationmark.triangle",
                description: Text(errorMessage))
        } else if !model.hasWorkstreams {
            ContentUnavailableView(
                "No workstreams",
                systemImage: "arrow.triangle.branch",
                description: Text("Parallel worktrees spawned for this project show up here."))
        } else {
            workstreamList(model)
        }
    }

    private func workstreamList(_ model: WorkstreamsModel) -> some View {
        List {
            ForEach(model.workstreams, id: \.id) { workstream in
                WorkstreamRow(
                    workstream: workstream,
                    isBusy: model.busyWorkstreamID == workstream.id,
                    onRetry: { Task { await model.retry(workstream) } },
                    onOpenIntegration: { openIntegrationSession(workstream) })
                    .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                        if WorkstreamsModel.status(for: workstream).isDiscardable {
                            Button(role: .destructive) {
                                discardTarget = workstream
                            } label: {
                                Label("Discard", systemImage: "trash")
                            }
                        }
                    }
                    .contextMenu {
                        rowActions(model, workstream)
                    }
            }
        }
        .refreshable { await model.refresh() }
    }

    @ViewBuilder
    private func rowActions(_ model: WorkstreamsModel, _ workstream: Ycc_V1_WorkstreamInfo) -> some View {
        let status = WorkstreamsModel.status(for: workstream)
        if status.isMergeable {
            Button {
                Task { await runPreview(model, workstream) }
            } label: {
                Label("Preview merge", systemImage: "eye")
            }
            Button {
                Task { await runMerge(model, workstream, accept: false) }
            } label: {
                Label("Merge…", systemImage: "arrow.triangle.merge")
            }
        }
        if status == .needsAttention || status == .ready {
            Button {
                Task { await model.retry(workstream) }
            } label: {
                Label("Retry integration", systemImage: "arrow.clockwise")
            }
        }
        if !workstream.sessionID.isEmpty {
            Button {
                router.open(.session(
                    id: workstream.sessionID,
                    project: workstream.project,
                    live: true,
                    title: workstream.taskID.isEmpty
                        ? "Workstream session" : "Workstream \(workstream.taskID)"))
            } label: {
                Label("Open session", systemImage: "bubble.left.and.bubble.right")
            }
        }
        if WorkstreamsModel.integrateSessionID(for: workstream) != nil {
            Button {
                openIntegrationSession(workstream)
            } label: {
                Label("Integration log", systemImage: "wrench.and.screwdriver")
            }
        }
        if status.isDiscardable {
            Divider()
            Button(role: .destructive) {
                discardTarget = workstream
            } label: {
                Label("Discard…", systemImage: "trash")
            }
        }
    }

    // MARK: - Action runners

    private func openIntegrationSession(_ workstream: Ycc_V1_WorkstreamInfo) {
        guard let sessionID = WorkstreamsModel.integrateSessionID(for: workstream) else { return }
        router.open(.session(
            id: sessionID,
            project: workstream.project,
            live: true,
            title: workstream.taskID.isEmpty
                ? "Integration log" : "Integration \(workstream.taskID)"))
    }

    private func runPreview(_ model: WorkstreamsModel, _ workstream: Ycc_V1_WorkstreamInfo) async {
        guard let outcome = await model.preview(workstream) else { return }
        previewSheet = PreviewSheet(workstream: workstream, outcome: outcome)
    }

    private func runMerge(
        _ model: WorkstreamsModel, _ workstream: Ycc_V1_WorkstreamInfo, accept: Bool
    ) async {
        guard let outcome = await model.merge(workstream, accept: accept) else { return }
        mergeSheet = MergeSheet(workstream: workstream, outcome: outcome)
    }

    // MARK: - Sheets

    @ViewBuilder
    private func previewSheetView(_ sheet: PreviewSheet) -> some View {
        NavigationStack {
            switch sheet.outcome {
            case .clean(let diff):
                DiffView(
                    title: "Merge preview",
                    content: .inline(diff: diff, truncated: false))
                    .toolbar { doneButton { previewSheet = nil } }
            case .conflicts(let paths):
                ConflictList(
                    title: "Merge preview",
                    message: "This workstream conflicts with base. Resolve in the worktree before merging.",
                    paths: paths)
                    .toolbar { doneButton { previewSheet = nil } }
            }
        }
    }

    @ViewBuilder
    private func mergeSheetView(_ sheet: MergeSheet) -> some View {
        NavigationStack {
            switch sheet.outcome {
            case .needsAccept(let diff):
                DiffView(
                    title: "Review & merge",
                    content: .inline(diff: diff, truncated: false))
                    .safeAreaInset(edge: .bottom) {
                        acceptBar(sheet.workstream)
                    }
                    .toolbar {
                        ToolbarItem(placement: .cancellationAction) {
                            Button("Cancel") { mergeSheet = nil }
                        }
                    }
            case .conflicts(let paths):
                ConflictList(
                    title: "Merge blocked",
                    message: "Merge conflicts must be resolved in the worktree first. Base is untouched.",
                    paths: paths)
                    .toolbar { doneButton { mergeSheet = nil } }
            case .merged(let commit):
                MergedConfirmation(commit: commit)
                    .toolbar { doneButton { mergeSheet = nil } }
            }
        }
    }

    private func acceptBar(_ workstream: Ycc_V1_WorkstreamInfo) -> some View {
        VStack(spacing: 0) {
            Divider()
            Button {
                mergeSheet = nil
                guard let model else { return }
                Task { await runMerge(model, workstream, accept: true) }
            } label: {
                Label("Accept & merge", systemImage: "arrow.triangle.merge")
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 6)
            }
            .buttonStyle(.borderedProminent)
            .padding(12)
        }
        .background(.bar)
    }

    private func doneButton(_ action: @escaping () -> Void) -> some ToolbarContent {
        ToolbarItem(placement: .confirmationAction) {
            Button("Done", action: action)
        }
    }

    private func projectFilter(_ model: WorkstreamsModel) -> some View {
        @Bindable var model = model
        return Menu {
            Picker("Project", selection: $model.selectedProject) {
                Text("All").tag("")
                ForEach(model.projects, id: \.name) { project in
                    Text(project.name).tag(project.name)
                }
            }
        } label: {
            Label(
                model.selectedProject.isEmpty ? "All" : model.selectedProject,
                systemImage: "line.3.horizontal.decrease.circle")
        }
        .onChange(of: model.selectedProject) { _, _ in
            Task { await model.refresh() }
        }
    }

    private func ensureLoaded() async {
        if model == nil {
            guard let client = app.client else { return }
            model = WorkstreamsModel(source: client, selectedProject: initialProject)
        }
        await model?.refresh()
    }
}

/// A preview result to present in a sheet.
private struct PreviewSheet: Identifiable {
    let workstream: Ycc_V1_WorkstreamInfo
    let outcome: PreviewOutcome
    var id: String { workstream.id }
}

/// A merge result to present in a sheet.
private struct MergeSheet: Identifiable {
    let workstream: Ycc_V1_WorkstreamInfo
    let outcome: MergeOutcome
    // Distinguish successive outcomes for the same workstream (needsAccept →
    // merged) so the sheet re-presents rather than being deduped by id.
    var id: String {
        switch outcome {
        case .needsAccept: return "\(workstream.id)-accept"
        case .conflicts: return "\(workstream.id)-conflict"
        case .merged: return "\(workstream.id)-merged"
        }
    }
}

/// A single workstream row: branch, optional task, status badge, session status,
/// and commit count.
private struct WorkstreamRow: View {
    let workstream: Ycc_V1_WorkstreamInfo
    let isBusy: Bool
    let onRetry: () -> Void
    let onOpenIntegration: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Text(branchLabel)
                    .font(.headline.monospaced())
                    .lineLimit(1)
                Spacer(minLength: 4)
                WorkstreamStatusBadge(workstream: workstream)
            }
            if let reason = WorkstreamsModel.statusReason(for: workstream),
               WorkstreamsModel.status(for: workstream) == .needsAttention {
                Label(reason, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption)
                    .foregroundStyle(.red)
                    .fixedSize(horizontal: false, vertical: true)
            }
            HStack(spacing: 8) {
                if !workstream.taskID.isEmpty {
                    Label(workstream.taskID, systemImage: "checklist")
                }
                Label(WorkstreamsModel.commitSummary(for: workstream),
                      systemImage: "arrow.triangle.branch")
                Spacer(minLength: 4)
                if !workstream.sessionStatus.isEmpty {
                    Text(workstream.sessionStatus)
                        .foregroundStyle(.secondary)
                }
            }
            .font(.caption)
            .foregroundStyle(.secondary)
            if WorkstreamsModel.status(for: workstream) == .needsAttention {
                Button(action: onRetry) {
                    Label("Retry integration", systemImage: "arrow.clockwise")
                }
                .buttonStyle(.borderedProminent)
                .tint(.red)
                .disabled(isBusy)
            }
            if WorkstreamsModel.integrateSessionID(for: workstream) != nil {
                Button(action: onOpenIntegration) {
                    Label("Integration log", systemImage: "wrench.and.screwdriver")
                }
                .buttonStyle(.borderless)
                .font(.caption)
            }
        }
        .padding(.vertical, 2)
    }

    /// Prefer the human-facing branch; fall back to the id.
    private var branchLabel: String {
        workstream.branch.isEmpty ? workstream.id : workstream.branch
    }
}

/// A coloured status pill for a workstream's lifecycle state.
private struct WorkstreamStatusBadge: View {
    let workstream: Ycc_V1_WorkstreamInfo

    private var status: WorkstreamStatus { WorkstreamsModel.status(for: workstream) }
    private var integrationState: WorkstreamIntegrationState {
        WorkstreamsModel.integrationState(for: workstream)
    }

    var body: some View {
        HStack(spacing: 4) {
            if status == .needsAttention {
                Image(systemName: "exclamationmark.triangle.fill")
            } else if integrationState == .integrating {
                ProgressView().controlSize(.mini)
            }
            Text(WorkstreamsModel.badgeTitle(for: workstream))
        }
        .font(.caption2.weight(.semibold))
        .padding(.horizontal, 7)
        .padding(.vertical, 2)
        .background(color.opacity(0.18), in: Capsule())
        .foregroundStyle(color)
    }

    private var color: Color {
        if (status == .active || status == .ready) &&
            (integrationState == .queued || integrationState == .integrating) {
            return .indigo
        }
        switch status {
        case .active: return .green
        case .ready: return .teal
        case .needsAttention: return .red
        case .merged: return .blue
        case .discarded: return .gray
        case .stale: return .orange
        case .unknown: return .gray
        }
    }
}

/// A list of conflicted paths shown when a preview/merge conflicts.
private struct ConflictList: View {
    let title: String
    let message: String
    let paths: [String]

    var body: some View {
        List {
            Section {
                Label(message, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
            }
            Section("Conflicted files") {
                if paths.isEmpty {
                    Text("No paths reported.").foregroundStyle(.secondary)
                } else {
                    ForEach(paths, id: \.self) { path in
                        Text(path).font(.callout.monospaced())
                    }
                }
            }
        }
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// A success confirmation shown after a workstream merges.
private struct MergedConfirmation: View {
    let commit: String

    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: "checkmark.circle.fill")
                .font(.largeTitle)
                .foregroundStyle(.green)
            Text("Merged")
                .font(.headline)
            if !commit.isEmpty {
                Text(commit)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .navigationTitle("Merge")
        .navigationBarTitleDisplayMode(.inline)
    }
}
