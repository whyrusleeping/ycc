import SwiftUI
import YccKit
import YccProto

/// The backlog browser has two presentations over the same data:
///
/// - **Board** (default) — horizontally snapping kanban lanes in workflow order
///   (proposed → todo → in progress → in review → blocked → done), each a column
///   of cards. Empty lanes are kept so the board keeps its shape and so there is
///   always somewhere to move a card to.
/// - **List** — the compact sectioned list, for scanning a large backlog.
///
/// Tapping a card opens the task detail; the card's move menu and the long-press
/// context menu change status in place; the toolbar "+" opens quick capture. A
/// mid-screen `.unauthorized` failure routes back to the connect screen via
/// ``AppModel/handleUnauthorized()``.
struct BacklogView: View {
    @Environment(AppModel.self) private var app
    @Environment(HomeRouter.self) private var router

    @State private var model: BacklogModel?
    @State private var loopModel: WorkLoopModel?
    @State private var lastLoopKey: LoopSnapshotKey?
    @State private var showCapture = false
    @State private var showLoopStartConfirmation = false
    @State private var showLoopStopConfirmation = false
    /// Board vs list, remembered across launches.
    @AppStorage("backlog.presentation") private var presentation: Presentation = .board
    /// Card/row ordering, shared by both presentations and remembered across launches.
    @AppStorage("backlog.sort") private var backlogSort: BacklogSort = .newestFirst

    /// The project to scope the backlog to (carried from the landing view).
    private let initialProject: String

    enum Presentation: String {
        case board, list
    }

    private struct LoopSnapshotKey: Equatable {
        let project: String
        let state: WorkLoopState
        let sessionsRun: Int32
    }

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
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if let model, model.showsProjectFilter {
                ToolbarItem(placement: .topBarLeading) {
                    projectFilter(model)
                }
            }
            ToolbarItem(placement: .topBarTrailing) {
                presentationToggle
            }
            ToolbarItem(placement: .topBarTrailing) {
                sortControl
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
        .confirmationDialog(
            "Start unattended work loop?",
            isPresented: $showLoopStartConfirmation,
            titleVisibility: .visible
        ) {
            Button("Start loop") {
                guard let loopModel = scopedLoopModel else { return }
                Task { await loopModel.start() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The daemon will drain ready backlog tasks unattended until none remain or a budget cap trips. This can spend tokens while your phone is locked or suspended.")
        }
        .confirmationDialog(
            "Stop work loop gracefully?",
            isPresented: $showLoopStopConfirmation,
            titleVisibility: .visible
        ) {
            Button("Stop loop", role: .destructive) {
                guard let loopModel = scopedLoopModel else { return }
                Task { await loopModel.stop() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            if scopedLoopModel?.state == .waiting {
                Text("The loop is waiting for the provider; stopping ends the wait immediately. The daemon will not pick another backlog task.")
            } else {
                Text("The current session will finish. The daemon will not pick another backlog task.")
            }
        }
        .alert(
            "Action failed",
            isPresented: Binding(
                get: { scopedLoopModel?.actionError != nil },
                set: { if !$0 { scopedLoopModel?.actionError = nil } }),
            presenting: scopedLoopModel?.actionError
        ) { _ in
            Button("OK", role: .cancel) { scopedLoopModel?.actionError = nil }
        } message: { message in
            Text(message)
        }
        .task { await ensureLoaded() }
        .task(id: workLoopTaskID) { await loadAndPollWorkLoop() }
        .onChange(of: model?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
        .onChange(of: loopModel?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
        .onChange(of: backlogSort) { _, sort in
            model?.sort = sort
        }
    }

    private var presentationToggle: some View {
        Button {
            withAnimation(.snappy) {
                presentation = presentation == .board ? .list : .board
            }
        } label: {
            Label(
                presentation == .board ? "Show as list" : "Show as board",
                systemImage: presentation == .board
                    ? "list.bullet"
                    : "rectangle.split.3x1")
        }
        .disabled(model == nil)
    }

    private var sortControl: some View {
        Menu {
            Picker("Sort", selection: $backlogSort) {
                ForEach(BacklogSort.allCases) { sort in
                    Text(sort.title).tag(sort)
                }
            }
        } label: {
            Label("Sort backlog", systemImage: "arrow.up.arrow.down")
        }
        .disabled(model == nil)
    }

    private func content(_ model: BacklogModel) -> some View {
        VStack(spacing: 0) {
            if !model.selectedProject.isEmpty, app.client != nil {
                workLoopBanner(model)
                Divider()
            }
            backlogContent(model)
        }
    }

    @ViewBuilder
    private func backlogContent(_ model: BacklogModel) -> some View {
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
        } else if presentation == .board {
            BacklogBoard(model: model)
                .alert(
                    "Couldn’t update task",
                    isPresented: updateErrorBinding(model),
                    presenting: model.updateError
                ) { _ in
                    Button("OK", role: .cancel) { model.updateError = nil }
                } message: { message in
                    Text(message)
                }
        } else {
            taskList(model)
        }
    }

    private func workLoopBanner(_ model: BacklogModel) -> some View {
        let loopModel = scopedLoopModel
        let state = loopModel?.state ?? .none
        return HStack(spacing: 10) {
            Button {
                router.open(.workLoop(project: model.selectedProject))
            } label: {
                HStack(spacing: 8) {
                    WorkLoopStateBadge(state: state)
                    Text(WorkLoopModel.bannerLine(for: loopModel?.loop))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                    Spacer(minLength: 0)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            if loopModel?.canStart == true {
                Button {
                    showLoopStartConfirmation = true
                } label: {
                    Label("Start loop", systemImage: "play.circle")
                        .labelStyle(.iconOnly)
                }
                .accessibilityLabel("Start work loop")
            } else if state == .stopping {
                Button {} label: {
                    Label("Stopping…", systemImage: "stop.circle")
                        .font(.caption)
                }
                .disabled(true)
                .accessibilityLabel("Stopping work loop")
            } else if state.isActive {
                Button(role: .destructive) {
                    showLoopStopConfirmation = true
                } label: {
                    Label("Stop loop", systemImage: "stop.circle")
                        .labelStyle(.iconOnly)
                }
                .disabled(loopModel?.canStop != true)
                .accessibilityLabel("Stop work loop")
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .background(Color(.secondarySystemGroupedBackground))
    }

    private func updateErrorBinding(_ model: BacklogModel) -> Binding<Bool> {
        Binding(
            get: { model.updateError != nil },
            set: { if !$0 { model.updateError = nil } })
    }

    private func taskList(_ model: BacklogModel) -> some View {
        List {
            ForEach(model.sections) { section in
                Section(section.title) {
                    ForEach(section.tasks, id: \.id) { task in
                        NavigationLink(value: HomeDestination.taskDetail(
                            project: model.selectedProject,
                            taskID: task.id,
                            title: task.title)
                        ) {
                            BacklogRow(task: task, isUpdating: model.updatingTaskID == task.id)
                        }
                        .contextMenu {
                            statusMenu(model, task: task)
                        }
                        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                            swipeStatusButtons(model, task: task)
                        }
                    }
                }
            }
        }
        .refreshable { await model.refresh() }
        .alert(
            "Couldn’t update task",
            isPresented: updateErrorBinding(model),
            presenting: model.updateError
        ) { _ in
            Button("OK", role: .cancel) { model.updateError = nil }
        } message: { message in
            Text(message)
        }
    }

    /// Quick swipe shortcuts for the most common transitions: mark done, and
    /// start (todo/proposed/blocked → in progress). The full set lives in the
    /// long-press context menu.
    @ViewBuilder
    private func swipeStatusButtons(_ model: BacklogModel, task: Ycc_V1_BacklogTaskSummary) -> some View {
        let current = TaskStatus(status: task.status)
        if current != .done {
            Button {
                Task { await model.setStatus(taskID: task.id, to: .done) }
            } label: {
                Label("Done", systemImage: "checkmark.circle.fill")
            }
            .tint(.green)
        }
        if current != .inProgress, current != .done {
            Button {
                Task { await model.setStatus(taskID: task.id, to: .inProgress) }
            } label: {
                Label("Start", systemImage: "play.circle.fill")
            }
            .tint(.blue)
        }
    }

    private func projectFilter(_ model: BacklogModel) -> some View {
        @Bindable var model = model
        return Menu {
            Picker("Project", selection: $model.selectedProject) {
                ForEach(model.projects, id: \.name) { project in
                    Text(project.name).tag(project.name)
                }
            }
        } label: {
            Label(
                model.selectedProject.isEmpty ? "Choose project" : model.selectedProject,
                systemImage: "line.3.horizontal.decrease.circle")
        }
        .onChange(of: model.selectedProject) { _, _ in
            Task { await model.refresh() }
        }
    }

    private var scopedLoopModel: WorkLoopModel? {
        guard let model, let loopModel,
              !model.selectedProject.isEmpty,
              loopModel.project == model.selectedProject else { return nil }
        return loopModel
    }

    /// The project scopes the model; the polling flag restarts this task when an
    /// action moves the loop into or out of an active state.
    private var workLoopTaskID: String {
        let project = model?.selectedProject ?? ""
        let polling = scopedLoopModel?.shouldPoll == true
        let clientAvailable = app.client != nil
        return "\(project)|\(polling)|\(clientAvailable)"
    }

    /// Refresh once, then poll while active. Loop lifecycle/session changes also
    /// reload the backlog so cards track unattended progress across lanes.
    private func loadAndPollWorkLoop() async {
        guard let backlogModel = model else {
            loopModel = nil
            return
        }
        let project = backlogModel.selectedProject
        guard !project.isEmpty, let client = app.client else {
            loopModel = nil
            return
        }
        if loopModel?.project != project {
            loopModel = WorkLoopModel(source: client, project: project)
        }
        guard let activeLoopModel = loopModel else { return }

        await activeLoopModel.refresh()
        recordLoopSnapshot(
            activeLoopModel,
            backlogModel: backlogModel,
            project: project)

        while !Task.isCancelled, activeLoopModel.shouldPoll {
            do {
                try await Task.sleep(nanoseconds: 5_000_000_000)
            } catch {
                return
            }
            guard let currentLoopModel = loopModel,
                  currentLoopModel === activeLoopModel,
                  backlogModel.selectedProject == project else { return }

            await activeLoopModel.refresh()
            recordLoopSnapshot(
                activeLoopModel,
                backlogModel: backlogModel,
                project: project)
        }
    }

    /// Persist the last observed loop snapshot across `.task(id:)` restarts. The
    /// backlog reload runs in an unstructured task so a polling-flag transition
    /// cannot cancel the final lane update as the loop finishes.
    private func recordLoopSnapshot(
        _ loopModel: WorkLoopModel,
        backlogModel: BacklogModel,
        project: String
    ) {
        let key = LoopSnapshotKey(
            project: project,
            state: loopModel.state,
            sessionsRun: loopModel.loop?.sessionsRun ?? 0)
        let previousKey = lastLoopKey
        lastLoopKey = key

        guard let previousKey,
              previousKey.project == project,
              previousKey != key else { return }
        Task {
            guard backlogModel.selectedProject == project else { return }
            await backlogModel.refresh()
        }
    }

    private func ensureLoaded() async {
        if model == nil {
            guard let client = app.client else { return }
            let backlogModel = BacklogModel(source: client, selectedProject: initialProject)
            backlogModel.sort = backlogSort
            model = backlogModel
        }
        await model?.refresh()
    }
}

/// The long-press status changer: every selectable status, with a checkmark on
/// the row's current one. Shared by the list rows and the board cards.
@MainActor
@ViewBuilder
func statusMenu(_ model: BacklogModel, task: Ycc_V1_BacklogTaskSummary) -> some View {
    let current = TaskStatus(status: task.status)
    Section("Move to") {
        ForEach(TaskStatus.selectable) { status in
            Button {
                Task { await model.setStatus(taskID: task.id, to: status) }
            } label: {
                if status == current {
                    Label(status.title, systemImage: "checkmark")
                } else {
                    Text(status.title)
                }
            }
            .disabled(status == current || model.updatingTaskID != nil)
        }
    }
}

// MARK: - Board

/// Horizontally snapping kanban lanes in workflow order, with proposed kept
/// leftmost. The board initially focuses todo, while the previous lane's trailing
/// edge peeks in on the left as a hint that the user can swipe back.
private struct BacklogBoard: View {
    let model: BacklogModel

    @State private var containerWidth: CGFloat = 375
    @State private var focusedLane: String? = TaskStatus.todo.rawValue

    private var laneWidth: CGFloat { min(max(containerWidth * 0.82, 250), 340) }

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            LazyHStack(alignment: .top, spacing: 12) {
                ForEach(model.board) { lane in
                    BacklogLane(model: model, lane: lane)
                        .frame(width: laneWidth)
                }
            }
            // Must sit directly on the layout container for `.viewAligned`
            // snapping to find the lane boundaries — hence content margins
            // rather than padding for the inset.
            .scrollTargetLayout()
        }
        .contentMargins(.leading, 30, for: .scrollContent)
        .contentMargins(.trailing, 12, for: .scrollContent)
        .contentMargins(.vertical, 10, for: .scrollContent)
        .scrollTargetBehavior(.viewAligned)
        .scrollPosition(id: $focusedLane, anchor: .leading)
        .background(Color(.systemGroupedBackground))
        .background {
            GeometryReader { geometry in
                Color.clear
                    .onAppear { containerWidth = geometry.size.width }
                    .onChange(of: geometry.size.width) { _, width in containerWidth = width }
            }
        }
    }
}

/// One lane: a sticky header with its status and count, over a scrolling column
/// of cards.
private struct BacklogLane: View {
    let model: BacklogModel
    let lane: BacklogSection

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                TaskStatusPill(status: lane.status)
                Text("\(lane.tasks.count)")
                    .font(.caption.weight(.semibold))
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
                Spacer()
            }
            .padding(.horizontal, 2)

            if lane.tasks.isEmpty {
                emptyLane
            } else {
                ScrollView(.vertical, showsIndicators: false) {
                    LazyVStack(spacing: 8) {
                        ForEach(lane.tasks, id: \.id) { task in
                            card(task)
                        }
                    }
                    .padding(.bottom, 8)
                }
                // Pull-to-refresh belongs on the vertical scroll: the board's
                // outer scroll is horizontal and cannot host it.
                .refreshable { await model.refresh() }
            }
            Spacer(minLength: 0)
        }
        // Claim the full proposed height so the inner vertical ScrollView has a
        // bounded height to lay out against.
        .frame(maxHeight: .infinity, alignment: .top)
    }

    @ViewBuilder
    private func card(_ task: Ycc_V1_BacklogTaskSummary) -> some View {
        NavigationLink(value: HomeDestination.taskDetail(
            project: model.selectedProject,
            taskID: task.id,
            title: task.title)
        ) {
            BacklogCard(
                model: model,
                task: task,
                isUpdating: model.updatingTaskID == task.id)
        }
        .buttonStyle(.plain)
        .contextMenu { statusMenu(model, task: task) }
    }

    private var emptyLane: some View {
        RoundedRectangle(cornerRadius: 12, style: .continuous)
            .strokeBorder(style: StrokeStyle(lineWidth: 1, dash: [5, 4]))
            .foregroundStyle(.quaternary)
            .frame(height: 84)
            .overlay {
                Text("Nothing \(lane.status.title.lowercased())")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }
    }
}

/// A board card: id + priority, title, readiness, and a move menu. The move menu
/// is its own hit target (rather than only a long-press) so the "push this card
/// to the next lane" action is actually discoverable.
private struct BacklogCard: View {
    let model: BacklogModel
    let task: Ycc_V1_BacklogTaskSummary
    var isUpdating = false

    private var status: TaskStatus { TaskStatus(status: task.status) }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Text(task.id)
                    .font(.caption2.monospaced())
                    .foregroundStyle(.secondary)
                Spacer(minLength: 4)
                if isUpdating {
                    ProgressView().controlSize(.mini)
                }
                PriorityBadge(priority: task.priority)
            }
            Text(task.title.isEmpty ? "(untitled)" : task.title)
                .font(.subheadline.weight(.medium))
                .foregroundStyle(.primary)
                .multilineTextAlignment(.leading)
                .lineLimit(4)
                .frame(maxWidth: .infinity, alignment: .leading)

            HStack(spacing: 6) {
                if let annotation = BacklogModel.blockedAnnotation(for: task) {
                    Label(annotation, systemImage: "lock.fill")
                        .font(.caption2)
                        .foregroundStyle(.orange)
                        .lineLimit(1)
                } else if task.ready, status != .done {
                    Label("Ready", systemImage: "checkmark.circle")
                        .font(.caption2)
                        .foregroundStyle(.green)
                }
                Spacer(minLength: 4)
                moveMenu
            }
        }
        .padding(10)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .strokeBorder(Color(.separator).opacity(0.5)))
        .opacity(isUpdating ? 0.6 : 1)
    }

    /// Quick lane movement: the workflow neighbours first (the common case),
    /// then every status for a jump.
    private var moveMenu: some View {
        Menu {
            if let next = status.nextBoardColumn {
                Button {
                    Task { await model.setStatus(taskID: task.id, to: next) }
                } label: {
                    Label("Move to \(next.title)", systemImage: "arrow.right")
                }
            }
            if let previous = status.previousBoardColumn {
                Button {
                    Task { await model.setStatus(taskID: task.id, to: previous) }
                } label: {
                    Label("Move to \(previous.title)", systemImage: "arrow.left")
                }
            }
            Divider()
            statusMenu(model, task: task)
        } label: {
            Image(systemName: "arrow.left.arrow.right")
                .font(.caption2)
                .foregroundStyle(.secondary)
                .padding(4)
                .contentShape(Rectangle())
        }
        .disabled(model.updatingTaskID != nil)
        .accessibilityLabel("Move task \(task.id)")
    }
}

// MARK: - List

/// A single backlog row: id + title, a priority badge, a ready/blocked
/// annotation matching ListBacklog semantics, and a spinner while a status
/// change for this row is in flight.
private struct BacklogRow: View {
    let task: Ycc_V1_BacklogTaskSummary
    var isUpdating = false

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
                if isUpdating {
                    ProgressView()
                        .controlSize(.small)
                }
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
