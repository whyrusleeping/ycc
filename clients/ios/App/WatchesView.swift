import SwiftUI
import YccKit
import YccProto

/// A project's watches (docs/design/watches.md): daemon-scheduled recurring
/// checks, each listed with its latest status, when it runs next and any
/// question a run is waiting on. Rows drill into ``WatchDetailView``; swipe
/// and context actions pause/resume, run now and delete. Creating and editing
/// stay on the web client and with agents (`schedule_watch`).
struct WatchesView: View {
    @Environment(AppModel.self) private var app

    private let client: YccClient
    private let project: String

    @State private var model: WatchesModel?
    @State private var deleteTarget: String?

    init(client: YccClient, project: String) {
        self.client = client
        self.project = project
    }

    var body: some View {
        Group {
            if let model {
                content(model)
            } else {
                ProgressView()
            }
        }
        .navigationTitle("Watches")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            if model == nil { model = WatchesModel(project: project, source: client) }
        }
        // Refresh on every appearance (including back from a watch), then
        // poll: quickly while a run is active, slowly otherwise.
        .task(id: model?.pollInterval) {
            guard let model else { return }
            while !Task.isCancelled {
                await model.refresh()
                try? await Task.sleep(for: .seconds(model.pollInterval))
            }
        }
        .onChange(of: model?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
        .confirmationDialog(
            "Delete \(deleteTarget ?? "")?",
            isPresented: Binding(get: { deleteTarget != nil }, set: { if !$0 { deleteTarget = nil } }),
            titleVisibility: .visible,
            presenting: deleteTarget
        ) { name in
            Button("Delete", role: .destructive) {
                Analytics.action("watches.delete")
                Task { await model?.delete(name) }
            }
            Button("Cancel", role: .cancel) {}
        } message: { _ in
            Text(WatchDeleteCopy.message)
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
    }

    @ViewBuilder
    private func content(_ model: WatchesModel) -> some View {
        if !model.hasLoaded {
            if let error = model.errorMessage {
                ContentUnavailableView(
                    "Couldn’t load watches",
                    systemImage: "exclamationmark.triangle",
                    description: Text(error))
            } else {
                ProgressView()
            }
        } else if model.watches.isEmpty {
            ContentUnavailableView(
                "No watches",
                systemImage: "eye",
                description: Text("A watch is a recurring check the daemon runs on a schedule. "
                    + "Ask an agent to keep an eye on something, or create one from the web client."))
        } else {
            List {
                if let error = model.errorMessage {
                    Label("Couldn’t refresh: \(error)", systemImage: "exclamationmark.triangle.fill")
                        .font(.footnote)
                        .foregroundStyle(Theme.attention)
                }
                ForEach(model.watches, id: \.definition.name) { watch in
                    let name = WatchFormat.name(watch)
                    NavigationLink(value: HomeDestination.watch(project: project, name: name)) {
                        WatchRow(watch: watch)
                    }
                    .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                        Button(role: .destructive) {
                            deleteTarget = name
                        } label: {
                            Label("Delete", systemImage: "trash")
                        }
                        if !WatchFormat.isExpired(watch) {
                            pauseButton(model, watch, via: .swipe)
                                .tint(Theme.info)
                        }
                    }
                    .contextMenu {
                        Button {
                            Analytics.action("watches.run_now", via: .contextMenu)
                            Task { await model.runNow(name) }
                        } label: {
                            Label("Run now", systemImage: "play")
                        }
                        .disabled(watch.running || model.busyNames.contains(name))
                        if !WatchFormat.isExpired(watch) {
                            pauseButton(model, watch, via: .contextMenu)
                        }
                        Divider()
                        Button(role: .destructive) {
                            deleteTarget = name
                        } label: {
                            Label("Delete…", systemImage: "trash")
                        }
                    }
                }
            }
            .refreshable { Analytics.action("refresh", via: .pull); await model.refresh() }
        }
    }

    private func pauseButton(_ model: WatchesModel, _ watch: Ycc_V1_Watch, via: UsageAnalytics.Via) -> some View {
        let name = WatchFormat.name(watch)
        return Button {
            Analytics.action(watch.paused ? "watches.resume" : "watches.pause", via: via)
            Task { await model.setPaused(name, paused: !watch.paused) }
        } label: {
            if watch.paused {
                Label("Resume", systemImage: "play.circle")
            } else {
                Label("Pause", systemImage: "pause.circle")
            }
        }
        .disabled(model.busyNames.contains(name))
    }
}

/// One watch in the list: name, latest verdict and schedule, the parked
/// question indicator, and the latest summary.
private struct WatchRow: View {
    let watch: Ycc_V1_Watch

    var body: some View {
        let last = WatchFormat.last(watch)
        let schedule = WatchFormat.schedule(watch)
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(WatchFormat.name(watch))
                    .font(.body.monospaced())
                    .lineLimit(1)
                Spacer(minLength: 6)
                StatusWord(text: WatchFormat.reportLabel(last), color: watchToneColor(WatchFormat.reportTone(last)))
            }
            HStack(spacing: 6) {
                StatusWord(text: schedule.label, color: schedule.running ? Theme.live : .secondary, live: schedule.running)
                Text(detailLine(last))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            if !watch.parkedSessionIds.isEmpty {
                Label(WatchFormat.parkedLabel(watch), systemImage: "bell.badge.fill")
                    .font(.caption.weight(.medium))
                    .foregroundStyle(Theme.attention)
            }
            if let last, !last.summary.isEmpty {
                Text(last.summary)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
        }
        .padding(.vertical, 2)
    }

    /// "every 30m · last 5 min. ago · probe"
    private func detailLine(_ last: Ycc_V1_WatchReport?) -> String {
        var parts = ["every \(WatchFormat.interval(watch.definition.intervalSecs))"]
        if let last, let date = WatchFormat.date(last.at) {
            parts.append("last " + date.formatted(.relative(presentation: .named)))
        }
        if !watch.definition.probe.isEmpty { parts.append("probe") }
        return parts.joined(separator: " · ")
    }
}

/// One watch: status and controls, the runs waiting on an answer, its
/// definition, notes, standing decisions and the report timeline (newest
/// first) with links to each run's session.
struct WatchDetailView: View {
    @Environment(AppModel.self) private var app
    @Environment(HomeRouter.self) private var router
    @Environment(\.dismiss) private var dismiss

    private let client: YccClient
    private let project: String
    private let name: String
    /// The home list, to tell whether a run session is still live.
    private let sessionList: SessionListModel?

    @State private var model: WatchDetailModel?
    @State private var confirmDelete = false

    init(client: YccClient, project: String, name: String, sessionList: SessionListModel?) {
        self.client = client
        self.project = project
        self.name = name
        self.sessionList = sessionList
    }

    var body: some View {
        Group {
            if let model {
                content(model)
            } else {
                ProgressView()
            }
        }
        .navigationTitle(name)
        .navigationBarTitleDisplayMode(.inline)
        .task {
            if model == nil { model = WatchDetailModel(project: project, name: name, source: client) }
        }
        .task(id: model?.pollInterval) {
            guard let model else { return }
            while !Task.isCancelled {
                await model.refresh()
                try? await Task.sleep(for: .seconds(model.pollInterval))
            }
        }
        .onChange(of: model?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
        .confirmationDialog("Delete \(name)?", isPresented: $confirmDelete, titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                Analytics.action("watches.delete")
                Task {
                    if await model?.delete() == true { dismiss() }
                }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(WatchDeleteCopy.message)
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
    }

    @ViewBuilder
    private func content(_ model: WatchDetailModel) -> some View {
        if let watch = model.watch, !model.notFound {
            loaded(model, watch)
        } else if model.notFound {
            ContentUnavailableView(
                "Watch not found",
                systemImage: "eye.slash",
                description: Text("It may have been deleted."))
        } else if let error = model.errorMessage {
            ContentUnavailableView(
                "Couldn’t load the watch",
                systemImage: "exclamationmark.triangle",
                description: Text(error))
        } else {
            ProgressView()
        }
    }

    private func loaded(_ model: WatchDetailModel, _ watch: Ycc_V1_Watch) -> some View {
        List {
            // A waiting question is what the phone is for: it leads.
            if !watch.parkedSessionIds.isEmpty {
                Section {
                    ForEach(watch.parkedSessionIds, id: \.self) { id in
                        sessionLink(id, live: true, track: "watches.parked") {
                            Label("Answer in run \(id.prefix(8))", systemImage: "bell.badge.fill")
                                .foregroundStyle(Theme.attention)
                        }
                    }
                } header: {
                    Text("Waiting for you")
                } footer: {
                    Text("These runs asked a question and are parked until you answer; later runs carry on.")
                }
            }
            statusSection(model, watch)
            definitionSection(watch)
            Section("Notes") {
                if watch.notes.isEmpty {
                    Text("No notes yet: each run passes its notes to the next.")
                        .foregroundStyle(.secondary)
                } else {
                    Text(watch.notes)
                        .font(.callout)
                        .textSelection(.enabled)
                }
            }
            if !watch.decisions.isEmpty {
                Section("Standing decisions") {
                    ForEach(Array(watch.decisions.enumerated()), id: \.offset) { _, decision in
                        linkedRow(decision.sessionID, live: false, track: "watches.decision_session") {
                            decisionRow(decision)
                        }
                    }
                }
            }
            reportsSection(model)
        }
        .refreshable { Analytics.action("refresh", via: .pull); await model.refresh() }
    }

    // MARK: - Sections

    @ViewBuilder
    private func statusSection(_ model: WatchDetailModel, _ watch: Ycc_V1_Watch) -> some View {
        let last = WatchFormat.last(watch)
        let schedule = WatchFormat.schedule(watch)
        let expired = WatchFormat.isExpired(watch)
        Section {
            HStack(spacing: 10) {
                StatusWord(text: WatchFormat.reportLabel(last), color: watchToneColor(WatchFormat.reportTone(last)))
                StatusWord(text: schedule.label, color: schedule.running ? Theme.live : .secondary, live: schedule.running)
                Spacer()
                if model.isBusy { ProgressView().controlSize(.small) }
            }
            if let error = model.errorMessage {
                Label("Couldn’t refresh: \(error)", systemImage: "exclamationmark.triangle.fill")
                    .font(.footnote)
                    .foregroundStyle(Theme.attention)
            }
            Button {
                Analytics.action("watches.run_now")
                Task { await model.runNow() }
            } label: {
                Label(watch.running ? "Running…" : "Run now", systemImage: "play")
            }
            .disabled(watch.running || model.isBusy)
            if !expired {
                Button {
                    Analytics.action(watch.paused ? "watches.resume" : "watches.pause")
                    Task { await model.setPaused(!watch.paused) }
                } label: {
                    if watch.paused {
                        Label("Resume", systemImage: "play.circle")
                    } else {
                        Label("Pause", systemImage: "pause.circle")
                    }
                }
                .disabled(model.isBusy)
            }
            Button(role: .destructive) {
                confirmDelete = true
            } label: {
                Label("Delete…", systemImage: "trash")
                    .foregroundStyle(Theme.danger)
            }
            .disabled(model.isBusy)
        } footer: {
            if expired {
                Text("Its expiry passed. Set a later expiry from the web client to resume it.")
            } else if watch.paused {
                Text("Paused by you. Resuming doesn’t replay missed runs.")
            } else if let due = WatchFormat.date(watch.nextDue), !watch.running {
                Text("Next run \(due.formatted(date: .abbreviated, time: .shortened)). Run now doesn’t change the schedule.")
            }
        }
    }

    private func definitionSection(_ watch: Ycc_V1_Watch) -> some View {
        let definition = watch.definition
        return Section("Definition") {
            VStack(alignment: .leading, spacing: 4) {
                Text("Brief").font(.caption).foregroundStyle(.secondary)
                Text(definition.brief)
                    .font(.callout)
                    .textSelection(.enabled)
            }
            LabeledContent("Every", value: WatchFormat.interval(definition.intervalSecs))
            LabeledContent("Model", value: definition.model.isEmpty ? "coordinator default" : definition.model)
            if definition.probe.isEmpty {
                LabeledContent("Probe", value: "none")
            } else {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Probe").font(.caption).foregroundStyle(.secondary)
                    Text(definition.probe)
                        .font(.callout.monospaced())
                        .textSelection(.enabled)
                    Text(definition.probeAlways
                        ? "A model run follows every probe."
                        : "A passing probe (exit 0) records ok without a model run.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            LabeledContent("Expires", value: WatchFormat.date(definition.expires)
                .map { $0.formatted(date: .abbreviated, time: .shortened) } ?? "never")
            if definition.allowedActions.isEmpty {
                LabeledContent("Allowed actions", value: "low-risk only")
            } else {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Allowed actions").font(.caption).foregroundStyle(.secondary)
                    ForEach(Array(definition.allowedActions.enumerated()), id: \.offset) { _, action in
                        Text("• \(action)").font(.callout)
                    }
                }
            }
            if let created = WatchFormat.date(watch.createdAt) {
                LabeledContent("Created", value: created.formatted(.relative(presentation: .named)))
            }
        }
    }

    private func decisionRow(_ decision: Ycc_V1_WatchDecision) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(decision.question)
                .font(.callout.weight(.medium))
            Text("Answer: \(decision.answer)")
                .font(.callout)
                .foregroundStyle(.secondary)
            if let at = WatchFormat.date(decision.at) {
                Text(at.formatted(.relative(presentation: .named)))
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }
        }
        .padding(.vertical, 2)
    }

    @ViewBuilder
    private func reportsSection(_ model: WatchDetailModel) -> some View {
        Section {
            if model.reports.isEmpty {
                Text("No reports yet.").foregroundStyle(.secondary)
            }
            ForEach(Array(model.reports.enumerated()), id: \.offset) { _, report in
                linkedRow(report.sessionID, live: nil, track: "watches.run_session") {
                    reportRow(report)
                }
            }
            if model.mayHaveOlderReports {
                Button {
                    Analytics.action("watches.older")
                    Task { await model.loadOlder() }
                } label: {
                    if model.isLoading {
                        HStack { ProgressView(); Text("Loading…") }
                    } else {
                        Text("Show older reports")
                    }
                }
                .disabled(model.isLoading)
            }
        } header: {
            Text("Reports")
        }
    }

    private func reportRow(_ report: Ycc_V1_WatchReport) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(WatchFormat.date(report.at)
                    .map { $0.formatted(.relative(presentation: .named)) } ?? "—")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                if report.kind == "skipped" {
                    StatusWord(text: "skipped", color: .secondary)
                } else {
                    StatusWord(text: WatchFormat.reportLabel(report), color: watchToneColor(WatchFormat.reportTone(report)))
                }
                let note = WatchFormat.kindNote(report.kind)
                if !note.isEmpty {
                    Text(note).font(.caption).foregroundStyle(.tertiary)
                }
            }
            if !report.summary.isEmpty {
                Text(report.summary)
                    .font(.callout)
            }
            ForEach(Array(report.actions.enumerated()), id: \.offset) { _, action in
                VStack(alignment: .leading, spacing: 1) {
                    Text("• \(action.action)").font(.footnote)
                    if !action.undo.isEmpty {
                        Text("undo: \(action.undo)")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .padding(.leading, 10)
                    }
                }
            }
            if let late = lateNote(report) {
                Text(late).font(.caption).foregroundStyle(.tertiary)
            }
        }
        .padding(.vertical, 2)
    }

    /// "scheduled for …" when the entry landed more than two minutes late.
    private func lateNote(_ report: Ycc_V1_WatchReport) -> String? {
        guard let at = WatchFormat.date(report.at),
              let scheduled = WatchFormat.date(report.scheduledFor),
              at.timeIntervalSince(scheduled) > 120 else { return nil }
        return "scheduled for \(scheduled.formatted(date: .abbreviated, time: .shortened))"
    }

    /// A row that opens its run session when it has one (with a chevron),
    /// else the plain row.
    @ViewBuilder
    private func linkedRow<Row: View>(
        _ sessionID: String, live: Bool?, track: String, @ViewBuilder row: () -> Row
    ) -> some View {
        if sessionID.isEmpty {
            row()
        } else {
            sessionLink(sessionID, live: live, track: track) {
                HStack(spacing: 8) {
                    row()
                        .frame(maxWidth: .infinity, alignment: .leading)
                    Image(systemName: "chevron.right")
                        .font(.footnote.weight(.semibold))
                        // Not `.tertiary`, which a Button tints with the accent.
                        .foregroundStyle(Color(.tertiaryLabel))
                }
            }
            .foregroundStyle(.primary)
            .accessibilityHint("Opens the run session")
        }
    }

    /// A drill-in to a run's session, pushed so Back returns to this watch.
    /// `live` nil asks the home list, defaulting to a persisted load (which
    /// still shows a pending question and re-opens the run to answer it).
    private func sessionLink<Label: View>(
        _ sessionID: String, live: Bool?, track: String, @ViewBuilder label: () -> Label
    ) -> some View {
        Button {
            Analytics.action(track)
            let known = sessionList?.session(sessionID: sessionID)
            router.path.append(.session(
                id: sessionID, project: project,
                live: live ?? known?.live ?? false,
                title: known.map(SessionListModel.displayTitle(for:)) ?? "Watch run · \(name)"))
        } label: {
            label()
        }
    }
}

/// The delete confirmation's explanation, shared by the list and the detail.
private enum WatchDeleteCopy {
    static let message = "This removes the watch, its notes, standing decisions and report history. "
        + "Its run sessions stay in the session history."
}

private func watchToneColor(_ tone: WatchTone) -> Color {
    switch tone {
    case .ok: return Theme.live
    case .warn: return Theme.attention
    case .alert: return Theme.danger
    case .muted: return .secondary
    }
}
