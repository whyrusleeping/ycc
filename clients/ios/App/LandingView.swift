import SwiftUI
import YccKit
import YccProto

/// The authenticated home screen: the daemon's session history, most-recent
/// first, with a needs-answer section pinned to the top (docs/design/ios-client
/// .md §6 phase 1 step 2). Live rows stream when opened; persisted rows render
/// the replayed transcript. A mid-session `.unauthorized` failure routes back
/// to the connect screen via ``AppModel/handleUnauthorized()``.
///
/// Navigation follows the design's shell (§6 "Navigation shell"): a left-edge
/// workspace drawer owns project selection and the project destinations
/// (backlog / workstreams / usage) plus global settings, leaving the toolbar
/// with just the menu button and the primary "new chat" action.
struct LandingView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.scenePhase) private var scenePhase

    @State private var model: SessionListModel?
    /// The navigation stack's path. Path-driven so the drawer can push a
    /// destination over whatever is currently on screen.
    @State private var path: [HomeDestination] = []
    /// Whether the workspace drawer is revealed.
    @State private var drawerOpen = false
    /// Whether the "new session" composer sheet is shown.
    @State private var showNewSession = false
    /// Project selected by the explicit picker shown when New Chat is tapped
    /// from the daemon-wide Recent Sessions feed.
    @State private var newSessionProject: String?
    /// Whether the Recent Sessions project picker is visible.
    @State private var showNewSessionProjectPicker = false
    /// A resume failure message to surface as an alert.
    @State private var resumeError: String?
    /// A deep-link routing failure (unknown/stale session or project) to surface
    /// as an alert — a graceful landing instead of navigating into a dead view.
    @State private var deepLinkError: String?
    /// Whether the "add project" sheet is shown (from the drawer).
    @State private var showAddProject = false
    /// A registered project awaiting destructive deregistration confirmation.
    @State private var projectToRemove: Ycc_V1_ProjectInfo?
    /// A project-removal failure to show independently of session-list content.
    @State private var projectRemovalError: String?

    var body: some View {
        DrawerContainer(isOpen: $drawerOpen, edgeSwipeEnabled: path.isEmpty) {
            drawer
        } content: {
            navigation
        }
        .confirmationDialog(
            "Start a new chat in…",
            isPresented: $showNewSessionProjectPicker,
            titleVisibility: .visible
        ) {
            if let model {
                ForEach(model.newSessionProjectChoices, id: \.self) { project in
                    Button(project) {
                        newSessionProject = project
                        showNewSession = true
                    }
                }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Choose which project this chat should work in.")
        }
        .sheet(isPresented: $showNewSession) {
            if let client = app.client {
                // A scoped Sessions list starts in its current project. Recent
                // Sessions first records an explicit project choice above.
                NewSessionView(
                    client: client,
                    initialProject: newSessionProject
                ) { sessionID, project in
                    showNewSession = false
                    // Follow the session's project so the list shows it when
                    // the user backs out of the live view.
                    if model?.selectedProject != project {
                        model?.selectedProject = project
                        Task { await model?.refresh() }
                    }
                    path.append(.session(id: sessionID, project: project, live: true, title: ""))
                }
            }
        }
        .sheet(isPresented: $showAddProject) {
            if let client = app.client {
                AddProjectView(client: client) { project in
                    // Select and refresh so the new project shows up in the
                    // drawer (and every other picker's next load).
                    model?.selectedProject = project.name
                    Task { await model?.refresh() }
                }
            }
        }
        .confirmationDialog(
            "Remove \(projectToRemove?.name ?? "project")?",
            isPresented: Binding(
                get: { projectToRemove != nil },
                set: { if !$0 { projectToRemove = nil } }),
            titleVisibility: .visible,
            presenting: projectToRemove
        ) { project in
            Button("Remove project", role: .destructive) {
                removeProject(project)
            }
            Button("Cancel", role: .cancel) { projectToRemove = nil }
        } message: { project in
            Text("This removes \(project.name) from ycc. It does not delete the workspace or any files on the daemon host.")
        }
        .alert(
            "Couldn’t remove project",
            isPresented: Binding(
                get: { projectRemovalError != nil },
                set: { if !$0 { projectRemovalError = nil } }),
            presenting: projectRemovalError
        ) { _ in
            Button("OK", role: .cancel) { projectRemovalError = nil }
        } message: { message in
            Text(message)
        }
        .alert(
            "Couldn’t resume",
            isPresented: Binding(
                get: { resumeError != nil },
                set: { if !$0 { resumeError = nil } }),
            presenting: resumeError
        ) { _ in
            Button("OK", role: .cancel) { resumeError = nil }
        } message: { message in
            Text(message)
        }
        .alert(
            "Couldn’t open link",
            isPresented: Binding(
                get: { deepLinkError != nil },
                set: { if !$0 { deepLinkError = nil } }),
            presenting: deepLinkError
        ) { _ in
            Button("OK", role: .cancel) { deepLinkError = nil }
        } message: { message in
            Text(message)
        }
        .task {
            await ensureLoaded()
            await consumePendingDeepLink()
        }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { Task { await model?.refresh() } }
        }
        .onChange(of: app.pendingDeepLink) { _, link in
            if link != nil { Task { await consumePendingDeepLink() } }
        }
        // Apply locally-known answers straight away, so the inbox and drawer
        // badges stop nagging the moment the user answers rather than at the
        // next manual refresh.
        .onChange(of: app.answeredQuestionSessions) { _, answered in
            guard !answered.isEmpty else { return }
            for sessionID in answered { model?.markAnswered(sessionID: sessionID) }
            app.clearAnsweredQuestionNotices()
        }
        // Returning to the inbox is a strong signal that its rows are stale:
        // the agent has usually moved on while the user was inside a session.
        .onChange(of: path.isEmpty) { _, isAtRoot in
            if isAtRoot { Task { await model?.refresh() } }
        }
        .onChange(of: model?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
    }

    // MARK: - Shell

    @ViewBuilder
    private var drawer: some View {
        if let model {
            WorkspaceDrawer(
                serverName: app.store.activeProfile?.name ?? "",
                model: model,
                onSelectProject: { project in
                    // Filtering is client-side over the aggregate load, so this
                    // is instant — no refetch, no spinner.
                    model.selectedProject = project
                    closeDrawer()
                },
                onOpen: { destination in
                    closeDrawer()
                    path.append(destination)
                },
                onAddProject: {
                    closeDrawer()
                    showAddProject = true
                },
                onRemoveProject: { project in
                    closeDrawer()
                    projectToRemove = project
                },
                onDisconnect: {
                    closeDrawer()
                    app.disconnect()
                })
        }
    }

    private var navigation: some View {
        NavigationStack(path: $path) {
            Group {
                if let model {
                    content(model)
                } else {
                    ProgressView()
                }
            }
            .navigationTitle(model?.selectedProject ?? "Recent")
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button {
                        // The container owns the open/close animation.
                        drawerOpen = true
                    } label: {
                        Label("Workspaces", systemImage: "line.3.horizontal")
                    }
                    .overlay(alignment: .topTrailing) {
                        // Mirror the drawer's loudest badge on the closed menu
                        // button so a question in another project is visible
                        // without opening anything.
                        if (model?.totalActivity.needsAnswer ?? 0) > 0 {
                            Circle()
                                .fill(Color.orange)
                                .frame(width: 8, height: 8)
                                .offset(x: 3, y: -2)
                        }
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { beginNewSession() } label: {
                        Label("New chat", systemImage: "square.and.pencil")
                    }
                }
            }
            .navigationDestination(for: HomeDestination.self) { destination in
                self.destination(destination)
            }
        }
    }

    @ViewBuilder
    private func destination(_ destination: HomeDestination) -> some View {
        // Defensive: a mid-session disconnect nils the client; RootView swaps to
        // the connect screen, so these branches are effectively unreachable —
        // but never force-unwrap in a lazily-built destination.
        if let client = app.client {
            switch destination {
            case let .session(id, project, live, title):
                SessionView(
                    client: client, project: project, sessionID: id,
                    live: live, title: title)
            case let .backlog(project):
                BacklogView(initialProject: project)
            case let .workstreams(project):
                WorkstreamsView(initialProject: project)
            case let .usage(project):
                UsageView(initialProject: project)
            case .settings:
                GlobalSettingsView(client: client)
            }
        }
    }

    private func closeDrawer() {
        drawerOpen = false
    }

    /// Start directly in a scoped project's composer, or require an explicit
    /// project choice from the daemon-wide Recent Sessions feed.
    private func beginNewSession() {
        guard let model else { return }
        if model.requiresProjectChoiceForNewSession {
            newSessionProject = nil
            showNewSessionProjectPicker = true
        } else {
            newSessionProject = model.selectedProject
            showNewSession = true
        }
    }

    /// Re-open a persisted session on its existing log, then navigate into the
    /// live view. Idempotent server-side if the session is already live.
    private func resume(_ session: Ycc_V1_SessionSummary) {
        guard let client = app.client else { return }
        let project = model?.project(for: session) ?? ""
        Task {
            do {
                let sessionID = try await client.resumeSession(
                    project: project, sessionId: session.sessionID)
                path.append(.session(
                    id: sessionID, project: project, live: true,
                    title: SessionListModel.displayTitle(for: session)))
            } catch YccError.unauthorized {
                app.handleUnauthorized()
            } catch let YccError.rpc(message) {
                resumeError = message
            } catch let YccError.notFound(message) {
                resumeError = message
            } catch let YccError.failedPrecondition(message) {
                resumeError = message
            } catch {
                resumeError = error.localizedDescription
            }
        }
    }

    // MARK: - Session list

    @ViewBuilder
    private func content(_ model: SessionListModel) -> some View {
        if model.isLoading && model.sessions.isEmpty {
            ProgressView()
        } else if let errorMessage = model.errorMessage, model.sessions.isEmpty {
            refreshableUnavailable(model) {
                ContentUnavailableView(
                    "Couldn’t load sessions",
                    systemImage: "exclamationmark.triangle",
                    description: Text(errorMessage))
            }
        } else if model.sessions.isEmpty {
            refreshableUnavailable(model) {
                ContentUnavailableView {
                    Label(
                        emptyTitle(model),
                        systemImage: model.partialWarning == nil
                            ? "bubble.left.and.bubble.right"
                            : "exclamationmark.triangle")
                } description: {
                    Text(model.partialWarning ?? "Start one with the compose button.")
                } actions: {
                    if model.partialWarning == nil {
                        Button("New chat") { beginNewSession() }
                            .buttonStyle(.borderedProminent)
                    }
                }
            }
        } else {
            sessionList(model)
        }
    }

    private func emptyTitle(_ model: SessionListModel) -> String {
        guard let project = model.selectedProject else { return "No sessions yet" }
        return "Nothing in \(project) yet"
    }

    /// Wraps an empty/error placeholder in a full-size scroll view so
    /// pull-to-refresh works even when there is no list to pull on.
    private func refreshableUnavailable<Content: View>(
        _ model: SessionListModel, @ViewBuilder content: () -> Content
    ) -> some View {
        let placeholder = content()
        return GeometryReader { proxy in
            ScrollView {
                placeholder
                    .frame(width: proxy.size.width, height: proxy.size.height)
            }
        }
        .refreshable { await model.refresh() }
    }

    private func sessionList(_ model: SessionListModel) -> some View {
        List {
            if let warning = model.partialWarning {
                Label(warning, systemImage: "exclamationmark.triangle.fill")
                    .font(.footnote)
                    .foregroundStyle(.orange)
                    .accessibilityLabel("Partial results. \(warning)")
            }
            ForEach(model.sections) { section in
                Section {
                    ForEach(section.sessions, id: \.sessionID) { session in
                        NavigationLink(value: HomeDestination.session(
                            id: session.sessionID,
                            project: model.project(for: session),
                            live: session.live,
                            title: SessionListModel.displayTitle(for: session))
                        ) {
                            SessionRow(
                                session: session,
                                project: model.project(for: session),
                                showsProject: model.selectedProject == nil)
                        }
                        .listRowBackground(section.kind == .needsAnswer
                            ? Color.orange.opacity(0.12) : nil)
                        // Persisted (non-live) rows can be re-opened on their
                        // existing log via ResumeSession.
                        .swipeActions(edge: .leading) {
                            if !session.live {
                                Button { resume(session) } label: {
                                    Label("Resume", systemImage: "play.circle")
                                }
                                .tint(.green)
                            }
                        }
                        .contextMenu {
                            if !session.live {
                                Button { resume(session) } label: {
                                    Label("Resume session", systemImage: "play.circle")
                                }
                            }
                        }
                    }
                } header: {
                    if let title = section.title {
                        Label {
                            Text(title)
                        } icon: {
                            if section.kind == .needsAnswer {
                                Image(systemName: "bell.badge.fill")
                                    .foregroundStyle(.orange)
                            }
                        }
                    }
                }
            }
        }
        .listStyle(.insetGrouped)
        .refreshable { await model.refresh() }
    }

    private func removeProject(_ project: Ycc_V1_ProjectInfo) {
        guard let model else { return }
        projectToRemove = nil
        Task {
            let removed = await model.removeProject(named: project.name)
            if !removed, !model.unauthorized {
                projectRemovalError = model.errorMessage ?? "The project could not be removed."
            }
        }
    }

    private func ensureLoaded() async {
        if model == nil {
            guard let client = app.client else { return }
            model = SessionListModel(source: client)
        }
        await model?.refresh()
    }

    // MARK: - Deep links (task 0186)

    /// Consume a parked `ycc://` deep link once the landing view is loaded:
    /// a session link resolves to a live/persisted open (or a graceful alert on
    /// an unknown id); a project link sets the drawer scope (or alerts on an
    /// unknown project).
    private func consumePendingDeepLink() async {
        guard let link = app.pendingDeepLink else { return }
        app.pendingDeepLink = nil
        // The list must be loaded so resolution can consult the current sessions
        // and the registered project list.
        if model == nil { await ensureLoaded() }
        switch link {
        case .session(let id, _):
            await openSession(id)
        case .project(let name):
            await openProject(name)
        }
    }

    /// Resolve a session id to the right project + live flag and navigate to it.
    /// Checks the already-loaded aggregate first, then scans every registered
    /// project. An id found nowhere yields a graceful alert.
    private func openSession(_ sessionID: String) async {
        guard let client = app.client else { return }
        if let match = model?.allSessions.first(where: { $0.sessionID == sessionID }) {
            path.append(.session(
                id: sessionID,
                project: model?.project(for: match) ?? "",
                live: match.live,
                title: SessionListModel.displayTitle(for: match)))
            return
        }
        do {
            var scanned = Set<String>()
            let projectsToScan = (model?.projects ?? []).map(\.name)
            for project in projectsToScan {
                guard scanned.insert(project).inserted else { continue }
                let sessions = try await client.listSessionHistory(project: project)
                if let match = sessions.first(where: { $0.sessionID == sessionID }) {
                    path.append(.session(
                        id: sessionID, project: project, live: match.live,
                        title: SessionListModel.displayTitle(for: match)))
                    return
                }
            }
            deepLinkError = "Session \(sessionID) was not found on this server."
        } catch YccError.unauthorized {
            app.handleUnauthorized()
        } catch let YccError.rpc(message) {
            deepLinkError = message
        } catch {
            deepLinkError = error.localizedDescription
        }
    }

    /// Apply a project deep link as the drawer scope if the project is registered.
    private func openProject(_ name: String) async {
        guard let model else { return }
        guard model.projects.contains(where: { $0.name == name }) else {
            deepLinkError = "Project “\(name)” is not registered on this server."
            return
        }
        model.selectedProject = name
    }
}

/// A single session row: title, status badge, live marker, needs-answer marker,
/// turns, and a relative last-activity time.
private struct SessionRow: View {
    let session: Ycc_V1_SessionSummary
    var project = ""
    var showsProject = false

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                if session.live && session.waitingInput {
                    Image(systemName: "bell.badge.fill")
                        .foregroundStyle(.orange)
                        .font(.subheadline)
                }
                Text(SessionListModel.displayTitle(for: session))
                    .font(.headline)
                    // Titles are derived from the opening prompt, so one line
                    // truncates almost every row into uselessness.
                    .lineLimit(2)
                Spacer(minLength: 4)
                if session.live {
                    Label("Live", systemImage: "dot.radiowaves.left.and.right")
                        .labelStyle(.iconOnly)
                        .foregroundStyle(.green)
                        .font(.caption)
                }
            }
            HStack(spacing: 8) {
                StatusBadge(status: session.status)
                if showsProject {
                    Label(project, systemImage: "folder")
                        .lineLimit(1)
                }
                if session.turns > 0 {
                    Text("\(session.turns) turns")
                }
                Spacer(minLength: 4)
                if let text = relativeLastActivity {
                    Text(text)
                }
            }
            .font(.caption)
            .foregroundStyle(.secondary)
        }
        .padding(.vertical, 2)
    }

    private var relativeLastActivity: String? {
        guard let date = SessionListModel.recencyDate(session) else { return nil }
        return date.formatted(Date.RelativeFormatStyle(presentation: .named))
    }
}

/// A coloured status pill for a session's `running`/`idle`/`error`/`paused`/
/// `stopped` state.
private struct StatusBadge: View {
    let status: String

    var body: some View {
        Text(label)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 7)
            .padding(.vertical, 2)
            .background(color.opacity(0.18), in: Capsule())
            .foregroundStyle(color)
    }

    private var kind: SessionStatusKind { SessionStatusKind(status: status) }

    private var label: String {
        kind == .unknown ? (status.isEmpty ? "unknown" : status) : kind.rawValue
    }

    private var color: Color {
        switch kind {
        case .running: return .green
        case .error: return .red
        case .paused: return .orange
        // `idle` is the overwhelmingly common state of a session list, so a
        // loud colour there is pure noise — reserve colour for what needs you.
        case .idle, .stopped, .unknown: return .gray
        }
    }
}
