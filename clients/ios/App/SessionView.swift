import SwiftUI
import YccKit

/// Read-only session transcript feed — the projection of the event log
/// (docs/design/ios-client.md §6 phase 1 step 3). Live sessions stream via
/// `Subscribe`; persisted sessions load once via `GetSessionTranscript`. The
/// heavy lifting (folding, reconnect) lives in ``SessionViewModel`` / this
/// view is a thin renderer.
///
/// Interactions (input bar, answer sheets, interrupt/resume/stop) are task 0183.
struct SessionView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.scenePhase) private var scenePhase

    @State private var model: SessionViewModel
    /// Whether the feed is scrolled to the newest row (auto-follow enabled).
    @State private var isAtBottom = true

    private let navigationTitle: String
    private static let bottomAnchor = "transcript-bottom"

    init(client: YccClient, project: String = "", sessionID: String, live: Bool) {
        _model = State(initialValue: SessionViewModel(
            source: client, project: project, sessionID: sessionID,
            mode: live ? .live : .persisted))
        self.navigationTitle = live ? "Live session" : "Session"
    }

    var body: some View {
        ScrollViewReader { proxy in
            ZStack(alignment: .bottom) {
                transcript
                if !isAtBottom {
                    jumpToLatestPill(proxy: proxy)
                        .padding(.bottom, 12)
                        .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            .animation(.default, value: isAtBottom)
            .onChange(of: model.rows) { _, _ in
                if isAtBottom {
                    withAnimation { proxy.scrollTo(Self.bottomAnchor, anchor: .bottom) }
                }
            }
        }
        .navigationTitle(navigationTitle)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { statusToolbar }
        .task { model.start() }
        .onDisappear { model.stop() }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { model.reconnect() }
        }
    }

    private var transcript: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 10) {
                if model.rows.isEmpty, model.state == .loading {
                    ProgressView().frame(maxWidth: .infinity).padding(.top, 40)
                }
                ForEach(model.rows) { row in
                    TranscriptRowView(row: row).id(row.id)
                }
                // Zero-height marker at the tail: its visibility drives
                // auto-follow and the "jump to latest" pill.
                Color.clear
                    .frame(height: 1)
                    .id(Self.bottomAnchor)
                    .onAppear { isAtBottom = true }
                    .onDisappear { isAtBottom = false }
            }
            .padding()
        }
    }

    private func jumpToLatestPill(proxy: ScrollViewProxy) -> some View {
        Button {
            withAnimation { proxy.scrollTo(Self.bottomAnchor, anchor: .bottom) }
        } label: {
            Label("Jump to latest", systemImage: "arrow.down")
                .font(.subheadline.weight(.semibold))
                .padding(.horizontal, 14)
                .padding(.vertical, 8)
                .background(.thinMaterial, in: Capsule())
                .overlay(Capsule().strokeBorder(.quaternary))
        }
        .buttonStyle(.plain)
    }

    @ToolbarContentBuilder
    private var statusToolbar: some ToolbarContent {
        ToolbarItem(placement: .topBarTrailing) {
            switch model.state {
            case .streaming:
                Label("Live", systemImage: "dot.radiowaves.left.and.right")
                    .labelStyle(.iconOnly)
                    .foregroundStyle(.green)
            case .reconnecting, .loading:
                ProgressView()
            case .failed:
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
            case .idle, .finished:
                EmptyView()
            }
        }
    }
}

/// Renders a single ``TranscriptRow``. Bubbles for messages; collapsed,
/// tappable disclosure rows for thinking and tool calls; compact system rows.
private struct TranscriptRowView: View {
    let row: TranscriptRow

    var body: some View {
        switch row.kind {
        case .userMessage(let text):
            bubble(text: text, isUser: true)
        case .modelMessage(let text):
            bubble(text: text, isUser: false, actor: row.actor)
        case .thinking(let text):
            ExpandableRow(
                title: "Thinking",
                systemImage: "brain",
                tint: .purple,
                detail: text
            )
        case .tool(let name, let status, let args, let output):
            ToolRowView(name: name, status: status, args: args, output: output)
        case .question(let prompt, let options, let answer):
            QuestionRowView(prompt: prompt, options: options, answer: answer)
        case .system(let text):
            systemRow(text)
        case .liveTail(let text):
            liveTail(text)
        }
    }

    private func bubble(text: String, isUser: Bool, actor: String = "") -> some View {
        HStack {
            if isUser { Spacer(minLength: 40) }
            VStack(alignment: isUser ? .trailing : .leading, spacing: 2) {
                if !isUser, !actor.isEmpty {
                    Text(actor).font(.caption2).foregroundStyle(.secondary)
                }
                Text(text.isEmpty ? " " : text)
                    .textSelection(.enabled)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(
                        isUser ? Color.accentColor.opacity(0.85) : Color(.secondarySystemBackground),
                        in: RoundedRectangle(cornerRadius: 14)
                    )
                    .foregroundStyle(isUser ? .white : .primary)
            }
            if !isUser { Spacer(minLength: 40) }
        }
    }

    private func systemRow(_ text: String) -> some View {
        HStack(spacing: 6) {
            Image(systemName: "circle.fill").font(.system(size: 4)).foregroundStyle(.tertiary)
            Text(text)
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func liveTail(_ text: String) -> some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    ProgressView().scaleEffect(0.6)
                    Text("streaming").font(.caption2).foregroundStyle(.secondary)
                }
                Text(text)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 14))
                    .foregroundStyle(.primary)
            }
            Spacer(minLength: 40)
        }
    }
}

/// A collapsed one-liner that expands to reveal detail text on tap.
private struct ExpandableRow: View {
    let title: String
    let systemImage: String
    var tint: Color = .secondary
    let detail: String

    @State private var expanded = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button {
                withAnimation(.snappy) { expanded.toggle() }
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: systemImage).foregroundStyle(tint)
                    Text(title).font(.caption).foregroundStyle(.secondary)
                    Spacer()
                    Image(systemName: expanded ? "chevron.up" : "chevron.down")
                        .font(.caption2).foregroundStyle(.tertiary)
                }
            }
            .buttonStyle(.plain)

            if expanded {
                Text(detail)
                    .font(.callout)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(10)
                    .background(Color(.tertiarySystemBackground), in: RoundedRectangle(cornerRadius: 10))
            }
        }
    }
}

/// A tool call collapsed to `name + status`, expandable to args and output.
private struct ToolRowView: View {
    let name: String
    let status: TranscriptRow.ToolStatus
    let args: String
    let output: String

    @State private var expanded = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button {
                withAnimation(.snappy) { expanded.toggle() }
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: "wrench.and.screwdriver").foregroundStyle(.blue)
                    Text(name).font(.caption.monospaced()).foregroundStyle(.primary)
                    statusBadge
                    Spacer()
                    Image(systemName: expanded ? "chevron.up" : "chevron.down")
                        .font(.caption2).foregroundStyle(.tertiary)
                }
            }
            .buttonStyle(.plain)

            if expanded {
                VStack(alignment: .leading, spacing: 8) {
                    if !args.isEmpty { detailBlock(label: "args", text: args) }
                    if !output.isEmpty { detailBlock(label: "output", text: output) }
                }
            }
        }
    }

    private var statusBadge: some View {
        Group {
            switch status {
            case .running:
                ProgressView().scaleEffect(0.6)
            case .ok:
                Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
            case .error:
                Image(systemName: "xmark.circle.fill").foregroundStyle(.red)
            }
        }
        .font(.caption2)
    }

    private func detailBlock(label: String, text: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label).font(.caption2).foregroundStyle(.secondary)
            Text(text)
                .font(.caption.monospaced())
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
                .background(Color(.tertiarySystemBackground), in: RoundedRectangle(cornerRadius: 8))
        }
    }
}

/// A pending or resolved `ask_user` question row.
private struct QuestionRowView: View {
    let prompt: String
    let options: [String]
    let answer: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Image(systemName: "questionmark.bubble").foregroundStyle(.orange)
                Text(prompt).font(.callout.weight(.medium))
            }
            if !options.isEmpty {
                ForEach(Array(options.enumerated()), id: \.offset) { _, option in
                    Text("• \(option)").font(.caption).foregroundStyle(.secondary)
                }
            }
            if let answer, !answer.isEmpty {
                Text("Answered: \(answer)")
                    .font(.caption)
                    .foregroundStyle(.green)
            } else {
                Text("Waiting for an answer")
                    .font(.caption2)
                    .foregroundStyle(.orange)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(10)
        .background(Color.orange.opacity(0.08), in: RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Color.orange.opacity(0.3)))
    }
}
