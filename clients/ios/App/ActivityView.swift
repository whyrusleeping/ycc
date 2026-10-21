import SwiftUI
import YccKit
import YccProto

/// The session Activity sheet: the coordinator → subagent → background-job
/// graph as a collapsible tree or a compact timeline, with drill-down into an
/// agent's filtered transcript or a job's live output. Presented from the
/// session screen's action menu; polls `GetSessionActivity` while on screen.
struct SessionActivitySheet: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    let client: YccClient
    let project: String
    let sessionID: String
    /// The presenting screen's transcript model, filtered per agent on drill-down.
    let session: SessionViewModel

    @State private var model: SessionActivityModel?
    @State private var layout: Layout = .tree
    /// Bumped by Retry to restart the one view-owned polling task (cancelling
    /// the previous one) instead of racing it with an unmanaged refresh.
    @State private var pollGeneration = 0

    private enum Layout: String, CaseIterable { case tree = "Tree", timeline = "Timeline" }

    var body: some View {
        NavigationStack {
            Group {
                if let model {
                    content(model)
                } else {
                    ProgressView()
                }
            }
            .navigationTitle("Activity")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .navigationDestination(for: String.self) { nodeID in
                if let model {
                    ActivityNodeDetail(
                        client: client, project: project, sessionID: sessionID,
                        session: session, model: model, nodeID: nodeID)
                }
            }
        }
        .task(id: pollGeneration) {
            if model == nil {
                model = SessionActivityModel(source: client, project: project, sessionID: sessionID)
            }
            await model?.poll()
        }
        .onChange(of: model?.unauthorized ?? false) { _, isUnauthorized in
            if isUnauthorized {
                dismiss()
                app.handleUnauthorized()
            }
        }
    }

    @ViewBuilder
    private func content(_ model: SessionActivityModel) -> some View {
        VStack(spacing: 0) {
            Picker("Layout", selection: $layout) {
                ForEach(Layout.allCases, id: \.self) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)
            .padding(.horizontal)
            .padding(.vertical, 8)
            if !model.hasLoaded {
                Spacer()
                ProgressView()
                Spacer()
            } else if let error = model.errorMessage, model.nodes.isEmpty {
                ContentUnavailableView {
                    Label("Activity unavailable", systemImage: "exclamationmark.triangle")
                } description: {
                    Text(error)
                } actions: {
                    Button("Retry") { pollGeneration += 1 }
                }
            } else if model.nodes.isEmpty {
                ContentUnavailableView("No activity yet", systemImage: "point.3.connected.trianglepath.dotted",
                                       description: Text("Agents and background jobs appear here as the session spawns them."))
            } else {
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    switch layout {
                    case .tree:
                        ActivityTreeList(model: model, now: context.date)
                    case .timeline:
                        ActivityTimelineChart(nodes: model.nodes, now: context.date)
                    }
                }
            }
        }
    }
}

// MARK: - Shared row pieces

private enum ActivityStyle {
    static func color(_ status: ActivityStatus) -> Color {
        switch status {
        case .running: return .blue
        case .done: return .green
        case .failed, .error: return .red
        case .blocked, .paused: return .orange
        case .idle: return .teal
        case .killed, .stopped, .lost, .other: return .gray
        }
    }

    static func kindLabel(_ node: Ycc_V1_ActivityNode) -> String {
        switch node.kind {
        case "coordinator": return "coordinator"
        case "generic": return "agent"
        case "bash": return "shell job"
        default: return node.kind
        }
    }

    static func icon(_ node: Ycc_V1_ActivityNode) -> String {
        switch node.kind {
        case "coordinator": return "person.crop.circle"
        case "bash": return "terminal"
        case "reviewer": return "checkmark.seal"
        default: return "person.2"
        }
    }
}

/// A status dot; pulses while the status is running.
private struct StatusDot: View {
    let status: ActivityStatus

    var body: some View {
        Image(systemName: "circle.fill")
            .font(.system(size: 10))
            .foregroundStyle(ActivityStyle.color(status))
            .symbolEffect(.pulse, options: .repeating, isActive: status.isActive)
            .accessibilityLabel(status.label)
    }
}

// MARK: - Tree

private struct ActivityTreeList: View {
    let model: SessionActivityModel
    let now: Date

    var body: some View {
        List {
            ForEach(model.rows) { row in
                ActivityTreeRowView(
                    row: row, now: now,
                    isCollapsed: model.collapsed.contains(row.id),
                    toggle: { model.toggleCollapsed(row.id) })
            }
            if let error = model.errorMessage {
                Label(error, systemImage: "exclamationmark.triangle")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
            if !model.live {
                Text("Session is not live; this is the recorded activity.")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
        }
        .listStyle(.plain)
    }
}

private struct ActivityTreeRowView: View {
    let row: ActivityTreeRow
    let now: Date
    let isCollapsed: Bool
    let toggle: () -> Void

    private var node: Ycc_V1_ActivityNode { row.node }
    private var status: ActivityStatus { ActivityStatus(node.status) }

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            // Tree guides: one thin line per ancestor level.
            ForEach(0..<row.depth, id: \.self) { _ in
                Rectangle()
                    .fill(Color.secondary.opacity(0.25))
                    .frame(width: 1)
                    .padding(.leading, 5)
                    .padding(.trailing, 6)
            }
            Button(action: toggle) {
                Image(systemName: "chevron.right")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                    .rotationEffect(.degrees(isCollapsed ? 0 : 90))
                    .frame(width: 16, height: 20)
                    .opacity(row.hasChildren ? 1 : 0)
            }
            .buttonStyle(.plain)
            .disabled(!row.hasChildren)
            .accessibilityLabel(isCollapsed ? "Expand" : "Collapse")
            NavigationLink(value: node.id) {
                HStack(alignment: .top, spacing: 8) {
                    StatusDot(status: status)
                        .padding(.top, 5)
                    VStack(alignment: .leading, spacing: 2) {
                        HStack(alignment: .firstTextBaseline) {
                            Text(ActivityGraph.displayName(node))
                                .font(node.kind == "bash" ? .footnote.monospaced() : .body)
                                .lineLimit(node.kind == "bash" ? 2 : 1)
                            Spacer(minLength: 8)
                            Text(ActivityGraph.elapsed(start: node.startedAt, end: node.finishedAt, status: status, now: now))
                                .font(.caption.monospacedDigit())
                                .foregroundStyle(.secondary)
                        }
                        Text(subtitle)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                        if status.isActive, node.kind != "bash" {
                            progressLine
                        }
                    }
                }
            }
        }
        .listRowInsets(EdgeInsets(top: 8, leading: 12, bottom: 8, trailing: 12))
        .animation(.easeInOut(duration: 0.15), value: isCollapsed)
    }

    private var subtitle: String {
        var parts = [ActivityStyle.kindLabel(node)]
        if !node.model.isEmpty { parts.append(node.model) }
        parts.append(status.label)
        if node.spans.count > 1 { parts.append("\(node.spans.count) rounds") }
        if node.mutates { parts.append("mutating") }
        return parts.joined(separator: " · ")
    }

    @ViewBuilder
    private var progressLine: some View {
        HStack(spacing: 6) {
            if !node.currentTool.isEmpty {
                Label(node.currentTool, systemImage: "wrench.and.screwdriver")
                    .lineLimit(1)
            } else {
                Label("thinking", systemImage: "ellipsis.bubble")
            }
            if node.turns > 0 || node.totalTokens > 0 {
                Text("·")
                Text("\(node.turns) turns, \(ActivityGraph.formatTokens(node.totalTokens)) tok")
                    .monospacedDigit()
            }
        }
        .font(.caption)
        .foregroundStyle(.blue)
    }
}

// MARK: - Timeline

private struct ActivityTimelineChart: View {
    let nodes: [Ycc_V1_ActivityNode]
    let now: Date

    private static let labelWidth: CGFloat = 104
    private static let laneHeight: CGFloat = 30
    private static let axisHeight: CGFloat = 22
    private static let minBarWidth: CGFloat = 4

    var body: some View {
        if let timeline = ActivityGraph.timeline(nodes: nodes, now: now) {
            GeometryReader { geo in
                let available = max(120, geo.size.width - Self.labelWidth - 24)
                // Fit to width; long sessions get ~2 pt/s and scroll sideways.
                let trackWidth = min(max(available, CGFloat(timeline.duration) * 2), 4000)
                ScrollView(.vertical) {
                    HStack(alignment: .top, spacing: 0) {
                        labels(timeline)
                            .frame(width: Self.labelWidth, alignment: .leading)
                        ScrollView(.horizontal, showsIndicators: true) {
                            tracks(timeline, width: trackWidth)
                                .padding(.trailing, 12)
                        }
                    }
                    .padding(.vertical, 8)
                    legend
                        .padding(.horizontal)
                        .padding(.bottom, 12)
                }
            }
        } else {
            ContentUnavailableView("Nothing started yet", systemImage: "clock")
        }
    }

    private func labels(_ timeline: ActivityTimeline) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("").frame(height: Self.axisHeight)
            ForEach(timeline.lanes) { lane in
                NavigationLink(value: lane.id) {
                    HStack(spacing: 4) {
                        let status = lane.bars.last?.status ?? .other("")
                        StatusDot(status: status)
                        Text(lane.label)
                            .font(.caption)
                            .lineLimit(1)
                            .truncationMode(.tail)
                    }
                }
                .buttonStyle(.plain)
                .padding(.leading, 12 + CGFloat(lane.depth) * 10)
                .frame(height: Self.laneHeight, alignment: .leading)
            }
        }
    }

    private func tracks(_ timeline: ActivityTimeline, width: CGFloat) -> some View {
        let scale = width / CGFloat(timeline.duration)
        return VStack(alignment: .leading, spacing: 0) {
            axis(timeline, width: width, scale: scale)
            ForEach(timeline.lanes) { lane in
                ZStack(alignment: .leading) {
                    Rectangle()
                        .fill(Color.secondary.opacity(0.08))
                        .frame(width: width, height: 1)
                    ForEach(Array(lane.bars.enumerated()), id: \.offset) { _, bar in
                        RoundedRectangle(cornerRadius: 3)
                            .fill(ActivityStyle.color(bar.status).opacity(bar.status.isActive ? 0.9 : 0.7))
                            .frame(width: max(Self.minBarWidth, CGFloat(bar.end - bar.start) * scale), height: 14)
                            .offset(x: CGFloat(bar.start) * scale)
                            .accessibilityLabel("\(lane.label) \(bar.label) \(bar.status.label)")
                    }
                }
                .frame(width: width, height: Self.laneHeight, alignment: .leading)
            }
        }
        .overlay(alignment: .topLeading) {
            if ActivityGraph.hasRunningWork(nodes) {
                Rectangle()
                    .fill(Color.blue.opacity(0.5))
                    .frame(width: 1)
                    .offset(x: min(width, CGFloat(now.timeIntervalSince(timeline.origin)) * scale))
            }
        }
    }

    private func axis(_ timeline: ActivityTimeline, width: CGFloat, scale: CGFloat) -> some View {
        let step = Self.tickStep(duration: timeline.duration, width: width)
        let ticks = stride(from: 0.0, through: timeline.duration, by: step).map { $0 }
        return ZStack(alignment: .bottomLeading) {
            ForEach(ticks, id: \.self) { tick in
                VStack(alignment: .leading, spacing: 2) {
                    Text(ActivityGraph.formatDuration(tick))
                        .font(.caption2.monospacedDigit())
                        .foregroundStyle(.secondary)
                    Rectangle().fill(Color.secondary.opacity(0.4)).frame(width: 1, height: 4)
                }
                .offset(x: CGFloat(tick) * scale)
            }
        }
        .frame(width: width, height: Self.axisHeight, alignment: .bottomLeading)
    }

    /// A tick spacing in seconds that leaves ~70 pt between labels.
    private static func tickStep(duration: TimeInterval, width: CGFloat) -> TimeInterval {
        let target = duration / Double(max(1, Int(width / 70)))
        for step in [1.0, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200] where step >= target {
            return step
        }
        return 14_400
    }

    private var legend: some View {
        HStack(spacing: 10) {
            ForEach([ActivityStatus.running, .done, .failed, .blocked, .lost], id: \.label) { status in
                HStack(spacing: 3) {
                    RoundedRectangle(cornerRadius: 2).fill(ActivityStyle.color(status)).frame(width: 10, height: 8)
                    Text(status.label)
                }
            }
        }
        .font(.caption2)
        .foregroundStyle(.secondary)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

// MARK: - Drill-down

private struct ActivityNodeDetail: View {
    let client: YccClient
    let project: String
    let sessionID: String
    let session: SessionViewModel
    let model: SessionActivityModel
    let nodeID: String

    var body: some View {
        if let node = model.node(id: nodeID) {
            if node.kind == "bash" {
                JobOutputView(client: client, project: project, sessionID: sessionID, node: node)
            } else {
                AgentDetailView(session: session, node: node)
            }
        } else {
            ContentUnavailableView("Node gone", systemImage: "questionmark.circle")
        }
    }
}

/// An agent's rounds, report, and its slice of the session transcript.
private struct AgentDetailView: View {
    let session: SessionViewModel
    let node: Ycc_V1_ActivityNode

    private var status: ActivityStatus { ActivityStatus(node.status) }
    private var actor: String {
        if node.kind == "coordinator" { return "coordinator" }
        return node.actor.isEmpty ? node.id : node.actor
    }
    private var rows: [TranscriptRow] {
        session.durableRows.filter { $0.actor == actor } + session.liveTails.filter { $0.actor == actor }
    }

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { context in
            List {
                Section {
                    LabeledContent("Status") {
                        HStack(spacing: 6) {
                            StatusDot(status: status)
                            Text(status.label)
                        }
                    }
                    if !node.model.isEmpty { LabeledContent("Model", value: node.model) }
                    let elapsed = ActivityGraph.elapsed(start: node.startedAt, end: node.finishedAt, status: status, now: context.date)
                    if !elapsed.isEmpty { LabeledContent("Elapsed", value: elapsed) }
                    if status.isActive {
                        LabeledContent("Current tool", value: node.currentTool.isEmpty ? "—" : node.currentTool)
                        LabeledContent("Progress", value: "\(node.turns) turns · \(ActivityGraph.formatTokens(node.totalTokens)) tokens")
                    }
                    if node.mutates { LabeledContent("Mutates", value: "yes") }
                } header: {
                    Text(ActivityStyle.kindLabel(node))
                }
                if !node.spans.isEmpty {
                    Section("Rounds") {
                        ForEach(node.spans, id: \.round) { span in
                            let spanStatus = ActivityStatus(span.status)
                            HStack(spacing: 8) {
                                StatusDot(status: spanStatus)
                                Text("Round \(span.round)")
                                if !span.contextMode.isEmpty {
                                    Text(span.contextMode).font(.caption).foregroundStyle(.secondary)
                                }
                                Spacer()
                                Text(spanStatus.label).font(.caption).foregroundStyle(.secondary)
                                Text(ActivityGraph.elapsed(start: span.startedAt, end: span.finishedAt, status: spanStatus, now: context.date))
                                    .font(.caption.monospacedDigit())
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                }
                if !node.result.isEmpty {
                    Section(node.kind == "coordinator" ? "Result" : "Report") {
                        MarkdownText(text: node.result)
                            .font(.callout)
                    }
                }
                Section {
                    if session.earlierRowCount > 0 {
                        Button {
                            Analytics.action("transcript.load_earlier")
                            session.loadEarlierRows()
                        } label: {
                            Label("Load earlier", systemImage: "arrow.up")
                                .frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.bordered)
                        .listRowSeparator(.hidden)
                    }
                    if rows.isEmpty {
                        Text(session.earlierRowCount > 0
                             ? "No rows for this agent in the loaded part of the transcript."
                             : "No transcript rows for this agent yet.")
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                    }
                    ForEach(Array(zip(rows.indices, rows)), id: \.1.id) { index, row in
                        TranscriptRowView(
                            row: row,
                            showsActorHeading: index == rows.startIndex,
                            loadDetail: { rowID in await session.loadDetail(rowID: rowID) })
                        .listRowSeparator(.hidden)
                    }
                } header: {
                    Text("Transcript")
                }
            }
            .listStyle(.insetGrouped)
        }
        .navigationTitle(ActivityGraph.displayName(node))
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// A shell job's output, followed live while the job runs.
private struct JobOutputView: View {
    let client: YccClient
    let project: String
    let sessionID: String
    let node: Ycc_V1_ActivityNode

    @State private var output: JobOutputModel?
    @State private var showsCommand = false

    /// The polled activity node is the source of truth; the follower's status
    /// only fills in between polls once the job has stopped.
    private var status: ActivityStatus {
        let polled = ActivityStatus(node.status)
        if polled.isActive, let followed = output?.follower.status, !followed.isEmpty,
           !ActivityStatus(followed).isActive {
            return ActivityStatus(followed)
        }
        return polled
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            if let output {
                if !output.hasLoaded {
                    Spacer()
                    ProgressView()
                    Spacer()
                } else if output.available {
                    notices(output)
                    MonospaceLog(text: output.text, follow: output.isRunning)
                } else {
                    fallback(reason: output.unavailableReason)
                }
            } else {
                Spacer()
            }
        }
        .navigationTitle(node.id)
        .navigationBarTitleDisplayMode(.inline)
        .task {
            if output == nil {
                output = JobOutputModel(source: client, project: project, sessionID: sessionID, jobID: node.id)
            }
            await output?.follow()
        }
    }

    private var header: some View {
        TimelineView(.periodic(from: .now, by: 1)) { context in
            VStack(alignment: .leading, spacing: 6) {
                Button { showsCommand.toggle() } label: {
                    Text(node.label)
                        .font(.footnote.monospaced())
                        .lineLimit(showsCommand ? nil : 2)
                        .multilineTextAlignment(.leading)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .buttonStyle(.plain)
                HStack(spacing: 6) {
                    StatusDot(status: status)
                    Text(status.label)
                    if !node.model.isEmpty { Text("· \(node.model)") }
                    Spacer()
                    Text(ActivityGraph.elapsed(start: node.startedAt, end: node.finishedAt, status: status, now: context.date))
                        .monospacedDigit()
                }
                .font(.caption)
                .foregroundStyle(.secondary)
            }
            .padding(.horizontal)
            .padding(.vertical, 10)
        }
    }

    @ViewBuilder
    private func notices(_ output: JobOutputModel) -> some View {
        if output.follower.droppedEarlier {
            notice("Older output was dropped from the daemon's buffer.", systemImage: "exclamationmark.triangle")
        } else if output.follower.trimmedHistory {
            notice("Older output was trimmed locally; showing the newest \(output.follower.maxBytes / 1024) KiB.",
                   systemImage: "arrow.up.to.line")
        } else if output.follower.omittedHead {
            notice("Showing the newest output; earlier lines are not loaded.", systemImage: "arrow.up.to.line")
        }
        if let error = output.errorMessage {
            notice("\(error) — retrying…", systemImage: "wifi.exclamationmark")
        }
    }

    private func notice(_ text: String, systemImage: String) -> some View {
        Label(text, systemImage: systemImage)
            .font(.caption)
            .foregroundStyle(.secondary)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal)
            .padding(.vertical, 6)
            .background(Color(.secondarySystemBackground))
    }

    @ViewBuilder
    private func fallback(reason: String) -> some View {
        notice(reason.isEmpty ? "Live output is not available; showing the recorded tail." : reason,
               systemImage: "info.circle")
        if node.result.isEmpty {
            ContentUnavailableView("No output recorded", systemImage: "terminal")
        } else {
            MonospaceLog(text: node.result, follow: false)
        }
    }
}

/// Selectable monospace text (soft-wrapped for the phone) that sticks to the
/// bottom while following. One `Text` stops drawing past a few thousand
/// lines, so the log is rendered as lazily laid-out chunks; selection works
/// within a chunk.
private struct MonospaceLog: View {
    let text: String
    let follow: Bool

    private static let linesPerChunk = 100

    private var chunks: [String] {
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
        return stride(from: 0, to: lines.count, by: Self.linesPerChunk).map { start in
            lines[start..<min(start + Self.linesPerChunk, lines.count)].joined(separator: "\n")
        }
    }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: 0) {
                    if text.isEmpty {
                        Text("(no output yet)")
                            .font(.system(.caption, design: .monospaced))
                            .foregroundStyle(.secondary)
                            .padding(12)
                    } else {
                        LazyVStack(alignment: .leading, spacing: 0) {
                            ForEach(Array(chunks.enumerated()), id: \.offset) { _, chunk in
                                Text(chunk)
                                    .font(.system(.caption, design: .monospaced))
                                    .textSelection(.enabled)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .padding(.horizontal, 12)
                            }
                        }
                        .padding(.vertical, 12)
                    }
                    Color.clear.frame(height: 1).id("log-end")
                }
            }
            .onChange(of: text.utf8.count) { _, _ in
                if follow { proxy.scrollTo("log-end", anchor: .bottom) }
            }
            .onAppear { proxy.scrollTo("log-end", anchor: .bottom) }
        }
    }
}
