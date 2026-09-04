import SwiftUI
import YccKit
import YccProto

/// The task-detail screen shows the frontmatter header (status, priority,
/// dependencies, ready/blocked,
/// dates) plus the markdown `body`, a status picker driving `UpdateTask`, and a
/// "Start work on this task" action that starts a work session (`StartSession`)
/// and navigates into its live stream.
struct TaskDetailView: View {
    @Environment(AppModel.self) private var app
    @Environment(HomeRouter.self) private var router

    @State private var model: TaskDetailModel
    /// A start-work failure message to surface as an alert.
    @State private var startError: String?
    @State private var isStarting = false

    private let taskID: String
    private let taskTitle: String
    private let project: String

    init(client: YccClient, project: String, taskID: String, taskTitle: String) {
        _model = State(initialValue: TaskDetailModel(source: client, project: project, taskID: taskID))
        self.taskID = taskID
        self.taskTitle = taskTitle
        self.project = project
    }

    /// The display title for a session opened from this task, mirroring how the
    /// session list would eventually title it.
    private var sessionTitle: String {
        taskTitle.isEmpty ? "Work on task \(taskID)" : taskTitle
    }

    var body: some View {
        Group {
            if let task = model.task {
                detail(task)
            } else if model.isLoading {
                ProgressView()
            } else if let errorMessage = model.errorMessage {
                ContentUnavailableView(
                    "Couldn’t load task",
                    systemImage: "exclamationmark.triangle",
                    description: Text(errorMessage))
            } else {
                ProgressView()
            }
        }
        .navigationTitle(model.task.map { $0.id } ?? "Task")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItemGroup(placement: .topBarTrailing) {
                Button("Edit") { model.beginEditing() }
                    .disabled(model.task == nil || model.isUpdating)
                statusMenu
            }
        }
        .sheet(isPresented: Binding(
            get: { model.isEditing },
            set: { if !$0 { model.cancelEditing() } }
        )) {
            TaskEditorView(model: model)
        }
        .alert(
            "Couldn’t start work",
            isPresented: Binding(
                get: { startError != nil },
                set: { if !$0 { startError = nil } }),
            presenting: startError
        ) { _ in
            Button("OK", role: .cancel) { startError = nil }
        } message: { message in
            Text(message)
        }
        .task { await model.load() }
        .onChange(of: model.unauthorized) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
    }

    @ViewBuilder
    private func detail(_ task: Ycc_V1_TaskDetail) -> some View {
        List {
            Section {
                Text(task.title.isEmpty ? "(untitled)" : task.title)
                    .font(.title3.weight(.semibold))
                metadata(task)
            }
            Section {
                if model.status == .inProgress, !model.activeSessions.isEmpty {
                    activeSessionButtons
                } else {
                    startWorkButton(task)
                }
            }
            if let errorMessage = model.errorMessage {
                Section {
                    Label(errorMessage, systemImage: "exclamationmark.triangle")
                        .foregroundStyle(.red)
                        .font(.callout)
                }
            }
            if !task.body.isEmpty {
                Section("Details") {
                    MarkdownText(text: task.body)
                }
            }
        }
        .refreshable { await model.load() }
    }

    @ViewBuilder
    private func metadata(_ task: Ycc_V1_TaskDetail) -> some View {
        HStack(spacing: 8) {
            // The pill doubles as the status changer (the toolbar menu offers
            // the same choices); the chevron marks it as tappable.
            Menu {
                statusChoices
            } label: {
                HStack(spacing: 3) {
                    TaskStatusPill(status: model.status)
                    Image(systemName: "chevron.up.chevron.down")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
            }
            .disabled(model.isUpdating)
            PriorityBadge(priority: task.priority)
            if task.ready, model.status != .done {
                Label("Ready", systemImage: "checkmark.circle")
                    .font(.caption)
                    .foregroundStyle(.green)
            }
        }
        if !task.blockedBy.isEmpty, model.status != .done {
            VStack(alignment: .leading, spacing: 8) {
                ForEach(task.blockedBy, id: \.self) { blockerID in
                    Button {
                        openTask(blockerID)
                    } label: {
                        HStack(spacing: 6) {
                            Label("Blocked by \(blockerID)", systemImage: "lock.fill")
                            Spacer()
                            Image(systemName: "chevron.right")
                                .font(.caption2)
                        }
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(.orange)
                    .accessibilityHint("Opens task \(blockerID)")
                }
            }
            .font(.caption)
        }
        if !task.dependsOn.isEmpty {
            taskLinks("Depends on", taskIDs: task.dependsOn)
        }
        if !task.specRefs.isEmpty {
            metaRow("Spec refs", task.specRefs.joined(separator: ", "))
        }
        if !task.created.isEmpty { metaRow("Created", task.created) }
        if !task.updated.isEmpty { metaRow("Updated", task.updated) }
    }

    private func metaRow(_ label: String, _ value: String) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
            Spacer(minLength: 8)
            Text(value)
                .font(.caption)
                .multilineTextAlignment(.trailing)
        }
    }

    /// Render task relationships as explicit links. Routing instead of nesting a
    /// `NavigationLink` makes task-to-task hops *lateral*: the detail screen is
    /// swapped in place, so cycles never grow the stack and Back still returns
    /// to the backlog (or wherever this detail was opened from).
    private func taskLinks(_ label: String, taskIDs: [String]) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
            Spacer(minLength: 8)
            VStack(alignment: .trailing, spacing: 8) {
                ForEach(taskIDs, id: \.self) { dependencyID in
                    Button {
                        openTask(dependencyID)
                    } label: {
                        HStack(spacing: 4) {
                            Text(dependencyID)
                            Image(systemName: "chevron.right")
                                .font(.caption2)
                        }
                    }
                    .font(.caption)
                    .accessibilityLabel("Open task \(dependencyID)")
                }
            }
        }
    }

    private func openTask(_ id: String) {
        router.open(.taskDetail(project: project, taskID: id, title: ""))
    }

    /// The selectable statuses, with a checkmark on the current one (shared by
    /// the toolbar menu and the tappable status pill).
    @ViewBuilder
    private var statusChoices: some View {
        ForEach(TaskStatus.selectable) { status in
            Button {
                Task { await model.setStatus(status) }
            } label: {
                if status == model.status {
                    Label(status.title, systemImage: "checkmark")
                } else {
                    Text(status.title)
                }
            }
        }
    }

    private var statusMenu: some View {
        Menu {
            statusChoices
        } label: {
            if model.isUpdating {
                ProgressView()
            } else {
                Label("Status", systemImage: "ellipsis.circle")
            }
        }
        .disabled(model.isUpdating || model.task == nil)
    }

    @ViewBuilder
    private var activeSessionButtons: some View {
        ForEach(model.activeSessions, id: \.sessionID) { session in
            Button {
                // Routed with dedupe: if the user *came here from* that very
                // session (session → backlog → task), this pops back to it
                // instead of stacking a second copy of the same transcript.
                router.open(.session(
                    id: session.sessionID, project: project,
                    live: true, title: sessionTitle))
            } label: {
                Label(
                    model.activeSessions.count == 1
                        ? "Open active session"
                        : "Open active session · \(String(session.sessionID.prefix(8)))",
                    systemImage: session.status == "paused" ? "pause.circle.fill" : "bolt.circle.fill")
            }
        }
    }

    @ViewBuilder
    private func startWorkButton(_ task: Ycc_V1_TaskDetail) -> some View {
        Button {
            startWork(task)
        } label: {
            if isStarting {
                HStack { Spacer(); ProgressView(); Spacer() }
            } else {
                Label("Start work on this task", systemImage: "play.circle.fill")
            }
        }
        .disabled(isStarting)
    }

    /// Start a work session focused on this task, then navigate into its live
    /// stream (reuses the ``LandingView`` start-and-push pattern).
    private func startWork(_ task: Ycc_V1_TaskDetail) {
        guard let client = app.client else { return }
        let titleText = task.title.isEmpty ? "" : ": \(task.title)"
        let prompt = "Work on task \(task.id)\(titleText)."
        isStarting = true
        Task {
            defer { isStarting = false }
            do {
                let sessionID = try await client.startSession(
                    project: project, mode: "work", prompt: prompt)
                router.open(.session(
                    id: sessionID, project: project,
                    live: true, title: sessionTitle))
            } catch YccError.unauthorized {
                app.handleUnauthorized()
            } catch let YccError.rpc(message) {
                startError = message
            } catch let YccError.notFound(message) {
                startError = message
            } catch let YccError.failedPrecondition(message) {
                startError = message
            } catch {
                startError = error.localizedDescription
            }
        }
    }
}

/// Full-screen task editor presented from detail. Draft state lives in the
/// model, so failed saves keep every field intact and cancellation is explicit.
private struct TaskEditorView: View {
    @Bindable var model: TaskDetailModel

    var body: some View {
        NavigationStack {
            Form {
                Section("Task") {
                    TextField("Title", text: $model.draftTitle, axis: .vertical)
                    Picker("Status", selection: $model.draftStatus) {
                        ForEach(TaskStatus.selectable) { status in
                            Text(status.title).tag(status)
                        }
                    }
                    Picker("Priority", selection: $model.draftPriority) {
                        ForEach(1...5, id: \.self) { priority in
                            Text("P\(priority)").tag(priority)
                        }
                    }
                }
                Section("Relationships") {
                    TextField(
                        "Dependencies (comma or line separated)",
                        text: $model.draftDependsOn,
                        axis: .vertical)
                        .lineLimit(2...5)
                    TextField(
                        "Spec references (comma or line separated)",
                        text: $model.draftSpecRefs,
                        axis: .vertical)
                        .lineLimit(2...5)
                }
                Section("Details (Markdown)") {
                    TextEditor(text: $model.draftBody)
                        .font(.body.monospaced())
                        .frame(minHeight: 260)
                }
                if let message = model.draftValidationMessage ?? model.errorMessage {
                    Section {
                        Label(message, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle("Edit task \(model.taskID)")
            .navigationBarTitleDisplayMode(.inline)
            .interactiveDismissDisabled(model.isUpdating)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { model.cancelEditing() }
                        .disabled(model.isUpdating)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") {
                        Task { await model.saveEditing() }
                    }
                    .disabled(model.isUpdating || model.draftValidationMessage != nil)
                }
            }
            .overlay {
                if model.isUpdating {
                    ProgressView("Saving…")
                        .padding()
                        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
                }
            }
        }
    }
}

/// A coloured status pill for a task's lifecycle status.
struct TaskStatusPill: View {
    let status: TaskStatus

    var body: some View {
        Text(status.title)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 7)
            .padding(.vertical, 2)
            .background(color.opacity(0.18), in: Capsule())
            .foregroundStyle(color)
    }

    private var color: Color {
        switch status {
        case .inProgress: return .green
        case .inReview: return .teal
        case .todo: return .blue
        case .blocked: return .orange
        case .proposed: return .purple
        case .done: return .gray
        case .unknown: return .gray
        }
    }
}
