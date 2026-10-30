import SwiftUI
import YccKit
import YccProto

/// The authenticated home screen: the daemon's session history, most-recent
/// first, with a needs-answer section pinned to the top. Live rows stream when
/// opened; persisted rows render the replayed transcript. A mid-session `.unauthorized` failure routes back
/// to the connect screen via ``AppModel/handleUnauthorized()``.
///
/// A left-edge workspace drawer owns project selection plus global settings,
/// while the
/// project-scoped destinations (backlog / workstreams / usage) hang off the
/// toolbar of the project's own session list — they need a project, and the
/// drawer's unscoped Recent-sessions feed has none to give them.
struct LandingView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.scenePhase) private var scenePhase

    @State private var model: SessionListModel?
    /// The app cache generation the session list model was created under; its
    /// project list must never repopulate a cache cleared by a server switch.
    @State private var modelCacheGeneration: UInt64 = 0
    /// The navigation router. Path-driven so the drawer can open a destination
    /// over whatever is currently on screen, and shared through the environment
    /// so cross-links between screens replace the stack instead of piling onto
    /// it — one Back always returns here (see ``HomeRouter``).
    @State private var router = HomeRouter()
    /// Whether the workspace drawer is revealed.
    @State private var drawerOpen = false
    /// The pending "new session" composer presentation, carrying the project it
    /// must open in. Kept as the sheet's *item* (rather than a bare flag plus a
    /// separate project `@State`) so the composer can never be presented with a
    /// stale project and silently fall back to the last-*started* one.
    @State private var newSessionRequest: NewSessionRequest?
    /// Whether the unscoped feed's backlog project picker is visible.
    @State private var showBacklogProjectPicker = false
    /// The last project the user actually looked at, remembered across launches.
    /// The unscoped Recent Sessions feed has no project of its own, so this is
    /// the choice its backlog chooser offers first.
    @AppStorage("ycc.lastViewedProject") private var lastViewedProject = ""
    /// A deep-link routing failure (unknown/stale session or project) to surface
    /// as an alert — a graceful landing instead of navigating into a dead view.
    @State private var deepLinkError: String?
    /// Whether the "add project" sheet is shown (from the drawer).
    @State private var showAddProject = false
    /// A registered project awaiting destructive deregistration confirmation.
    @State private var projectToRemove: Ycc_V1_ProjectInfo?
    /// A project-removal failure to show independently of session-list content.
    @State private var projectRemovalError: String?
    /// A registered project awaiting a new name in the rename prompt.
    @State private var projectToRename: Ycc_V1_ProjectInfo?
    /// The rename prompt's draft text, seeded with the current name.
    @State private var renameDraft = ""
    /// A rename failure (collision, unknown name) surfaced as its own alert.
    @State private var projectRenameError: String?
    @State private var followUpError: String?
    /// Bumped whenever the list comes back into view (popping back to it,
    /// returning to the foreground) to rebuild running rows' backgrounds. On
    /// device, a running row's background was seen to come back fully blank
    /// after the list sat hidden under a session view; rebuilding it on
    /// reappearance redraws it. Not reproduced in the simulator.
    @State private var rowBackgroundEpoch = 0
    /// Daemon dependency health, behind the alert row atop the list; created
    /// with the list model for the same connection.
    @State private var health: HealthModel?
    @State private var showHealth = false

    // Split into shell → presentations → observers because a single modifier
    // chain of this length makes the type checker give up ("unable to
    // type-check this expression in reasonable time").
    var body: some View {
        observers(presentations(cacheFeeds(shell)))
    }

    /// Publish the session list's project registry to the app-level cache so
    /// the backlog / workstreams / usage / composer screens never have to gate
    /// their first paint on their own `ListProjects` round trip.
    private func cacheFeeds(_ content: some View) -> some View {
        content
        .onChange(of: model?.projects, initial: true) { _, _ in publishProjects() }
        // A first load that returns no projects leaves `projects` unchanged;
        // the loaded flag still makes the (empty) list authoritative.
        .onChange(of: model?.hasLoadedProjects) { _, _ in publishProjects() }
    }

    private func publishProjects() {
        // The initial empty list means "not loaded yet", not "no projects".
        guard let model, model.hasLoadedProjects else { return }
        // Only the connection this list model was built for may feed the cache.
        app.dataCache.updateProjects(model.projects, ifGeneration: modelCacheGeneration)
    }

    private var shell: some View {
        DrawerContainer(isOpen: $drawerOpen, edgeSwipeEnabled: router.path.isEmpty) {
            drawer
        } content: {
            navigation
        }
        .environment(router)
    }

    /// The sheets, dialogs, and alerts hung off the shell.
    private func presentations(_ content: some View) -> some View {
        content
        .confirmationDialog(
            "Open backlog for…",
            isPresented: $showBacklogProjectPicker,
            titleVisibility: .visible
        ) {
            if let model {
                ForEach(newSessionProjectChoices(model), id: \.self) { project in
                    Button(project) {
                        router.open(.backlog(project: project))
                    }
                }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Choose which project's backlog to open.")
        }
        .sheet(item: $newSessionRequest) { request in
            if request.choosingProject, let model {
                // Same request id, so picking a project swaps this sheet's
                // content for the composer in place instead of dismissing
                // and re-presenting.
                NewChatProjectSheet(model: model) { project in
                    newSessionRequest?.project = project
                    newSessionRequest?.choosingProject = false
                }
            } else if let client = app.client {
                // The composer opens in the project the user was last looking
                // at; the chip inside it can still move the session elsewhere.
                NewSessionView(
                    client: client,
                    initialProject: request.project,
                    cache: app.dataCache
                ) { sessionID, project, title in
                    Analytics.submit("new_session")
                    newSessionRequest = nil
                    // Follow the session's project so the list shows it when
                    // the user backs out of the live view.
                    if model?.selectedProject != project {
                        model?.selectedProject = project
                        Task { await model?.refresh() }
                    }
                    router.open(.session(id: sessionID, project: project, live: true, title: title))
                }
                .trackedFlow("new_session")
            }
        }
        .sheet(isPresented: $showAddProject) {
            if let client = app.client {
                AddProjectView(client: client) { project in
                    Analytics.submit("add_project")
                    // Select and refresh so the new project shows up in the
                    // drawer (and every other picker's next load).
                    model?.selectedProject = project.name
                    Task { await model?.refresh() }
                }
                .trackedFlow("add_project")
            }
        }
        .sheet(isPresented: $showHealth) {
            if let health {
                HealthSheet(model: health)
                    .trackedView("health")
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
            "Rename \(projectToRename?.name ?? "project")",
            isPresented: Binding(
                get: { projectToRename != nil },
                set: { if !$0 { projectToRename = nil } }),
            presenting: projectToRename
        ) { project in
            TextField("Project name", text: $renameDraft)
                .autocorrectionDisabled()
                .textInputAutocapitalization(.never)
            Button("Rename") { renameProject(project) }
            Button("Cancel", role: .cancel) { projectToRename = nil }
        } message: { _ in
            Text("Only the registry name changes — the workspace path, sessions, and workstreams follow it.")
        }
        .alert(
            "Couldn’t rename project",
            isPresented: Binding(
                get: { projectRenameError != nil },
                set: { if !$0 { projectRenameError = nil } }),
            presenting: projectRenameError
        ) { _ in
            Button("OK", role: .cancel) { projectRenameError = nil }
        } message: { message in
            Text(message)
        }
        .alert(
            "Couldn’t update follow-up flag",
            isPresented: Binding(
                get: { followUpError != nil },
                set: { if !$0 { followUpError = nil } }),
            presenting: followUpError
        ) { _ in
            Button("OK", role: .cancel) { followUpError = nil }
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
    }

    /// The lifecycle and change observers hung off the shell.
    private func observers(_ content: some View) -> some View {
        content
        .task {
            await ensureLoaded()
            await consumePendingDeepLink()
        }
        // Keep the drawer's cached git badges current while it is visible. Changing
        // drawerOpen cancels this task, so the lightweight poll stops immediately
        // when the drawer closes.
        .task(id: drawerOpen) {
            guard drawerOpen else { return }
            await model?.refreshProjects()
            while !Task.isCancelled {
                do {
                    try await Task<Never, Never>.sleep(nanoseconds: 10_000_000_000)
                } catch {
                    return
                }
                await model?.refreshProjects()
            }
        }
        // Passive triggers are throttled: a refresh that succeeded within the
        // last few seconds is fresh enough (each costs round trips on a
        // remote link). Pull-to-refresh and post-mutation reloads stay forced.
        .onChange(of: scenePhase) { _, phase in
            if phase == .active {
                rowBackgroundEpoch &+= 1
                Task { await model?.refreshIfStale() }
                Task { await health?.refresh() }
            }
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
        .onChange(of: router.path.isEmpty) { _, isAtRoot in
            if isAtRoot {
                rowBackgroundEpoch &+= 1
                Task { await model?.refreshIfStale() }
                Task { await health?.refresh() }
            }
        }
        // Usage analytics: the stack's top screen, with the drawer as an
        // overlay while it is open.
        .onChange(of: router.path, initial: true) { _, path in
            UsageAnalytics.shared.setScreen(path.last?.analyticsView ?? "home")
        }
        .onChange(of: drawerOpen) { _, open in
            if open { UsageAnalytics.shared.present("drawer") } else { UsageAnalytics.shared.dismiss("drawer") }
        }
        .onChange(of: model?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
        // Remember the scope the user actually browses, so New chat from the
        // unscoped feed lands where they were rather than wherever they last
        // happened to start a session.
        .onChange(of: model?.selectedProject) { _, project in
            if let project, !project.isEmpty { lastViewedProject = project }
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
                    Analytics.action("drawer.select_project")
                    // Filtering is client-side over the aggregate load, so this
                    // is instant — no refetch, no spinner.
                    model.selectedProject = project
                    closeDrawer()
                },
                onOpen: { destination in
                    closeDrawer()
                    router.open(destination)
                },
                onAddProject: {
                    closeDrawer()
                    showAddProject = true
                },
                onRenameProject: { project in
                    Analytics.action("drawer.rename_project", via: .contextMenu)
                    closeDrawer()
                    renameDraft = project.name
                    projectToRename = project
                },
                onRemoveProject: { project in
                    Analytics.action("drawer.remove_project", via: .contextMenu)
                    closeDrawer()
                    projectToRemove = project
                },
                onDisconnect: {
                    Analytics.action("drawer.disconnect")
                    closeDrawer()
                    app.disconnect()
                })
        }
    }

    private var navigation: some View {
        @Bindable var router = router
        return NavigationStack(path: $router.path) {
            Group {
                if let model {
                    content(model)
                } else {
                    ProgressView()
                }
            }
            .topBar {
                if let model, model.showsProjectFilter {
                    ScopeChipBar(model: model) { project, position in
                        Analytics.action("scope.select", attrs: ["pos": String(position)])
                        // Client-side filter over the aggregate load: instant.
                        model.selectedProject = project
                    }
                }
            }
            .navigationTitle(model?.selectedProject ?? "Recent")
            // Inline, not large: a large title expanding back on scroll-to-top
            // animates behind the pinned chip bar and the bar jumps down, so the
            // title flickers behind the toolbar buttons. The selected chip
            // already names the scope.
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button {
                        Analytics.action("drawer.open")
                        // The container owns the open/close animation.
                        drawerOpen = true
                    } label: {
                        Label("Workspaces", systemImage: "line.3.horizontal")
                    }
                    .overlay(alignment: .topTrailing) {
                        // Mirror the drawer's loudest badge on the closed menu
                        // button so a question — or unread agent output — in
                        // another project is visible without opening anything.
                        if (model?.totalActivity.needsAnswer ?? 0) > 0 {
                            Circle()
                                .fill(Theme.attention)
                                .frame(width: 8, height: 8)
                                .offset(x: 3, y: -2)
                        } else if (model?.totalActivity.unread ?? 0) > 0 {
                            Circle()
                                .fill(Color.accentColor)
                                .frame(width: 8, height: 8)
                                .offset(x: 3, y: -2)
                        }
                    }
                }
                projectDestinations
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
        // Links inside a file viewer on this stack push the next file here.
        .environment(\.fileNavigator, FileNavigator { router.path.append(.file($0)) })
    }

    /// The project-scoped destinations. Backlog earns its own glyph (it is the
    /// one users reach for constantly) and remains available on the daemon-wide
    /// Recent Sessions feed, where it asks which project's backlog to open. The
    /// rest sit behind an overflow menu, as in the session view. The overflow
    /// itself is not project-scoped: it also carries "Mark all read", which the
    /// unscoped feed needs just as much.
    @ToolbarContentBuilder
    private var projectDestinations: some ToolbarContent {
        ToolbarItem(placement: .topBarTrailing) {
            if let project = model?.selectedProject {
                NavigationLink(value: HomeDestination.backlog(project: project)) {
                    Label("Backlog", systemImage: "checklist")
                }
            } else {
                Button {
                    guard let model else { return }
                    let choices = newSessionProjectChoices(model)
                    if choices.count > 1 {
                        showBacklogProjectPicker = true
                    } else if let project = choices.first {
                        router.open(.backlog(project: project))
                    }
                } label: {
                    Label("Backlog", systemImage: "checklist")
                }
                .disabled(model?.newSessionProjectChoices.isEmpty ?? true)
            }
        }
        ToolbarItem(placement: .topBarTrailing) {
            Menu {
                if let project = model?.selectedProject {
                    NavigationLink(value: HomeDestination.workLoop(project: project)) {
                        Label("Work loop", systemImage: "arrow.triangle.2.circlepath")
                    }
                    NavigationLink(value: HomeDestination.workstreams(project: project)) {
                        Label("Workstreams", systemImage: "arrow.triangle.branch")
                    }
                    NavigationLink(value: HomeDestination.usage(project: project)) {
                        Label("Usage", systemImage: "chart.bar")
                    }
                    NavigationLink(value: HomeDestination.memory(project: project)) {
                        Label("Memory", systemImage: "brain")
                    }
                    NavigationLink(value: HomeDestination.file(FileRoute(
                        project: project, reference: FileReference(path: "", isDirectory: true)))) {
                        Label("Files", systemImage: "folder")
                    }
                }
                if (model?.unreadCount ?? 0) > 0 {
                    if model?.selectedProject != nil { Divider() }
                    Button {
                        Analytics.action("sessions.mark_all_read", via: .menu)
                        model?.markAllRead()
                    } label: {
                        Label("Mark all read", systemImage: "envelope.open")
                    }
                }
            } label: {
                Label("More", systemImage: "ellipsis.circle")
            }
            // Nothing to offer on the unscoped feed with everything read.
            .disabled(model?.selectedProject == nil && (model?.unreadCount ?? 0) == 0)
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
                    live: live, title: title,
                    models: app.dataCache.sessionModels, sessionList: model)
            case let .taskDetail(project, taskID, title):
                TaskDetailView(
                    client: client, project: project,
                    taskID: taskID, taskTitle: title,
                    cache: app.dataCache)
            case let .backlog(project):
                BacklogView(initialProject: project)
            case let .workLoop(project):
                WorkLoopView(project: project)
            case let .workstreams(project):
                WorkstreamsView(initialProject: project)
            case let .usage(project):
                UsageView(initialProject: project)
            case let .memory(project):
                MemoryView(client: client, project: project)
            case let .file(route):
                FileScreen(route: route)
            case .settings:
                GlobalSettingsView(client: client, cache: app.dataCache, health: health)
            }
        }
    }

    private func closeDrawer() {
        drawerOpen = false
    }

    /// Open the composer. A project-scoped list already answers "which project",
    /// so it starts straight away; the unscoped Recent sessions feed does not,
    /// so it *asks* — silently inheriting the last project the user happened to
    /// browse put chats in the wrong workspace with no visible choice. The
    /// chooser preselects the most recently active project, so the common case
    /// is still one tap away.
    private func beginNewSession() {
        guard let model else { return }
        if !model.requiresProjectChoiceForNewSession, let project = model.selectedProject {
            newSessionRequest = NewSessionRequest(project: project)
            return
        }
        let choices = model.newSessionProjectChoices
        if choices.count > 1 {
            newSessionRequest = NewSessionRequest(project: nil, choosingProject: true)
        } else {
            // Sole project (or none registered): nothing to choose between — let
            // the composer resolve it and show its project chip.
            newSessionRequest = NewSessionRequest(project: choices.first)
        }
    }

    /// Registered projects for the unscoped feed's backlog chooser, with the
    /// last project the user viewed hoisted to the top as the likely intent.
    private func newSessionProjectChoices(_ model: SessionListModel) -> [String] {
        let choices = model.newSessionProjectChoices
        guard !lastViewedProject.isEmpty, choices.contains(lastViewedProject) else {
            return choices
        }
        return [lastViewedProject] + choices.filter { $0 != lastViewedProject }
    }

    // MARK: - Session list

    // Flagged sessions are surfaced by the pinned "Follow up" section inside
    // the list, so there is no separate filter chrome above it.
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
                            .buttonStyle(.accent)
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
                    // A broken model login matters most before any session
                    // exists, so the empty screen keeps the health alert too.
                    .overlay(alignment: .top) {
                        if let health, !health.alerting.isEmpty {
                            HealthBanner(alerting: health.alerting) { showHealth = true }
                                .padding(12)
                                .background(
                                    Color(.secondarySystemBackground),
                                    in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                                .padding()
                        }
                    }
            }
        }
        .refreshable { Analytics.action("refresh", via: .pull); await refreshAll(model) }
    }

    /// Pull-to-refresh reloads the sessions and, alongside, the health checks.
    private func refreshAll(_ model: SessionListModel) async {
        Task { await health?.refresh() }
        await model.refresh()
    }

    private func sessionList(_ model: SessionListModel) -> some View {
        List {
            // Its own leading section, so it appearing or clearing is a plain
            // section insert/remove that never touches the session sections.
            if let health, !health.alerting.isEmpty {
                Section {
                    HealthBanner(alerting: health.alerting) { showHealth = true }
                }
            }
            if let warning = model.partialWarning {
                Label(warning, systemImage: "exclamationmark.triangle.fill")
                    .font(.footnote)
                    .foregroundStyle(Theme.attention)
                    .accessibilityLabel("Partial results. \(warning)")
            }
            ForEach(model.sections) { section in
                Section {
                    // Section-scoped row ids: a (un)flag moves a row between
                    // sections, and a bare session id makes List animate that
                    // as a cross-section move (rows below slide up first, then
                    // the old row reappears and fades). Distinct ids make it a
                    // plain delete from one section and insert into the other.
                    ForEach(section.sessions.map { (section.id + "/" + $0.sessionID, $0) }, id: \.0) { _, session in
                        NavigationLink(value: HomeDestination.session(
                            id: session.sessionID,
                            project: model.project(for: session),
                            live: session.live,
                            title: SessionListModel.displayTitle(for: session))
                        ) {
                            SessionRow(
                                session: session,
                                project: model.displayProject(for: session),
                                showsProject: model.selectedProject == nil,
                                isLoopOwned: model.isLoopSession(session),
                                isUnread: model.isUnread(session))
                        }
                        // No row tint for needs-answer: the section header and
                        // the gutter bell already carry the one attention cue.
                        // Running sessions breathe instead (RunningRowBackground).
                        .listRowBackground(SessionListModel.lifecycleLabel(for: session) == "running"
                            ? RunningRowBackground(
                                isFirst: session.sessionID == section.sessions.first?.sessionID,
                                isLast: session.sessionID == section.sessions.last?.sessionID,
                                epoch: rowBackgroundEpoch)
                            : nil)
                        .swipeActions(edge: .leading, allowsFullSwipe: false) {
                            Button {
                                setFollowUp(session, model: model, via: .swipe)
                            } label: {
                                Label(session.followUp ? "Unflag" : "Follow up",
                                    systemImage: session.followUp ? "flag.slash" : "flag")
                            }
                            .tint(Theme.attention)
                            .disabled(model.followUpUpdatingIDs.contains(session.sessionID))
                        }
                        // Acknowledging new agent output without reading it is a
                        // legitimate answer to a badge, so it gets the trailing
                        // (dismissive) edge.
                        .swipeActions(edge: .trailing) {
                            if model.isUnread(session) {
                                Button {
                                    Analytics.action("sessions.mark_read", via: .swipe)
                                    model.markRead(session)
                                } label: {
                                    Label("Mark read", systemImage: "envelope.open")
                                }
                                .tint(.gray)
                            }
                        }
                        .contextMenu {
                            Button {
                                setFollowUp(session, model: model, via: .contextMenu)
                            } label: {
                                Label(session.followUp ? "Remove follow-up flag" : "Flag for follow-up",
                                    systemImage: session.followUp ? "flag.slash" : "flag")
                            }
                            .disabled(model.followUpUpdatingIDs.contains(session.sessionID))
                            if model.isUnread(session) {
                                Button {
                                    Analytics.action("sessions.mark_read", via: .contextMenu)
                                    model.markRead(session)
                                } label: {
                                    Label("Mark read", systemImage: "envelope.open")
                                }
                            }
                        }
                    }
                } header: {
                    if let title = section.title {
                        Label {
                            Text(title.lowercased())
                                .font(.caption.monospaced())
                        } icon: {
                            if section.kind == .needsAnswer {
                                Image(systemName: "bell.badge.fill")
                                    .foregroundStyle(Theme.attention)
                            } else if section.kind == .followUp {
                                Image(systemName: "flag.fill")
                                    .foregroundStyle(Theme.attention)
                            }
                        }
                        .textCase(nil)
                    }
                }
            }
            if model.hasMoreHistory {
                Button {
                    Task { await model.loadMoreHistory() }
                } label: {
                    HStack {
                        Text("Load older sessions")
                        if model.isLoadingMoreHistory { ProgressView() }
                    }
                }
                .disabled(model.isLoadingMoreHistory)
            }
        }
        .listStyle(.insetGrouped)
        // Animate the optimistic (un)flag and any rollback: both land in the
        // same update that adds/removes the id from followUpUpdatingIDs.
        // Unanimated, the row snaps out from under the closing swipe action.
        .animation(.default, value: model.followUpUpdatingIDs)
        .refreshable { Analytics.action("refresh", via: .pull); await refreshAll(model) }
    }

    private func setFollowUp(_ session: Ycc_V1_SessionSummary, model: SessionListModel, via: UsageAnalytics.Via) {
        Analytics.action(session.followUp ? "sessions.unflag" : "sessions.follow_up", via: via)
        Task {
            let updated = await model.setFollowUp(sessionID: session.sessionID, followUp: !session.followUp)
            if !updated, let message = model.followUpErrorMessage {
                followUpError = message
            }
        }
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

    private func renameProject(_ project: Ycc_V1_ProjectInfo) {
        guard let model else { return }
        projectToRename = nil
        let newName = renameDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        // Empty or unchanged: nothing to ask the daemon for.
        guard !newName.isEmpty, newName != project.name else { return }
        Task {
            let renamed = await model.renameProject(named: project.name, to: newName)
            if renamed {
                // Keep the "last viewed" chooser default pointing at the same
                // project under its new name.
                if lastViewedProject == project.name { lastViewedProject = newName }
            } else if !model.unauthorized {
                projectRenameError = model.errorMessage ?? "The project could not be renamed."
            }
        }
    }

    private func ensureLoaded() async {
        if model == nil {
            guard let client = app.client else { return }
            modelCacheGeneration = app.dataCache.generation
            model = SessionListModel(source: client, readMarks: app.readMarks)
            health = HealthModel(source: client)
        }
        // Health is supplementary: never hold the session list up for it.
        Task { await health?.refresh() }
        // A new model has never refreshed, so this always loads it; a
        // re-appearing landing view reuses a fresh list.
        await model?.refreshIfStale()
    }

    // MARK: - Deep links

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
            router.open(.session(
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
                    router.open(.session(
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

/// A pending "new session" presentation: the project chooser first when the
/// Recent feed must ask, then the composer in the chosen project. A `nil`
/// project means "no explicit choice" — the composer falls back to its own
/// remembered project. The id is fixed per request so the chooser → composer
/// step updates the presented sheet rather than replacing it.
private struct NewSessionRequest: Identifiable {
    var project: String?
    var choosingProject = false
    let id = UUID()
}

/// A single session row, laid out like a mail inbox entry so it scans fast:
///
///     ▪  project  0431                           13m
///        Title of the session, wrapping to at
///        most two lines
///        ▪ running · work · via loop
///
/// The leading gutter carries the loudest attention signal (needs-answer bell,
/// else the unread square) without shifting the text column; unread also bolds
/// the title. The header says *where* (project in the unscoped feed, focus
/// tasks) and *when*; the footer is one quiet mono line of routine metadata. A
/// row with nothing to put in the header (a plain chat in a project-scoped list)
/// moves the timestamp onto the title line instead of spending a line on it.
/// The list-row background of a running session: an "on" LED glow with no
/// drawn line. A thin blurred stroke centred on the outline makes the light
/// densest where a border would be; it spreads only a few points outward, so
/// it fades inside the cell instead of cutting off against neighbouring rows.
/// Wider blurred strokes, masked to the inside, carry the glow further in.
/// No fill. Swells and fades on a 3s cycle; TimelineView drives the phase so
/// the pulse survives List cell reuse, and Reduce Motion holds a steady glow.
///
/// The outline follows the cell's own shape: in an inset-grouped list only a
/// section's first row has rounded (top) corners and its last row rounded
/// (bottom) corners, concentric with the section card; other corners are square.
private struct RunningRowBackground: View {
    var isFirst: Bool
    var isLast: Bool
    /// Changing this rebuilds the glow (see `rowBackgroundEpoch`).
    var epoch = 0
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    /// The inset-grouped section card's corner radius (continuous): fitted to
    /// the iOS 27 simulator's rendering against UIKit continuous-corner paths.
    private static let sectionRadius: CGFloat = 26
    /// How far the glow's outline sits inside the cell.
    private static let inset: CGFloat = 6

    var body: some View {
        // The card fill stays outside the TimelineView, so the row keeps its
        // card even if the animated glow fails to draw.
        ZStack {
            Color(.secondarySystemGroupedBackground)
            glow.id(epoch)
        }
        .accessibilityHidden(true)
    }

    private var glow: some View {
        TimelineView(.animation(minimumInterval: 1.0 / 30, paused: reduceMotion)) { context in
            let phase = reduceMotion
                ? 0.6
                : (sin(context.date.timeIntervalSinceReferenceDate * 2 * .pi / 3) + 1) / 2
            let r = Self.sectionRadius - Self.inset
            let outline = UnevenRoundedRectangle(
                cornerRadii: .init(
                    topLeading: isFirst ? r : 0, bottomLeading: isLast ? r : 0,
                    bottomTrailing: isLast ? r : 0, topTrailing: isFirst ? r : 0),
                style: .continuous)
            ZStack {
                ZStack {
                    outline.stroke(Theme.liveGlow, lineWidth: 12)
                        .blur(radius: 12)
                        .opacity(0.04 + 0.5 * phase)
                    outline.stroke(Theme.liveGlow, lineWidth: 5)
                        .blur(radius: 5)
                        .opacity(0.08 + 0.6 * phase)
                }
                .mask(outline)
                outline.stroke(Theme.liveGlow, lineWidth: 2)
                    .blur(radius: 1.5)
                    .opacity(0.18 + 0.72 * phase)
            }
            .padding(Self.inset)
        }
    }
}

private struct SessionRow: View {
    let session: Ycc_V1_SessionSummary
    var project = ""
    var showsProject = false
    var isLoopOwned = false
    var isUnread = false

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            indicator
                .font(.subheadline)
                .imageScale(.small)
                .frame(width: 14)
            VStack(alignment: .leading, spacing: 3) {
                if hasHeaderLead {
                    header
                    title
                } else {
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        title
                        Spacer(minLength: 4)
                        trailingStatus
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                    }
                }
                footer
            }
        }
        .padding(.vertical, 2)
    }

    @ViewBuilder
    private var indicator: some View {
        if session.live && session.waitingInput {
            Image(systemName: "bell.badge.fill")
                .foregroundStyle(Theme.attention)
                .accessibilityLabel("needs answer")
        } else if isUnread {
            Rectangle()
                .fill(Color.accentColor)
                .frame(width: 7, height: 7)
                .accessibilityLabel("unread")
        } else {
            // Zero height so it sits on the baseline without growing the row.
            Color.clear.frame(height: 0)
        }
    }

    private var visibleProject: String? {
        showsProject && !project.isEmpty ? project : nil
    }

    private var taskLabels: [String] {
        SessionListModel.taskChipLabels(for: session)
    }

    private var hasHeaderLead: Bool {
        visibleProject != nil || !taskLabels.isEmpty
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            if let visibleProject {
                Text(visibleProject)
                    .foregroundStyle(.primary.opacity(0.8))
                    .lineLimit(1)
                    .accessibilityLabel("project \(visibleProject)")
            }
            if !taskLabels.isEmpty {
                Text(taskLabels.joined(separator: " "))
                    .lineLimit(1)
                    .layoutPriority(-1)
                    .accessibilityLabel(taskLabels.map(taskAccessibilityLabel).joined(separator: ", "))
            }
            Spacer(minLength: 4)
            trailingStatus
        }
        .font(.caption.monospaced())
        .foregroundStyle(.secondary)
    }

    private var title: some View {
        Text(SessionListModel.displayTitle(for: session))
            .font(isUnread ? .body.weight(.semibold) : .body)
            .foregroundStyle(.primary)
            // Titles are derived from the opening prompt, so one line
            // truncates almost every row into uselessness.
            .lineLimit(2)
    }

    /// The follow-up flag and the compact activity age, right-aligned.
    @ViewBuilder
    private var trailingStatus: some View {
        if session.followUp {
            Image(systemName: "flag.fill")
                .imageScale(.small)
                .foregroundStyle(Theme.attention)
                .accessibilityLabel("flagged for follow-up")
        }
        if let date = SessionListModel.recencyDate(session) {
            Text(SessionListModel.compactAge(of: date))
                .monospacedDigit()
                .lineLimit(1)
                .fixedSize()
                .accessibilityLabel(date.formatted(.relative(presentation: .named, unitsStyle: .wide)))
        }
    }

    @ViewBuilder
    private var footer: some View {
        // The gutter bell already says "waiting"; don't repeat it in words.
        let lifecycle = SessionListModel.lifecycleLabel(for: session)
            .flatMap { $0 == "waiting" ? nil : $0 }
        let items = SessionListModel.metadataItems(for: session, isLoopOwned: isLoopOwned)
        if lifecycle != nil || !items.isEmpty {
            HStack(spacing: 6) {
                if let lifecycle {
                    StatusBadge(label: lifecycle)
                }
                if !items.isEmpty {
                    Text(items.joined(separator: " · "))
                        .lineLimit(1)
                        .accessibilityLabel(items
                            .map { $0 == "via loop" ? "via work loop" : $0 }
                            .joined(separator: ", "))
                }
            }
            .font(.caption2.monospaced())
            .foregroundStyle(.secondary)
        }
    }

    private func taskAccessibilityLabel(_ label: String) -> String {
        if label.hasPrefix("+"), let count = Int(label.dropFirst()) {
            return "\(count) more tasks"
        }
        return "task \(label)"
    }
}

/// A compact lifecycle word. Routine `idle` and unknown legacy values are
/// filtered by SessionListModel before this view is created.
private struct StatusBadge: View {
    let label: String

    var body: some View {
        StatusWord(text: label, color: color, live: label == "running" || label == "background")
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("session status \(label)")
    }

    private var color: Color {
        switch label {
        case "running", "background": return Theme.live
        case "waiting", "paused": return Theme.attention
        case "error": return Theme.danger
        default: return .secondary
        }
    }
}
