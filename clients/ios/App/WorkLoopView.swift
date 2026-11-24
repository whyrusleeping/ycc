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
    @State private var showStartConfirmation = false
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

    /// Login sheet and result alerts. The start/stop confirmations sit on
    /// their buttons so the iOS 26 popover points at them.
    private func withDialogs(_ content: some View) -> some View {
        content
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
                    Text("Start an unattended drain of this project’s ready backlog tasks.")
                } actions: {
                    VStack(spacing: 14) {
                        parallelStepper(model)
                            .frame(maxWidth: 320)
                        Button("Start loop") { confirmStart(model) }
                            .buttonStyle(.accent)
                            .disabled(!model.canStart)
                            .workLoopStartConfirmation(
                                isPresented: $showStartConfirmation, warning: model.preflightWarning,
                                parallel: model.parallel
                            ) {
                                start(model)
                            }
                    }
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

            Section("Resource envelope") {
                ForEach(WorkLoopModel.resourceEnvelopeLines(for: loop), id: \.self) { line in
                    Text(line)
                        .font(.caption)
                        .foregroundStyle(.secondary)
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
                        VStack(alignment: .leading, spacing: 6) {
                            // Borderless, so the created-task chips below
                            // stay separately tappable within the row.
                            Button {
                                router.open(.session(
                                    id: session.sessionID,
                                    project: project,
                                    live: Self.isLive(session.sessionID, in: loop),
                                    title: session.focus.isEmpty ? "Loop session" : session.focus))
                            } label: {
                                // A borderless label centres multi-line text.
                                workLoopSessionRow(session)
                                    .multilineTextAlignment(.leading)
                                    .contentShape(Rectangle())
                            }
                            .buttonStyle(.borderless)
                            .foregroundStyle(Color.primary)
                            if !session.createdTasks.isEmpty {
                                createdTasksRow(session.createdTasks)
                            }
                        }
                    }
                }
            }

            ForEach(WorkLoopModel.digestSections(for: loop)) { section in
                Section {
                    ForEach(section.rows, id: \.id) { task in
                        digestRow(
                            task,
                            showReason: section.title == "Blocked"
                                || section.title == WorkLoopModel.uncommittedSectionTitle)
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
                    Label("Landed manually", systemImage: "hand.raised")
                } footer: {
                    Text("Commits made during the loop that no loop session recorded.")
                }
            }

            Section {
                if model.state.canStart {
                    parallelStepper(model)
                    Button {
                        confirmStart(model)
                    } label: {
                        Label("Start loop", systemImage: "play.circle")
                    }
                    .disabled(!model.canStart)
                    .workLoopStartConfirmation(
                        isPresented: $showStartConfirmation, warning: model.preflightWarning,
                        parallel: model.parallel
                    ) {
                        start(model)
                    }
                }
                if model.state.isActive {
                    Button(role: .destructive) {
                        showStopConfirmation = true
                    } label: {
                        Label(
                            model.state == .stopping ? "Stopping…" : "Stop loop",
                            systemImage: "stop.circle")
                    }
                    .disabled(!model.canStop)
                    .workLoopStopConfirmation(
                        isPresented: $showStopConfirmation,
                        waiting: model.state == .waiting
                    ) {
                        Analytics.action("loop.stop")
                        Task { await model.stop() }
                    }
                }
            }
        }
        .refreshable { Analytics.action("refresh", via: .pull); await model.refresh() }
    }

    /// Check for uncommitted in-review work, then confirm; the dialog warns
    /// when the preflight found some.
    private func confirmStart(_ model: WorkLoopModel) {
        Task {
            await model.loadPreflight()
            showStartConfirmation = true
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

    /// How many loop sessions the next start runs at once (1...16); the
    /// project's configured count unless changed.
    private func parallelStepper(_ model: WorkLoopModel) -> some View {
        let caption = WorkLoopModel.parallelCaption(parallel: model.parallel, configured: model.configuredParallel)
        return Stepper(
            value: Binding(get: { model.parallel }, set: { model.parallel = $0 }),
            in: 1...WorkLoopModel.maxParallel
        ) {
            VStack(alignment: .leading, spacing: 2) {
                Text(model.parallel == 1 ? "1 session at a time" : "\(model.parallel) sessions at once")
                if !caption.isEmpty {
                    Text(caption)
                        .font(.caption)
                        .foregroundStyle(model.parallel > 1 ? Theme.attention : Color.secondary)
                }
            }
        }
        .disabled(!model.canStart)
    }

    /// One live loop session: its transcript, its claimed task, and whether
    /// it works in a slot or waits on its background jobs.
    private func slotRow(_ slot: Ycc_V1_WorkLoopSlot) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            if slot.sessionID.isEmpty {
                Text("Starting…")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            } else {
                Button {
                    router.open(.session(
                        id: slot.sessionID,
                        project: project,
                        live: true,
                        title: slot.taskID.isEmpty ? "Loop session" : slot.taskID))
                } label: {
                    Label(String(slot.sessionID.prefix(8)), systemImage: "dot.radiowaves.left.and.right")
                        .font(.subheadline.monospaced())
                        .foregroundStyle(Color.primary)
                }
                .buttonStyle(.borderless)
                .accessibilityHint("Opens the session")
            }
            if !slot.taskID.isEmpty {
                taskChip(slot.taskID)
            }
            Spacer(minLength: 4)
            Text(slot.state)
                .font(.caption)
                .foregroundStyle(slot.state == "waiting" ? Theme.info : Theme.live)
        }
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

    private func workLoopSessionRow(_ session: Ycc_V1_WorkLoopSession) -> some View {
        let totals = WorkLoopModel.totalsLine(
            tokens: session.tokens,
            cost: session.cost,
            priceStatus: session.priceStatus)
        let secondary = WorkLoopModel.durationText(secs: session.durationSecs)
            .map { "\(totals) · \($0)" } ?? totals

        return VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(String(session.sessionID.prefix(8)))
                    .font(.headline.monospaced())
                if !session.focus.isEmpty {
                    Text(session.focus).lineLimit(1)
                }
                if session.attempt > 0 {
                    Text("attempt \(session.attempt)")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.tertiary)
            }
            Text(secondary)
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
            if !session.evidence.isEmpty {
                Text(session.evidence)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(4)
            }
        }
        .padding(.vertical, 2)
    }

    private func digestRow(_ task: Ycc_V1_WorkLoopDigestTask, showReason: Bool) -> some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(alignment: .firstTextBaseline) {
                Text(task.id).font(.headline.monospaced())
                Text(task.title).lineLimit(2)
                Spacer(minLength: 4)
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
            Text(WorkLoopModel.totalsLine(
                tokens: task.tokens,
                cost: task.cost,
                priceStatus: task.priceStatus))
                .monospacedDigit()
            if showReason && !task.reason.isEmpty {
                Label(task.reason, systemImage: "exclamationmark.bubble")
                    .foregroundStyle(Theme.attention)
            }
            if !task.latestEvidence.isEmpty {
                Text("Latest evidence: \(task.latestEvidence)")
                    .lineLimit(5)
            }
            if !task.remainingCriteria.isEmpty {
                Text("Remaining criteria: \(task.remainingCriteria)")
                    .foregroundStyle(.secondary)
                    .lineLimit(4)
            }
            if !task.nextStep.isEmpty {
                Text("Next step: \(task.nextStep)")
                    .foregroundStyle(.secondary)
                    .lineLimit(4)
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
        // The slot picker shows the project's configured count.
        if let model, model.state.canStart, model.preflight == nil {
            await model.loadPreflight()
        }
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
    /// The start confirmation. Apply it to the Start button itself, not the
    /// screen: on iOS 26 a confirmation dialog presents as a popover anchored to
    /// the view it modifies, so a screen-level dialog points its arrow at the
    /// middle of the screen.
    /// A `warning` (uncommitted in-review work) leads the message and makes
    /// the action "Start anyway". A `parallel` above 1 is named as experimental.
    /// `onChooseParallel`, for entry points without the slot picker, adds a
    /// button that opens one.
    func workLoopStartConfirmation(
        isPresented: Binding<Bool>,
        warning: String?,
        parallel: Int,
        onChooseParallel: (() -> Void)? = nil,
        onStart: @escaping () -> Void
    ) -> some View {
        confirmationDialog(
            "Start unattended work loop?",
            isPresented: isPresented,
            titleVisibility: .visible
        ) {
            Button(warning == nil ? "Start loop" : "Start anyway", action: onStart)
            if let onChooseParallel {
                Button("Change sessions at once…", action: onChooseParallel)
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            let slots = parallel > 1
                ? " It runs \(parallel) sessions at once in the shared working tree (experimental)."
                : ""
            let base = "The daemon will drain ready backlog tasks unattended until none remain or a budget cap trips.\(slots) This can spend tokens while your phone is locked or suspended."
            if let warning {
                Text("\(warning). Loop sessions leave that work to its owner and route around those files; commit or settle it first, or start anyway.\n\n\(base)")
            } else {
                Text(base)
            }
        }
    }

    /// The stop confirmation; like ``workLoopStartConfirmation(isPresented:warning:parallel:onStart:)``,
    /// apply it to the Stop button.
    func workLoopStopConfirmation(
        isPresented: Binding<Bool>,
        waiting: Bool,
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
            if waiting {
                Text("The loop is waiting for the provider; stopping ends the wait immediately. The daemon will not pick another backlog task.")
            } else {
                Text("The current session will finish. The daemon will not pick another backlog task.")
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
