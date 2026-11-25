import SwiftUI
import YccKit
import YccProto

/// Phone control and reconnectable observation for the daemon-owned unattended
/// backlog drain. The daemon keeps running while iOS suspends the app; this view
/// polls only while visible/active and reloads when the app returns to foreground.
struct WorkLoopView: View {
    @Environment(AppModel.self) private var app
    @Environment(HomeRouter.self) private var router
    @Environment(\.scenePhase) private var scenePhase

    @State private var model: WorkLoopModel?
    @State private var showStartSheet = false
    @State private var showStopConfirmation = false
    @State private var showAnthropicLogin = false

    let project: String

    var body: some View {
        // Dialogs and alerts are applied in a separate function: as one chain
        // with inline optional-chaining Bindings the type checker gives up.
        withDialogs(
            Group {
                if let model {
                    content(model)
                } else {
                    ProgressView()
                }
            }
            .navigationTitle("Work loop")
        )
        // Restart when an action moves into/out of an active state. This matters
        // when Start is tapped from the empty screen after the initial load task
        // has already returned.
        .task(id: model?.shouldPoll ?? false) { await loadAndPoll() }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active {
                Task { await model?.refresh() }
            }
        }
        .onChange(of: model?.unauthorized ?? false) { _, unauthorized in
            if unauthorized { app.handleUnauthorized() }
        }
    }

    private var actionErrorPresented: Binding<Bool> {
        Binding(
            get: { model?.actionError != nil },
            set: { if !$0 { model?.actionError = nil } })
    }

    private var completionPresented: Binding<Bool> {
        Binding(
            get: { model?.completionMessage != nil },
            set: { if !$0 { model?.completionMessage = nil } })
    }

    /// Start and login sheets and result alerts. The stop confirmation sits
    /// on its button so the iOS 26 popover points at it.
    private func withDialogs(_ content: some View) -> some View {
        content
        .sheet(isPresented: $showStartSheet) {
            if let model {
                WorkLoopStartSheet(model: model) { start(model) }
            }
        }
        .sheet(isPresented: $showAnthropicLogin) {
            if let client = app.client {
                NavigationStack { AnthropicLoginView(client: client) }
                    .trackedView("anthropic_login")
            }
        }
        .alert(
            "Action failed",
            isPresented: actionErrorPresented,
            presenting: model?.actionError
        ) { _ in
            Button("OK", role: .cancel) { model?.actionError = nil }
        } message: { message in
            Text(message)
        }
        .alert(
            "Work loop finished",
            isPresented: completionPresented,
            presenting: model?.completionMessage
        ) { _ in
            Button("OK", role: .cancel) { model?.completionMessage = nil }
        } message: { message in
            Text(message)
        }
    }

    @ViewBuilder
    private func content(_ model: WorkLoopModel) -> some View {
        if model.isLoading && model.loop == nil {
            ProgressView()
        } else if let error = model.errorMessage, model.loop == nil {
            refreshablePlaceholder {
                ContentUnavailableView(
                    "Couldn’t load work loop",
                    systemImage: "exclamationmark.triangle",
                    description: Text(error))
            } refresh: {
                await model.refresh()
            }
        } else if model.loop == nil {
            refreshablePlaceholder {
                ContentUnavailableView {
                    Label("No work loop yet", systemImage: "arrow.triangle.2.circlepath")
                } description: {
                    Text("Work through this project’s ready backlog tasks unattended.")
                } actions: {
                    Button("Start loop…") { showStartSheet = true }
                        .buttonStyle(.accent)
                        .disabled(!model.canStart)
                }
            } refresh: {
                await model.refresh()
            }
        } else if let loop = model.loop {
            loopList(model, loop)
        }
    }

    /// A finished loop whose outcome or last session error mentions Anthropic
    /// most likely stopped on an expired login.
    private func needsAnthropicReconnect(_ model: WorkLoopModel, _ loop: Ycc_V1_WorkLoopInfo) -> Bool {
        guard model.state == .finished else { return false }
        if loop.outcome.localizedCaseInsensitiveContains("anthropic") { return true }
        guard let lastError = loop.sessions.last?.errorMessage else { return false }
        return lastError.localizedCaseInsensitiveContains("anthropic")
    }

    private func loopList(_ model: WorkLoopModel, _ loop: Ycc_V1_WorkLoopInfo) -> some View {
        List {
            Section {
                header(model, loop)
            }

            if needsAnthropicReconnect(model, loop) {
                Section {
                    Button("Reconnect Anthropic") { showAnthropicLogin = true }
                } footer: {
                    Text("Sign in from this phone. Reconnecting does not restart the loop.")
                }
            }

            if !loop.slots.isEmpty {
                Section("Live sessions") {
                    ForEach(Array(loop.slots.enumerated()), id: \.offset) { _, slot in
                        slotRow(slot)
                    }
                }
            } else if !model.currentSessionID.isEmpty {
                Section("Current session") {
                    Button {
                        router.open(.session(
                            id: model.currentSessionID,
                            project: project,
                            live: true,
                            title: "Loop session"))
                    } label: {
                        HStack {
                            Label(String(model.currentSessionID.prefix(8)), systemImage: "dot.radiowaves.left.and.right")
                            Spacer()
                            Image(systemName: "chevron.right")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(.tertiary)
                        }
                    }
                    .foregroundStyle(.primary)
                }
            }

            if !loop.sessions.isEmpty {
                Section("Sessions run") {
                    ForEach(loop.sessions, id: \.sessionID) { session in
                        let title = WorkLoopModel.taskTitle(session.focus, in: loop)
                        VStack(alignment: .leading, spacing: 6) {
                            // Borderless, so the created-task chips and the
                            // report's Show all below stay separately
                            // tappable within the row.
                            Button {
                                router.open(.session(
                                    id: session.sessionID,
                                    project: project,
                                    live: Self.isLive(session.sessionID, in: loop),
                                    title: title.isEmpty ? (session.focus.isEmpty ? "Loop session" : CoordinationFormat.normalizeTaskID(session.focus)) : title))
                            } label: {
                                // A borderless label centres multi-line text.
                                workLoopSessionRow(
                                    session, title: title,
                                    attempt: WorkLoopModel.attemptLabel(for: session, in: loop))
                                    .multilineTextAlignment(.leading)
                                    .contentShape(Rectangle())
                            }
                            .buttonStyle(.borderless)
                            .foregroundStyle(Color.primary)
                            if !session.createdTasks.isEmpty {
                                createdTasksRow(session.createdTasks)
                            }
                            if !session.evidence.isEmpty {
                                ClampedMarkdown(text: session.evidence)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        .padding(.vertical, 2)
                    }
                }
            }

            ForEach(WorkLoopModel.digestSections(for: loop)) { section in
                Section {
                    ForEach(section.rows, id: \.id) { task in
                        digestRow(
                            task,
                            showReason: section.title == "Blocked"
                                || section.title == WorkLoopModel.uncommittedSectionTitle,
                            impliedStatus: Self.impliedStatus(ofSection: section.title))
                    }
                } header: {
                    Label(section.title, systemImage: section.systemImage)
                }
            }

            if !loop.manualLandings.isEmpty {
                Section {
                    ForEach(loop.manualLandings, id: \.sha) { landing in
                        Button {
                            router.open(.commitDiff(project: project, sha: landing.sha))
                        } label: {
                            HStack(alignment: .firstTextBaseline, spacing: 8) {
                                Text(String(landing.sha.prefix(7)))
                                    .font(.caption.monospaced())
                                    .foregroundStyle(.secondary)
                                Text(landing.subject)
                                    .font(.subheadline)
                                    .lineLimit(2)
                                Spacer(minLength: 4)
                                Image(systemName: "chevron.right")
                                    .font(.caption.weight(.semibold))
                                    .foregroundStyle(.tertiary)
                            }
                        }
                        .foregroundStyle(.primary)
                    }
                } header: {
                    Label("Other commits", systemImage: "arrow.triangle.branch")
                } footer: {
                    Text("Commits made during the loop that no loop session recorded.")
                }
            }

            resourceEnvelopeSection(loop)

            if model.state.canStart {
                Section {
                    Button {
                        showStartSheet = true
                    } label: {
                        Label("Start loop…", systemImage: "play.circle")
                    }
                    .disabled(!model.canStart)
                }
            }
        }
        .refreshable { Analytics.action("refresh", via: .pull); await model.refresh() }
        // Stop sits in the navigation bar so it is reachable without
        // scrolling past the live sessions and digest.
        .toolbar {
            if model.state.isActive {
                ToolbarItem(placement: .topBarTrailing) {
                    Button(role: .destructive) {
                        showStopConfirmation = true
                    } label: {
                        Text(model.state == .stopping ? "Stopping…" : "Stop")
                    }
                    .tint(Theme.danger)
                    .disabled(!model.canStop)
                    .accessibilityLabel(model.state == .stopping ? "Stopping loop" : "Stop loop")
                    .workLoopStopConfirmation(
                        isPresented: $showStopConfirmation,
                        message: WorkLoopModel.stopConfirmMessage(for: loop)
                    ) {
                        Analytics.action("loop.stop")
                        Task { await model.stop() }
                    }
                }
            }
        }
    }

    /// The limits the loop started with, after everything live. With no limit
    /// set at all it is one line that expands to the full envelope.
    @ViewBuilder
    private func resourceEnvelopeSection(_ loop: Ycc_V1_WorkLoopInfo) -> some View {
        let lines = WorkLoopModel.resourceEnvelopeLines(for: loop)
        Section("Resource envelope") {
            if let summary = WorkLoopModel.unboundedEnvelopeLine(for: loop) {
                DisclosureGroup {
                    ForEach(lines, id: \.self) { envelopeLine($0) }
                } label: {
                    envelopeLine(summary)
                }
            } else {
                ForEach(lines, id: \.self) { envelopeLine($0) }
            }
        }
    }

    private func envelopeLine(_ line: String) -> some View {
        Text(line)
            .font(.caption)
            .foregroundStyle(.secondary)
    }

    /// The status a digest section's heading already says, so its rows only
    /// show a status that differs from it.
    private static func impliedStatus(ofSection title: String) -> String {
        switch title {
        case "Completed": return "done"
        case "Blocked": return "blocked"
        case "In review", WorkLoopModel.uncommittedSectionTitle: return "in_review"
        default: return ""
        }
    }

    private func start(_ model: WorkLoopModel) {
        Analytics.action("loop.start")
        Task { await model.start() }
    }

    /// Whether a session is one of the loop's live sessions (or, for
    /// snapshots without slots, its current one).
    private static func isLive(_ sessionID: String, in loop: Ycc_V1_WorkLoopInfo) -> Bool {
        loop.slots.contains { $0.sessionID == sessionID }
            || sessionID == WorkLoopModel.currentSessionID(for: loop)
    }

    /// One live loop session: its task, declared intent, working or waiting
    /// and for how long, and its latest activity. The task chip opens the
    /// task; the rest of the row opens the session.
    private func slotRow(_ slot: Ycc_V1_WorkLoopSlot) -> some View {
        TimelineView(.periodic(from: .now, by: 15)) { context in
            VStack(alignment: .leading, spacing: 4) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    if !slot.taskID.isEmpty {
                        taskChip(slot.taskID)
                    }
                    Text(slotHeadline(slot))
                        .font(.subheadline)
                        .foregroundStyle(slot.taskTitle.isEmpty ? .secondary : .primary)
                        .lineLimit(2)
                    Spacer(minLength: 4)
                    Text(WorkLoopModel.slotStateText(slot, now: context.date))
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(slot.state == "waiting" ? Theme.info : Theme.live)
                        .fixedSize()
                }
                if !slot.sessionID.isEmpty {
                    Button {
                        router.open(.session(
                            id: slot.sessionID,
                            project: project,
                            live: true,
                            title: slot.taskTitle.isEmpty ? (slot.taskID.isEmpty ? "Loop session" : slot.taskID) : slot.taskTitle))
                    } label: {
                        VStack(alignment: .leading, spacing: 4) {
                            if !slot.intent.isEmpty {
                                Text(slot.intent)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                                    .lineLimit(3)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                            HStack(spacing: 6) {
                                Text("\(Image(systemName: "dot.radiowaves.left.and.right")) \(String(slot.sessionID.prefix(8)))")
                                    .font(.caption.monospaced())
                                if let last = WorkLoopModel.lastActivityDate(for: slot) {
                                    Text("· active \(last.formatted(.relative(presentation: .named, unitsStyle: .abbreviated)))")
                                        .font(.caption)
                                }
                                Spacer(minLength: 4)
                                Image(systemName: "chevron.right")
                                    .font(.caption.weight(.semibold))
                                    .foregroundStyle(.tertiary)
                            }
                            .foregroundStyle(.secondary)
                        }
                        .multilineTextAlignment(.leading)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.borderless)
                    .foregroundStyle(Color.primary)
                    .accessibilityHint("Opens the session")
                }
            }
            .padding(.vertical, 2)
        }
    }

    /// The task title, or what the slot's session is doing before it has one.
    private func slotHeadline(_ slot: Ycc_V1_WorkLoopSlot) -> String {
        if !slot.taskTitle.isEmpty { return slot.taskTitle }
        if !slot.taskID.isEmpty { return "" }
        return slot.sessionID.isEmpty ? "Starting…" : "Choosing a task…"
    }

    /// The session's created tasks; the chips scroll sideways when they
    /// outgrow the row.
    private func createdTasksRow(_ ids: [String]) -> some View {
        HStack(spacing: 6) {
            Text(ids.count == 1 ? "Created" : "Created \(ids.count)")
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
                .fixedSize()
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    ForEach(ids, id: \.self) { taskChip($0) }
                }
            }
        }
    }

    /// A tappable task id that opens the task's detail.
    private func taskChip(_ taskID: String) -> some View {
        Button {
            router.open(.taskDetail(project: project, taskID: taskID, title: ""))
        } label: {
            Text(taskID)
                .font(.caption.monospaced().weight(.medium))
                .foregroundStyle(Theme.info)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(Theme.info.opacity(0.12), in: Capsule())
        }
        .buttonStyle(.borderless)
        .accessibilityLabel("Task \(taskID)")
    }

    private func header(_ model: WorkLoopModel, _ loop: Ycc_V1_WorkLoopInfo) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                WorkLoopStateBadge(state: model.state)
                Spacer()
                if let started = WorkLoopModel.startedAtDate(for: loop) {
                    Text(started.formatted(Date.RelativeFormatStyle(presentation: .named)))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Text(WorkLoopModel.summaryLine(for: loop))
                .font(.headline)
            Text(WorkLoopModel.totalsLine(for: loop))
                .font(.subheadline.monospacedDigit())
                .foregroundStyle(.secondary)
            if model.state.isActive, !WorkLoopModel.slotsLine(for: loop).isEmpty {
                Label(WorkLoopModel.slotsLine(for: loop), systemImage: "square.stack.3d.up")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
            if model.state == .waiting {
                Label(WorkLoopModel.waitingLine(for: loop), systemImage: "hourglass")
                    .font(.subheadline)
                    .foregroundStyle(Theme.info)
                    .symbolRenderingMode(.monochrome)
            }
            if model.state == .finished && !loop.outcome.isEmpty {
                Label(loop.outcome, systemImage: "flag.checkered")
                    .font(.subheadline)
            }
            if let warning = WorkLoopModel.uncommittedInReviewLine(
                tasks: loop.uncommittedInReview, paths: Int(loop.uncommittedPaths)) {
                Label("At start, \(warning); loop sessions route around that work.", systemImage: "tray.full")
                    .font(.footnote)
                    .foregroundStyle(Theme.attention)
            }
            if let error = model.errorMessage {
                Label(error, systemImage: "exclamationmark.triangle.fill")
                    .font(.footnote)
                    .foregroundStyle(Theme.danger)
            }
        }
        .padding(.vertical, 5)
    }

    /// A finished loop session, led by its task (id and title); the session
    /// id, usage and duration are secondary. The attempt number shows only
    /// for a task the loop attempted more than once.
    private func workLoopSessionRow(_ session: Ycc_V1_WorkLoopSession, title: String, attempt: String?) -> some View {
        let totals = WorkLoopModel.totalsLine(
            tokens: session.tokens,
            cost: session.cost,
            priceStatus: session.priceStatus)
        let usage = WorkLoopModel.durationText(secs: session.durationSecs)
            .map { "\(totals) · \($0)" } ?? totals
        let taskID = CoordinationFormat.normalizeTaskID(session.focus)

        return VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                if taskID.isEmpty {
                    Text("No task")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                } else {
                    Text(taskID)
                        .font(.headline.monospaced())
                    if !title.isEmpty {
                        Text(title)
                            .font(.subheadline)
                            .lineLimit(2)
                    }
                }
                if let attempt {
                    Text(attempt)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize()
                }
                Spacer(minLength: 4)
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.tertiary)
            }
            Text("\(Image(systemName: "dot.radiowaves.left.and.right")) \(String(session.sessionID.prefix(8))) · \(usage)")
                .font(.caption.monospacedDigit())
                .foregroundStyle(.secondary)
            if !session.errorKind.isEmpty {
                let detail = session.errorMessage.isEmpty
                    ? "Failed (\(session.errorKind))"
                    : "Failed (\(session.errorKind)): \(session.errorMessage)"
                Label(detail, systemImage: "exclamationmark.triangle")
                    .font(.caption)
                    .foregroundStyle(Theme.danger)
                    .lineLimit(3)
            }
        }
    }

    private func digestRow(_ task: Ycc_V1_WorkLoopDigestTask, showReason: Bool, impliedStatus: String) -> some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(alignment: .firstTextBaseline) {
                Text(task.id).font(.headline.monospaced())
                Text(task.title).lineLimit(2)
                Spacer(minLength: 4)
                if !task.status.isEmpty, task.status != impliedStatus {
                    TaskStatusPill(status: TaskStatus(status: task.status))
                }
                if !task.sha.isEmpty {
                    Text(String(task.sha.prefix(7)))
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                }
            }
            if task.attempts > 0 {
                Label("\(task.attempts) \(task.attempts == 1 ? "attempt" : "attempts")", systemImage: "arrow.clockwise")
            }
            if !task.verdictTally.isEmpty {
                Label(task.verdictTally, systemImage: "checkmark.seal")
            }
            if let totals = WorkLoopModel.digestTotalsLine(for: task) {
                Text(totals).monospacedDigit()
            }
            if showReason && !task.reason.isEmpty {
                Label(task.reason, systemImage: "exclamationmark.bubble")
                    .foregroundStyle(Theme.attention)
            }
            let details = [
                ("Latest evidence", task.latestEvidence),
                ("Remaining criteria", task.remainingCriteria),
                ("Next step", task.nextStep),
            ].filter { !$0.1.isEmpty }
            if !details.isEmpty {
                DisclosureGroup {
                    ForEach(details, id: \.0) { label, text in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(label)
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(.secondary)
                            MarkdownText(text: text)
                        }
                    }
                } label: {
                    Text("Evidence and next steps")
                        .foregroundStyle(.secondary)
                }
            }
        }
        .font(.caption)
        .padding(.vertical, 2)
    }

    private func refreshablePlaceholder<Content: View>(
        @ViewBuilder content: () -> Content,
        refresh: @escaping () async -> Void
    ) -> some View {
        let placeholder = content()
        return GeometryReader { proxy in
            ScrollView {
                placeholder
                    .frame(width: proxy.size.width, height: proxy.size.height)
            }
        }
        .refreshable { Analytics.action("refresh", via: .pull); await refresh() }
    }

    /// Refresh once, then poll only while the last snapshot says the loop is
    /// active. Task cancellation (navigation away) terminates both sleep and loop.
    private func loadAndPoll() async {
        if model == nil {
            guard let client = app.client else { return }
            model = WorkLoopModel(source: client, project: project, cache: app.dataCache)
        }
        // This task restarts whenever Start/Stop flips `shouldPoll`; their
        // responses already carry the new state, so skip the duplicate load.
        await model?.refreshIfStale()
        while !Task.isCancelled, model?.shouldPoll == true {
            do {
                try await Task.sleep(nanoseconds: 5_000_000_000)
            } catch {
                return
            }
            guard !Task.isCancelled else { return }
            await model?.refresh()
        }
    }
}

extension View {
    /// The stop confirmation. Apply it to the Stop button itself, not the
    /// screen: on iOS 26 a confirmation dialog presents as a popover anchored
    /// to the view it modifies.
    func workLoopStopConfirmation(
        isPresented: Binding<Bool>,
        message: String,
        onStop: @escaping () -> Void
    ) -> some View {
        confirmationDialog(
            "Stop work loop gracefully?",
            isPresented: isPresented,
            titleVisibility: .visible
        ) {
            Button("Stop loop", role: .destructive, action: onStop)
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(message)
        }
    }
}

/// A loop session's report rendered as markdown, as the transcript renders
/// it. A long report is clipped to a few lines until Show all.
private struct ClampedMarkdown: View {
    let text: String
    @State private var expanded = false

    /// The web client's threshold for clamping a loop report.
    private var isLong: Bool {
        text.count > 280 || text.split(separator: "\n", omittingEmptySubsequences: false).count > 4
    }

    var body: some View {
        let clamped = isLong && !expanded
        VStack(alignment: .leading, spacing: 2) {
            MarkdownText(text: text)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxHeight: clamped ? 76 : nil, alignment: .top)
                .clipped()
                .mask {
                    LinearGradient(
                        stops: [.init(color: .black, location: 0.65),
                                .init(color: .black.opacity(clamped ? 0 : 1), location: 1)],
                        startPoint: .top, endPoint: .bottom)
                }
            if isLong {
                Button(expanded ? "Show less" : "Show all") {
                    expanded.toggle()
                }
                .font(.caption.weight(.medium))
                .buttonStyle(.borderless)
                .foregroundStyle(Theme.info)
            }
        }
    }
}

struct WorkLoopStateBadge: View {
    let state: WorkLoopState

    var body: some View {
        StatusWord(text: state.title, color: color, live: state == .running)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(state.title)
    }

    private var color: Color {
        switch state {
        case .running: return Theme.live
        case .waiting: return Theme.info
        case .stopping: return Theme.attention
        case .finished: return .secondary
        case .none, .unknown: return .secondary
        }
    }
}
