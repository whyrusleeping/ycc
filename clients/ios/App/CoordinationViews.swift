import SwiftUI
import YccKit
import YccProto

// Session coordination surfaces (spec §7.3): the live sessions view with the
// project notice board, held-task badges, the question sheet's notices and the
// per-project setting. Notice and intent text is untrusted peer content: it
// renders as plain text, with only session ids turned into links.

/// How often a coordination surface re-reads the board while on screen.
let coordinationPollInterval: UInt64 = 20_000_000_000

/// Refresh `model` now and then every ``coordinationPollInterval`` until the
/// calling task is cancelled (the view went away).
@MainActor
func pollCoordination(_ model: CoordinationModel) async {
    while !Task.isCancelled {
        await model.refresh()
        do {
            try await Task.sleep(nanoseconds: coordinationPollInterval)
        } catch {
            return
        }
    }
}

// MARK: - Live sessions

/// A project's live sessions (intent, held task, activity, claimed paths) and
/// its active notices, where the user posts and clears notices. Shown while
/// the project's session coordination is on; off, it points to Settings.
struct LiveSessionsView: View {
    @Environment(AppModel.self) private var app
    @Environment(HomeRouter.self) private var router

    @State private var model: CoordinationModel
    @State private var noticeText = ""
    @State private var subject = ""
    @State private var expiry = CoordinationFormat.defaultExpirySeconds
    @State private var clearTarget: Ycc_V1_ProjectNotice?
    @State private var clearReason = ""
    @FocusState private var focusedField: Field?

    private enum Field { case text, subject }

    private let project: String

    init(client: YccClient, project: String, cache: AppDataCache? = nil) {
        self.project = project
        _model = State(initialValue: CoordinationModel(
            source: client, project: project, cache: cache, reloadsProjects: true))
    }

    var body: some View {
        content
            .navigationTitle("Live sessions")
            .navigationBarTitleDisplayMode(.inline)
            .task { await pollCoordination(model) }
            .onChange(of: model.unauthorized) { _, unauthorized in
                if unauthorized { app.handleUnauthorized() }
            }
            .alert(
                "Clear notice \(clearTarget?.id ?? "")?",
                isPresented: Binding(
                    get: { clearTarget != nil },
                    set: { if !$0 { clearTarget = nil } }),
                presenting: clearTarget
            ) { notice in
                TextField("Reason (optional)", text: $clearReason)
                Button("Clear", role: .destructive) {
                    Analytics.action("coordination.clear_notice", attrs: ["reason": clearReason.isEmpty ? "none" : "given"])
                    let reason = clearReason
                    Task { await model.clear(id: notice.id, reason: reason) }
                }
                Button("Cancel", role: .cancel) {}
            } message: { notice in
                Text("“\(notice.text)”\nOther sessions see the reason with the clear.")
            }
            .alert(
                "Couldn’t update the notice board",
                isPresented: Binding(
                    get: { model.actionError != nil },
                    set: { if !$0 { model.actionError = nil } }),
                presenting: model.actionError
            ) { _ in
                Button("OK", role: .cancel) { model.actionError = nil }
            } message: { message in
                Text(message)
            }
    }

    @ViewBuilder
    private var content: some View {
        switch model.isOn {
        case .none:
            if let error = model.errorMessage {
                ContentUnavailableView(
                    "Couldn’t load live sessions", systemImage: "exclamationmark.triangle",
                    description: Text(error))
            } else {
                ProgressView()
            }
        case .some(false):
            if model.sessions.isEmpty {
                ContentUnavailableView {
                    Label("Session coordination is off", systemImage: "person.2.slash")
                } description: {
                    Text(Self.offText(project))
                } actions: {
                    Button("Open Settings") { router.open(.settings) }
                }
            } else {
                leaseHolders
            }
        case .some(true):
            board
        }
    }

    private var board: some View {
        let now = Date()
        return List {
            if let error = model.errorMessage {
                Section {
                    Label(error, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(Theme.danger)
                        .font(.callout)
                }
            }
            postSection
            Section {
                if model.board == nil {
                    HStack { Spacer(); ProgressView(); Spacer() }
                } else if model.notices.isEmpty {
                    Text("No active notices.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(model.notices, id: \.id) { notice in
                        NoticeRow(notice: notice, now: now)
                            .opacity(model.clearing.contains(notice.id) ? 0.5 : 1)
                            .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                                Button("Clear", role: .destructive) { beginClear(notice) }
                            }
                            .contextMenu {
                                Button(role: .destructive) { beginClear(notice) } label: {
                                    Label("Clear notice…", systemImage: "xmark.circle")
                                }
                            }
                    }
                }
            } header: {
                Text("Notices · \(model.notices.count)")
            } footer: {
                if !model.notices.isEmpty {
                    Text("Swipe a notice to clear it.")
                }
            }
            Section {
                if model.board != nil, model.sessions.isEmpty {
                    Text("No live sessions.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                }
                ForEach(model.sessions, id: \.sessionID) { session in
                    LiveSessionRow(session: session, project: project, now: now) {
                        openSession(session)
                    } openTask: { taskID in
                        router.open(.taskDetail(project: project, taskID: taskID, title: ""))
                    } openPath: { path in
                        router.open(.file(FileRoute(
                            project: project, sessionID: session.sessionID,
                            reference: FileReference(path: path))))
                    }
                }
            } header: {
                Text("Sessions · \(model.sessions.count)")
            } footer: {
                Text("Live sessions of this project; those quiet for over an hour without an intent are left out.")
            }
        }
        .refreshable {
            Analytics.action("refresh", via: .pull)
            await model.refresh()
        }
    }

    private static func offText(_ project: String) -> String {
        "Turn it on for \(project) in Settings › Session coordination to see which live sessions hold which tasks and files, and to post notices to them."
    }

    /// Coordination off: only the sessions holding exclusive leases.
    private var leaseHolders: some View {
        let now = Date()
        return List {
            Section {
                ForEach(model.sessions, id: \.sessionID) { session in
                    LiveSessionRow(session: session, project: project, now: now) {
                        openSession(session)
                    } openTask: { taskID in
                        router.open(.taskDetail(project: project, taskID: taskID, title: ""))
                    } openPath: { path in
                        router.open(.file(FileRoute(
                            project: project, sessionID: session.sessionID,
                            reference: FileReference(path: path))))
                    }
                }
            } header: {
                Text("Leases · \(model.sessions.reduce(0) { $0 + $1.leases.count })")
            } footer: {
                Text("Sessions holding exclusive leases on deploy targets or shared services.")
            }
            Section {
                Button("Open Settings") { router.open(.settings) }
            } header: {
                Text("Session coordination is off")
            } footer: {
                Text(Self.offText(project))
            }
        }
        .refreshable {
            Analytics.action("refresh", via: .pull)
            await model.refresh()
        }
    }

    private var postSection: some View {
        Section {
            TextField(
                "Notice, e.g. “deploying the API; avoid restarts until 15:00”",
                text: $noticeText, axis: .vertical)
                .lineLimit(1...4)
                .focused($focusedField, equals: .text)
                .onChange(of: noticeText) { _, text in
                    if text.count > CoordinationFormat.noticeMax {
                        noticeText = String(text.prefix(CoordinationFormat.noticeMax))
                    }
                }
            TextField("Subject (optional)", text: $subject)
                .textInputAutocapitalization(.never)
                .focused($focusedField, equals: .subject)
                .onChange(of: subject) { _, text in
                    if text.count > CoordinationFormat.subjectMax {
                        subject = String(text.prefix(CoordinationFormat.subjectMax))
                    }
                }
            Picker("Expires after", selection: $expiry) {
                ForEach(CoordinationFormat.expiryChoices, id: \.seconds) { choice in
                    Text(choice.label).tag(choice.seconds)
                }
            }
            Button {
                post()
            } label: {
                HStack {
                    Text(model.isPosting ? "Posting…" : "Post notice")
                    Spacer()
                    if model.isPosting { ProgressView() }
                }
            }
            .disabled(model.isPosting || noticeText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        } header: {
            Text("Post a notice")
        } footer: {
            Text("Sessions see new notices at their next checkpoint and in list_sessions. A notice informs them; it never answers a question or approves anything.")
        }
    }

    private func post() {
        Analytics.action("coordination.post_notice", attrs: ["subject": subject.isEmpty ? "none" : "given"])
        let text = noticeText
        let subject = subject
        let expiry = expiry
        Task {
            if await model.post(text: text, subject: subject, expiresInSeconds: expiry) {
                noticeText = ""
                self.subject = ""
                focusedField = nil
            }
        }
    }

    private func beginClear(_ notice: Ycc_V1_ProjectNotice) {
        clearReason = ""
        clearTarget = notice
    }

    private func openSession(_ session: Ycc_V1_ProjectLiveSession) {
        Analytics.action("coordination.open_session", attrs: ["from": "live"])
        router.open(.session(
            id: session.sessionID, project: project, live: true,
            title: session.title))
    }
}

/// One live session: title and activity, then id, mode, last activity, jobs
/// and worktree, its intent, the tasks it holds (or held earlier), and the
/// paths it has changed but not committed.
private struct LiveSessionRow: View {
    let session: Ycc_V1_ProjectLiveSession
    let project: String
    let now: Date
    let open: () -> Void
    let openTask: (String) -> Void
    let openPath: (String) -> Void

    @State private var allClaims = false
    private static let claimsShown = 6

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button(action: open) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    // Explicit colours: a borderless button tints `.primary`.
                    Text(CoordinationFormat.title(session))
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(Color.primary)
                        .multilineTextAlignment(.leading)
                        .lineLimit(2)
                    Spacer(minLength: 4)
                    ActivityPill(activity: session.activity)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.borderless)
            .accessibilityHint("Opens the session")

            Text(metaLine)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(2)

            if !session.intent.isEmpty {
                VStack(alignment: .leading, spacing: 1) {
                    (Text("Intent  ").font(.caption.weight(.semibold)).foregroundColor(.secondary)
                        + Text(session.intent).font(.caption))
                        .fixedSize(horizontal: false, vertical: true)
                    if !session.intentSetAt.isEmpty {
                        Text("set \(CoordinationFormat.age(session.intentSetAt, now: now))")
                            .font(.caption2)
                            .foregroundStyle(.tertiary)
                    }
                }
            }

            ForEach(session.leases, id: \.name) { lease in
                VStack(alignment: .leading, spacing: 1) {
                    (Text("Lease  ").font(.caption.weight(.semibold)).foregroundColor(.secondary)
                        + Text(lease.name).font(.caption.monospaced())
                        + Text(": \(lease.reason)").font(.caption))
                        .fixedSize(horizontal: false, vertical: true)
                    Text(leaseExpiry(lease))
                        .font(.caption2)
                        .foregroundStyle(.tertiary)
                }
            }

            if !session.focusTasks.isEmpty {
                HStack(spacing: 6) {
                    Text(session.held ? "Holds" : "Earlier focus")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.secondary)
                    ForEach(session.focusTasks, id: \.self) { taskID in
                        Button {
                            openTask(CoordinationFormat.normalizeTaskID(taskID))
                        } label: {
                            Text(taskID)
                                .font(.caption.monospaced().weight(.medium))
                                .foregroundStyle(session.held ? Theme.info : Color.secondary)
                                .padding(.horizontal, 6)
                                .padding(.vertical, 2)
                                .background(
                                    (session.held ? Theme.info : Color.secondary).opacity(0.12),
                                    in: Capsule())
                        }
                        .buttonStyle(.borderless)
                        .accessibilityLabel("Task \(taskID)")
                    }
                }
            }

            if !session.claims.isEmpty {
                VStack(alignment: .leading, spacing: 3) {
                    Text("Claims \(session.claims.count) \(session.claims.count == 1 ? "path" : "paths")")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.secondary)
                    ForEach(shownClaims, id: \.path) { claim in
                        Button {
                            openPath(claim.path)
                        } label: {
                            HStack(alignment: .firstTextBaseline, spacing: 6) {
                                Text(claim.path)
                                    .font(.caption.monospaced())
                                    .foregroundStyle(Color.accentColor)
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                                if !claim.since.isEmpty {
                                    Text(CoordinationFormat.age(claim.since, now: now))
                                        .font(.caption2)
                                        .foregroundStyle(Color(.tertiaryLabel))
                                }
                            }
                        }
                        .buttonStyle(.borderless)
                    }
                    if session.claims.count > Self.claimsShown {
                        Button(allClaims ? "Show fewer" : "Show \(session.claims.count - Self.claimsShown) more") {
                            withAnimation(.snappy) { allClaims.toggle() }
                        }
                        .font(.caption)
                        .buttonStyle(.borderless)
                    }
                }
            }
        }
        .padding(.vertical, 2)
    }

    private var shownClaims: [Ycc_V1_ClaimedPath] {
        allClaims ? session.claims : Array(session.claims.prefix(Self.claimsShown))
    }

    private func leaseExpiry(_ lease: Ycc_V1_SessionLease) -> String {
        var parts: [String] = []
        let expiry = CoordinationFormat.expiresIn(lease.expiresAt, now: now)
        if !expiry.isEmpty { parts.append("expires \(expiry)") }
        if !lease.jobID.isEmpty { parts.append("or when \(lease.jobID) exits") }
        return parts.joined(separator: " · ")
    }

    private var metaLine: String {
        var parts = [CoordinationFormat.shortSessionID(session.sessionID)]
        if !session.mode.isEmpty { parts.append(session.mode) }
        if !session.lastActivity.isEmpty {
            parts.append("active \(CoordinationFormat.age(session.lastActivity, now: now))")
        }
        if session.runningJobs > 0 {
            parts.append(session.runningJobs == 1 ? "1 job running" : "\(session.runningJobs) jobs running")
        }
        if !session.worktree.isEmpty {
            parts.append("worktree \(CoordinationFormat.worktreeName(session.worktree))")
        }
        return parts.joined(separator: " · ")
    }
}

/// A live session's activity as a small tinted word.
private struct ActivityPill: View {
    let activity: String

    var body: some View {
        let badge = CoordinationFormat.activity(activity)
        Text(badge.label)
            .font(.caption2.weight(.medium))
            .foregroundStyle(color(badge.tone))
            .lineLimit(1)
            .fixedSize()
    }

    private func color(_ tone: CoordinationFormat.Tone) -> Color {
        switch tone {
        case .attention: return Theme.attention
        case .running: return Theme.live
        case .background: return Theme.info
        case .danger: return Theme.danger
        case .muted: return .secondary
        }
    }
}

// MARK: - Notices

/// One active notice: subject, text, poster, related tasks, age and expiry.
/// Session ids in the text and poster line link to their session.
struct NoticeRow: View {
    let notice: Ycc_V1_ProjectNotice
    var now = Date()

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                if !notice.subject.isEmpty {
                    Text(notice.subject)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Theme.info)
                        .lineLimit(1)
                }
                Text(notice.id)
                    .font(.caption2.monospaced())
                    .foregroundStyle(.tertiary)
                Spacer(minLength: 4)
                let expires = CoordinationFormat.expiresIn(notice.expiresAt, now: now)
                if !expires.isEmpty {
                    Text(expires == "expired" ? expires : "expires \(expires)")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
            }
            SessionLinkedText(text: notice.text)
                .font(.callout)
            SessionLinkedText(text: posterLine)
                .font(.caption2)
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 1)
    }

    private var posterLine: String {
        var parts: [String] = []
        let poster = CoordinationFormat.poster(notice)
        if notice.sessionID.isEmpty {
            parts.append("posted by you")
        } else {
            parts.append("posted by \(notice.sessionID)" + (poster.isEmpty ? "" : " (\(poster))"))
        }
        if !notice.taskIds.isEmpty {
            parts.append((notice.taskIds.count == 1 ? "task " : "tasks ") + notice.taskIds.joined(separator: ", "))
        }
        let age = CoordinationFormat.age(notice.postedAt, now: now)
        if !age.isEmpty { parts.append(age) }
        return parts.joined(separator: " · ")
    }
}

/// Plain text with daemon session ids turned into `ycc://session/<id>` links,
/// opened in-app through the deep-link router (which resolves the project).
struct SessionLinkedText: View {
    @Environment(AppModel.self) private var app
    let text: String

    var body: some View {
        Text(Self.attributed(text))
            .environment(\.openURL, OpenURLAction { url in
                guard DeepLink(url: url) != nil else { return .systemAction }
                Analytics.action("coordination.open_session", attrs: ["from": "link"])
                app.handleDeepLink(url)
                return .handled
            })
    }

    static func attributed(_ text: String) -> AttributedString {
        var out = AttributedString()
        for part in CoordinationFormat.splitSessionIDs(text) {
            switch part {
            case .text(let plain):
                out += AttributedString(plain)
            case .session(let id):
                var link = AttributedString(id)
                link.link = URL(string: "ycc://session/\(id)")
                out += link
            }
        }
        return out
    }
}

/// The question sheet's notice section: what the project's live sessions
/// were told, which may hold the context the question asks about.
struct QuestionNoticesSection: View {
    let notices: [Ycc_V1_ProjectNotice]

    var body: some View {
        if !notices.isEmpty {
            Section {
                ForEach(notices, id: \.id) { notice in
                    NoticeRow(notice: notice)
                }
            } header: {
                Text(notices.count == 1 ? "Project notice" : "Project notices · \(notices.count)")
            }
        }
    }
}

// MARK: - Held badge

/// Marks a task a live session is working on; taps open that session (a
/// menu picks one when several hold it).
struct HeldBadge: View {
    let holders: [Ycc_V1_ProjectLiveSession]
    let open: (Ycc_V1_ProjectLiveSession) -> Void

    var body: some View {
        if let first = holders.first {
            if holders.count == 1 {
                Button { open(first) } label: { label }
                    .buttonStyle(.borderless)
                    .accessibilityLabel("Held by \(CoordinationFormat.title(first))")
                    .accessibilityHint("Opens the session")
            } else {
                Menu {
                    ForEach(holders, id: \.sessionID) { session in
                        Button(CoordinationFormat.title(session)) { open(session) }
                    }
                } label: { label }
                    .accessibilityLabel("Held by \(holders.count) sessions")
            }
        }
    }

    private var label: some View {
        HStack(spacing: 3) {
            Image(systemName: "person.badge.clock")
            Text(holders.count > 1 ? "Held ×\(holders.count)" : "Held")
        }
        .font(.caption2.weight(.medium))
        .foregroundStyle(Theme.info)
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .background(Theme.info.opacity(0.12), in: Capsule())
        .fixedSize()
    }
}

// MARK: - Settings

/// Per-project session coordination switches (Settings). Each project keeps
/// its own setting in its .ycc/config.toml.
struct CoordinationSettingsView: View {
    @Environment(AppModel.self) private var app
    @State private var model: CoordinationSettingsModel
    /// Called after a project's setting changed, so the home list re-reads it.
    private let onChange: () -> Void

    init(client: YccClient, cache: AppDataCache? = nil, onChange: @escaping () -> Void = {}) {
        _model = State(initialValue: CoordinationSettingsModel(source: client, cache: cache))
        self.onChange = onChange
    }

    var body: some View {
        Form {
            if let message = model.errorMessage {
                Section {
                    Label(message, systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(Theme.danger)
                        .font(.callout)
                }
            }
            Section {
                if model.projects.isEmpty {
                    Text(model.isLoading ? "Loading…" : "No projects yet.")
                        .foregroundStyle(.secondary)
                }
                ForEach(model.projects, id: \.name) { project in
                    Toggle(isOn: Binding(
                        get: { project.sessionCoordination },
                        set: { on in
                            Analytics.action("settings.session_coordination", attrs: ["on": on ? "true" : "false"])
                            Task {
                                await model.set(on, for: project.name)
                                onChange()
                            }
                        }
                    )) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(project.name)
                            Text(project.path)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                                .truncationMode(.middle)
                        }
                    }
                    .disabled(model.applying.contains(project.name))
                }
            } header: {
                Text("Projects")
            } footer: {
                Text("Experimental. Session coordination lets agents list the project’s other sessions and the files each has changed but not committed, and tells two sessions when they edit the same file. Turning it on applies to sessions started or reopened afterwards; turning it off takes effect at once.")
            }
        }
        .navigationTitle("Session coordination")
        .navigationBarTitleDisplayMode(.inline)
        .task { await model.load() }
        .refreshable { Analytics.action("refresh", via: .pull); await model.load() }
        .onChange(of: model.unauthorized) { _, unauthorized in
            if unauthorized { app.handleUnauthorized() }
        }
    }
}
