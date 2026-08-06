import SwiftUI
import YccKit
import YccProto

/// Phone control and reconnectable observation for the daemon-owned unattended
/// backlog drain. The daemon keeps running while iOS suspends the app; this view
/// polls only while visible/active and reloads when the app returns to foreground.
struct WorkLoopView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.scenePhase) private var scenePhase

    @State private var model: WorkLoopModel?
    @State private var showStartConfirmation = false
    @State private var showStopConfirmation = false
    @State private var sessionTarget: WorkLoopSessionTarget?

    let project: String

    var body: some View {
        Group {
            if let model {
                content(model)
            } else {
                ProgressView()
            }
        }
        .navigationTitle("Work loop")
        .navigationDestination(item: $sessionTarget) { target in
            if let client = app.client {
                SessionView(
                    client: client,
                    project: project,
                    sessionID: target.sessionID,
                    live: target.live,
                    title: target.title)
            }
        }
        .confirmationDialog(
            "Start unattended work loop?",
            isPresented: $showStartConfirmation,
            titleVisibility: .visible
        ) {
            Button("Start loop") {
                guard let model else { return }
                Task { await model.start() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The daemon will drain ready backlog tasks unattended until none remain or a budget cap trips. This can spend tokens while your phone is locked or suspended.")
        }
        .confirmationDialog(
            "Stop work loop gracefully?",
            isPresented: $showStopConfirmation,
            titleVisibility: .visible
        ) {
            Button("Stop loop", role: .destructive) {
                guard let model else { return }
                Task { await model.stop() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The current session will finish. The daemon will not pick another backlog task.")
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
                    Button("Start loop") { showStartConfirmation = true }
                        .buttonStyle(.borderedProminent)
                        .disabled(!model.canStart)
                }
            } refresh: {
                await model.refresh()
            }
        } else if let loop = model.loop {
            loopList(model, loop)
        }
    }

    private func loopList(_ model: WorkLoopModel, _ loop: Ycc_V1_WorkLoopInfo) -> some View {
        List {
            Section {
                header(model, loop)
            }

            if !model.currentSessionID.isEmpty {
                Section("Current session") {
                    Button {
                        sessionTarget = WorkLoopSessionTarget(
                            sessionID: model.currentSessionID,
                            title: "Loop session",
                            live: true)
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
                        Button {
                            sessionTarget = WorkLoopSessionTarget(
                                sessionID: session.sessionID,
                                title: session.focus.isEmpty ? "Loop session" : session.focus,
                                live: session.sessionID == model.currentSessionID)
                        } label: {
                            workLoopSessionRow(session)
                        }
                        .foregroundStyle(.primary)
                    }
                }
            }

            ForEach(WorkLoopModel.digestSections(for: loop)) { section in
                Section {
                    ForEach(section.rows, id: \.id) { task in
                        digestRow(task, blocked: section.title == "Blocked")
                    }
                } header: {
                    Label(section.title, systemImage: section.systemImage)
                }
            }

            Section {
                if model.state.canStart {
                    Button {
                        showStartConfirmation = true
                    } label: {
                        Label("Start loop", systemImage: "play.circle")
                    }
                    .disabled(!model.canStart)
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
                }
            }
        }
        .refreshable { await model.refresh() }
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
            if model.state == .finished && !loop.outcome.isEmpty {
                Label(loop.outcome, systemImage: "flag.checkered")
                    .font(.subheadline)
            }
            if let error = model.errorMessage {
                Label(error, systemImage: "exclamationmark.triangle.fill")
                    .font(.footnote)
                    .foregroundStyle(.orange)
            }
        }
        .padding(.vertical, 5)
    }

    private func workLoopSessionRow(_ session: Ycc_V1_WorkLoopSession) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(String(session.sessionID.prefix(8)))
                    .font(.headline.monospaced())
                if !session.focus.isEmpty {
                    Text(session.focus).lineLimit(1)
                }
                Spacer()
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.tertiary)
            }
            Text(WorkLoopModel.totalsLine(
                tokens: session.tokens,
                cost: session.cost,
                priceStatus: session.priceStatus))
                .font(.caption.monospacedDigit())
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 2)
    }

    private func digestRow(_ task: Ycc_V1_WorkLoopDigestTask, blocked: Bool) -> some View {
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
            if !task.verdictTally.isEmpty {
                Label(task.verdictTally, systemImage: "checkmark.seal")
            }
            Text(WorkLoopModel.totalsLine(
                tokens: task.tokens,
                cost: task.cost,
                priceStatus: task.priceStatus))
                .monospacedDigit()
            if blocked && !task.reason.isEmpty {
                Label(task.reason, systemImage: "exclamationmark.bubble")
                    .foregroundStyle(.orange)
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
        .refreshable { await refresh() }
    }

    /// Refresh once, then poll only while the last snapshot says the loop is
    /// active. Task cancellation (navigation away) terminates both sleep and loop.
    private func loadAndPoll() async {
        if model == nil {
            guard let client = app.client else { return }
            model = WorkLoopModel(source: client, project: project)
        }
        await model?.refresh()
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

private struct WorkLoopSessionTarget: Identifiable, Hashable {
    let sessionID: String
    let title: String
    let live: Bool
    var id: String { sessionID }
}

private struct WorkLoopStateBadge: View {
    let state: WorkLoopState

    var body: some View {
        Text(state.title)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(color.opacity(0.18), in: Capsule())
            .foregroundStyle(color)
    }

    private var color: Color {
        switch state {
        case .running: return .green
        case .stopping: return .orange
        case .finished: return .blue
        case .none, .unknown: return .gray
        }
    }
}
