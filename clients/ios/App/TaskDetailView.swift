import SwiftUI
import YccKit
import YccProto

/// The task-detail screen (docs/design/ios-client.md §6 phase 2 step 6, spec
/// §18.5): the frontmatter header (status, priority, dependencies, ready/blocked,
/// dates) plus the markdown `body`, a status picker driving `UpdateTask`, and a
/// "Start work on this task" action that starts a work session (`StartSession`)
/// and navigates into its live stream.
struct TaskDetailView: View {
    @Environment(AppModel.self) private var app

    @State private var model: TaskDetailModel
    /// The session to push into a live streaming view (set after Start work).
    @State private var liveTarget: LiveTaskSessionTarget?
    /// A start-work failure message to surface as an alert.
    @State private var startError: String?
    @State private var isStarting = false

    private let taskTitle: String
    private let project: String

    init(client: YccClient, project: String, taskID: String, taskTitle: String) {
        _model = State(initialValue: TaskDetailModel(source: client, project: project, taskID: taskID))
        self.taskTitle = taskTitle
        self.project = project
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
            ToolbarItem(placement: .topBarTrailing) { statusMenu }
        }
        .navigationDestination(item: $liveTarget) { target in
            if let client = app.client {
                SessionView(
                    client: client,
                    project: target.project,
                    sessionID: target.sessionID,
                    live: true)
            }
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
                startWorkButton(task)
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
            TaskStatusPill(status: model.status)
            PriorityBadge(priority: task.priority)
            if task.ready, model.status != .done {
                Label("Ready", systemImage: "checkmark.circle")
                    .font(.caption)
                    .foregroundStyle(.green)
            }
        }
        if !task.blockedBy.isEmpty, model.status != .done {
            Label("Blocked by " + task.blockedBy.joined(separator: ", "), systemImage: "lock.fill")
                .font(.caption)
                .foregroundStyle(.orange)
        }
        if !task.dependsOn.isEmpty {
            metaRow("Depends on", task.dependsOn.joined(separator: ", "))
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

    private var statusMenu: some View {
        Menu {
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
                    project: project, mode: "work", prompt: prompt, interactionLevel: "judgement")
                liveTarget = LiveTaskSessionTarget(sessionID: sessionID, project: project)
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

/// A session to push into a live streaming view after "Start work".
private struct LiveTaskSessionTarget: Identifiable, Hashable {
    let sessionID: String
    let project: String
    var id: String { sessionID }
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
