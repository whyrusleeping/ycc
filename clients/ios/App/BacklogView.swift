import SwiftUI
import YccKit
import YccProto

/// The backlog browser (docs/design/ios-client.md §6 phase 2 step 6, spec
/// §18.5): the daemon's durable backlog grouped into ordered status sections,
/// with ready/blocked annotations and priority. Tapping a row opens the task
/// detail; the toolbar "+" opens a quick-capture sheet. A mid-screen
/// `.unauthorized` failure routes back to the connect screen via
/// ``AppModel/handleUnauthorized()``.
struct BacklogView: View {
    @Environment(AppModel.self) private var app

    @State private var model: BacklogModel?
    @State private var showCapture = false

    /// The project to scope the backlog to (carried from the landing view).
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
        .navigationTitle("Backlog")
        .toolbar {
            if let model, model.showsProjectFilter {
                ToolbarItem(placement: .topBarLeading) {
                    projectFilter(model)
                }
            }
            ToolbarItem(placement: .topBarTrailing) {
                Button { showCapture = true } label: {
                    Label("Capture task", systemImage: "plus")
                }
                .disabled(model == nil)
            }
        }
        .sheet(isPresented: $showCapture) {
            if let model {
                QuickCaptureView(model: model)
            }
        }
        .task { await ensureLoaded() }
        .onChange(of: model?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
    }

    @ViewBuilder
    private func content(_ model: BacklogModel) -> some View {
        if model.isLoading && model.tasks.isEmpty {
            ProgressView()
        } else if let errorMessage = model.errorMessage, model.tasks.isEmpty {
            ContentUnavailableView(
                "Couldn’t load backlog",
                systemImage: "exclamationmark.triangle",
                description: Text(errorMessage))
        } else if model.tasks.isEmpty {
            ContentUnavailableView {
                Label("Backlog is empty", systemImage: "checklist")
            } description: {
                Text("Capture a task with the + button.")
            } actions: {
                Button("Capture task") { showCapture = true }
            }
        } else {
            taskList(model)
        }
    }

    private func taskList(_ model: BacklogModel) -> some View {
        List {
            ForEach(model.sections) { section in
                Section(section.title) {
                    ForEach(section.tasks, id: \.id) { task in
                        NavigationLink {
                            if let client = app.client {
                                TaskDetailView(
                                    client: client,
                                    project: model.selectedProject,
                                    taskID: task.id,
                                    taskTitle: task.title)
                            }
                        } label: {
                            BacklogRow(task: task)
                        }
                    }
                }
            }
        }
        .refreshable { await model.refresh() }
    }

    private func projectFilter(_ model: BacklogModel) -> some View {
        @Bindable var model = model
        return Menu {
            Picker("Project", selection: $model.selectedProject) {
                Text("Default").tag("")
                ForEach(model.projects, id: \.name) { project in
                    Text(project.name).tag(project.name)
                }
            }
        } label: {
            Label(
                model.selectedProject.isEmpty ? "Default" : model.selectedProject,
                systemImage: "line.3.horizontal.decrease.circle")
        }
        .onChange(of: model.selectedProject) { _, _ in
            Task { await model.refresh() }
        }
    }

    private func ensureLoaded() async {
        if model == nil {
            guard let client = app.client else { return }
            model = BacklogModel(source: client, selectedProject: initialProject)
        }
        await model?.refresh()
    }
}

/// A single backlog row: id + title, a priority badge, and a ready/blocked
/// annotation matching ListBacklog semantics.
private struct BacklogRow: View {
    let task: Ycc_V1_BacklogTaskSummary

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Text(task.id)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                Text(task.title.isEmpty ? "(untitled)" : task.title)
                    .font(.headline)
                    .lineLimit(2)
                Spacer(minLength: 4)
                PriorityBadge(priority: task.priority)
            }
            if let annotation = BacklogModel.blockedAnnotation(for: task) {
                Label(annotation, systemImage: "lock.fill")
                    .font(.caption)
                    .foregroundStyle(.orange)
            } else if task.ready, TaskStatus(status: task.status) != .done {
                Label("Ready", systemImage: "checkmark.circle")
                    .font(.caption)
                    .foregroundStyle(.green)
            }
        }
        .padding(.vertical, 2)
    }
}

/// A small "P3" priority pill. Priority is 1..5 (1 = highest); 0 renders nothing.
struct PriorityBadge: View {
    let priority: Int32

    var body: some View {
        if priority > 0 {
            Text("P\(priority)")
                .font(.caption2.weight(.semibold))
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(color.opacity(0.18), in: Capsule())
                .foregroundStyle(color)
        }
    }

    private var color: Color {
        switch priority {
        case 1: return .red
        case 2: return .orange
        case 3: return .yellow
        case 4: return .blue
        default: return .gray
        }
    }
}
