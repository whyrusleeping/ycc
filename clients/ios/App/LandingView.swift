import SwiftUI
import YccKit
import YccProto

/// The authenticated home screen: the daemon's session history, most-recent
/// first, with a needs-answer section pinned to the top (docs/design/ios-client
/// .md §6 phase 1 step 2). Live rows stream when opened; persisted rows render
/// the replayed transcript. A mid-session `.unauthorized` failure routes back
/// to the connect screen via ``AppModel/handleUnauthorized()``.
struct LandingView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.scenePhase) private var scenePhase

    @State private var model: SessionListModel?

    var body: some View {
        NavigationStack {
            Group {
                if let model {
                    content(model)
                } else {
                    ProgressView()
                }
            }
            .navigationTitle("Sessions")
            .toolbar {
                if let model, model.showsProjectFilter {
                    ToolbarItem(placement: .topBarLeading) {
                        projectFilter(model)
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Disconnect") { app.disconnect() }
                }
            }
        }
        .task { await ensureLoaded() }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { Task { await model?.refresh() } }
        }
        .onChange(of: model?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized { app.handleUnauthorized() }
        }
    }

    @ViewBuilder
    private func content(_ model: SessionListModel) -> some View {
        if model.isLoading && model.sessions.isEmpty {
            ProgressView()
        } else if let errorMessage = model.errorMessage, model.sessions.isEmpty {
            ContentUnavailableView(
                "Couldn’t load sessions",
                systemImage: "exclamationmark.triangle",
                description: Text(errorMessage))
        } else if model.sessions.isEmpty {
            ContentUnavailableView(
                "No sessions",
                systemImage: "bubble.left.and.bubble.right",
                description: Text("Sessions started on this daemon show up here."))
        } else {
            sessionList(model)
        }
    }

    private func sessionList(_ model: SessionListModel) -> some View {
        List {
            ForEach(model.sections) { section in
                Section {
                    ForEach(section.sessions, id: \.sessionID) { session in
                        NavigationLink {
                            // Defensive: a mid-session disconnect nils the
                            // client; RootView swaps to the connect screen, so
                            // this branch is effectively unreachable — but never
                            // force-unwrap in a lazily-built destination.
                            if let client = app.client {
                                SessionView(
                                    client: client,
                                    project: model.selectedProject,
                                    sessionID: session.sessionID,
                                    live: session.live)
                            }
                        } label: {
                            SessionRow(session: session)
                        }
                        .listRowBackground(section.kind == .needsAnswer
                            ? Color.orange.opacity(0.12) : nil)
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
        .refreshable { await model.refresh() }
    }

    private func projectFilter(_ model: SessionListModel) -> some View {
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
            model = SessionListModel(source: client)
        }
        await model?.refresh()
    }
}

/// A single session row: title, status badge, live marker, needs-answer marker,
/// turns, and a relative last-activity time.
private struct SessionRow: View {
    let session: Ycc_V1_SessionSummary

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
                    .lineLimit(1)
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
                if session.turns > 0 {
                    Label("\(session.turns)", systemImage: "arrow.triangle.2.circlepath")
                        .labelStyle(.titleAndIcon)
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
        case .idle: return .blue
        case .error: return .red
        case .paused: return .orange
        case .stopped: return .gray
        case .unknown: return .gray
        }
    }
}
