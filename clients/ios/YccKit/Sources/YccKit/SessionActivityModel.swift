import Foundation
import Observation
import YccProto

/// The two RPCs behind the session Activity view, abstracted so the tree,
/// timeline and output-follow logic are testable headlessly.
public protocol SessionActivitySource: Sendable {
    func getSessionActivity(project: String, sessionId: String) async throws
        -> Ycc_V1_GetSessionActivityResponse
    func getJobOutput(
        project: String, sessionId: String, jobId: String,
        offset: Int64, limit: Int32, tailLines: Int32
    ) async throws -> Ycc_V1_GetJobOutputResponse
}

extension YccClient: SessionActivitySource {}

/// A node status (or, for the coordinator root, the session phase).
public enum ActivityStatus: Equatable, Sendable {
    case running, done, failed, blocked, killed, lost
    case idle, paused, error, stopped
    case other(String)

    public init(_ raw: String) {
        switch raw {
        case "running": self = .running
        case "done": self = .done
        case "failed": self = .failed
        case "blocked": self = .blocked
        case "killed": self = .killed
        case "lost": self = .lost
        case "idle": self = .idle
        case "paused": self = .paused
        case "error": self = .error
        case "stopped": self = .stopped
        default: self = .other(raw)
        }
    }

    /// Work is still in flight: elapsed time grows and the row animates.
    public var isActive: Bool { self == .running }

    public var label: String {
        switch self {
        case .running: return "running"
        case .done: return "done"
        case .failed: return "failed"
        case .blocked: return "blocked"
        case .killed: return "killed"
        case .lost: return "lost"
        case .idle: return "idle"
        case .paused: return "paused"
        case .error: return "error"
        case .stopped: return "stopped"
        case .other(let raw): return raw.isEmpty ? "unknown" : raw
        }
    }
}

/// One row of the collapsible tree: a node with its depth and whether it has
/// children (so the view can draw a disclosure chevron).
public struct ActivityTreeRow: Identifiable, Equatable, Sendable {
    public var node: Ycc_V1_ActivityNode
    public var depth: Int
    public var hasChildren: Bool
    public var id: String { node.id }
}

/// One bar of the timeline, in seconds relative to the session's first start.
public struct ActivityTimelineBar: Equatable, Sendable {
    public var start: TimeInterval
    public var end: TimeInterval
    public var status: ActivityStatus
    public var label: String
    /// The agent round this bar draws; nil for a job or the coordinator.
    public var round: Int32? = nil
}

public struct ActivityTimelineLane: Identifiable, Equatable, Sendable {
    public var id: String
    public var label: String
    /// The node's kind (`coordinator`, an agent role, or a job kind like `bash`).
    public var kind: String
    public var depth: Int
    public var bars: [ActivityTimelineBar]
}

public struct ActivityTimeline: Equatable, Sendable {
    public var origin: Date
    /// Seconds from `origin` to the latest end (or now, when work is running).
    public var duration: TimeInterval
    public var lanes: [ActivityTimelineLane]
}

/// Pure tree/timeline/formatting helpers over the daemon's flat node list.
public enum ActivityGraph {
    /// Depth-first rows in the daemon's order (root first, then by start time),
    /// skipping the descendants of any id in `collapsed`. A node whose parent
    /// is unknown is shown under the root rather than dropped.
    public static func rows(
        nodes: [Ycc_V1_ActivityNode], collapsed: Set<String>
    ) -> [ActivityTreeRow] {
        guard !nodes.isEmpty else { return [] }
        let ids = Set(nodes.map(\.id))
        let rootID = nodes[0].id
        var children: [String: [Ycc_V1_ActivityNode]] = [:]
        for node in nodes.dropFirst() {
            let parent = ids.contains(node.parentID) && node.parentID != node.id ? node.parentID : rootID
            children[parent, default: []].append(node)
        }
        var out: [ActivityTreeRow] = []
        var visited: Set<String> = []
        func visit(_ node: Ycc_V1_ActivityNode, depth: Int) {
            guard visited.insert(node.id).inserted else { return }
            let kids = children[node.id] ?? []
            out.append(ActivityTreeRow(node: node, depth: depth, hasChildren: !kids.isEmpty))
            guard !collapsed.contains(node.id) else { return }
            for kid in kids { visit(kid, depth: depth + 1) }
        }
        visit(nodes[0], depth: 0)
        return out
    }

    /// Whether any node (or the coordinator phase) is still running.
    public static func hasRunningWork(_ nodes: [Ycc_V1_ActivityNode]) -> Bool {
        nodes.contains { ActivityStatus($0.status).isActive }
    }

    /// Lanes in tree order (one per node, nothing collapsed) with one bar per
    /// agent round, or one bar per job lifetime. A bar still running ends at
    /// `now`; a finished bar with no recorded end (the coordinator root) ends
    /// at the session's last event (the root's `lastActivityAt`, falling back
    /// to the latest recorded end), so a stopped session viewed later is not
    /// stretched out to the present. A node without any parsable start has no
    /// bars.
    public static func timeline(nodes: [Ycc_V1_ActivityNode], now: Date) -> ActivityTimeline? {
        let rows = rows(nodes: nodes, collapsed: [])
        var starts: [Date] = []
        var ends: [Date] = []
        for node in nodes {
            starts += [parseTime(node.startedAt)].compactMap { $0 } + node.spans.compactMap { parseTime($0.startedAt) }
            ends += [parseTime(node.finishedAt)].compactMap { $0 } + node.spans.compactMap { parseTime($0.finishedAt) }
        }
        guard let origin = starts.min() else { return nil }
        let lastEvent = nodes.first.flatMap { parseTime($0.lastActivityAt) }
        let settledEnd = hasRunningWork(nodes) ? now : (lastEvent ?? ends.max() ?? now)
        var latest: TimeInterval = 0
        var lanes: [ActivityTimelineLane] = []
        for row in rows {
            let node = row.node
            var bars: [ActivityTimelineBar] = []
            func add(start: String, end: String, status: String, label: String, round: Int32? = nil) {
                guard let s = parseTime(start) else { return }
                let st = ActivityStatus(status)
                let e = parseTime(end) ?? (st.isActive ? now : (parseTime(node.finishedAt) ?? settledEnd))
                let bar = ActivityTimelineBar(
                    start: max(0, s.timeIntervalSince(origin)),
                    end: max(0, e.timeIntervalSince(origin)),
                    status: st, label: label, round: round)
                bars.append(bar)
                latest = max(latest, bar.end)
            }
            if node.spans.isEmpty {
                add(start: node.startedAt, end: node.finishedAt, status: node.status, label: node.label)
            } else {
                for span in node.spans {
                    add(start: span.startedAt, end: span.finishedAt, status: span.status,
                        label: "round \(span.round)", round: span.round)
                }
            }
            lanes.append(ActivityTimelineLane(
                id: node.id, label: laneLabel(node), kind: node.kind, depth: row.depth, bars: bars))
        }
        return ActivityTimeline(origin: origin, duration: max(latest, 1), lanes: lanes)
    }

    /// A job lane is named by its id (its label is a whole command).
    static func laneLabel(_ node: Ycc_V1_ActivityNode) -> String {
        node.kind == "bash" ? "\(node.id) \(node.label)" : displayName(node)
    }

    /// Short name for a node: the coordinator, the agent id without its
    /// `agent:` prefix, or a job's command.
    public static func displayName(_ node: Ycc_V1_ActivityNode) -> String {
        if node.kind == "coordinator" { return node.label.isEmpty ? "Coordinator" : node.label }
        if !node.label.isEmpty { return node.label }
        if node.id.hasPrefix("agent:") { return String(node.id.dropFirst("agent:".count)) }
        return node.id
    }

    /// `started_at` → `finished_at` (or `now` while running) as "12s",
    /// "3m 04s" or "1h 02m"; empty when the node never started.
    public static func elapsed(start: String, end: String, status: ActivityStatus, now: Date) -> String {
        guard let s = parseTime(start) else { return "" }
        let e = parseTime(end) ?? (status.isActive ? now : nil)
        guard let e else { return "" }
        return formatDuration(e.timeIntervalSince(s))
    }

    public static func formatDuration(_ interval: TimeInterval) -> String {
        let total = max(0, Int(interval.rounded(.down)))
        let h = total / 3600, m = (total % 3600) / 60, s = total % 60
        if h > 0 { return String(format: "%dh %02dm", h, m) }
        if m > 0 { return String(format: "%dm %02ds", m, s) }
        return "\(s)s"
    }

    /// "1.2k" / "3.4M" token counts; exact below 1000.
    public static func formatTokens(_ tokens: Int64) -> String {
        if tokens >= 1_000_000 { return String(format: "%.1fM", Double(tokens) / 1_000_000) }
        if tokens >= 1_000 { return String(format: "%.1fk", Double(tokens) / 1_000) }
        return "\(tokens)"
    }

    private static let fractional: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()
    private static let plain: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()

    public static func parseTime(_ raw: String) -> Date? {
        guard !raw.isEmpty else { return nil }
        return fractional.date(from: raw) ?? plain.date(from: raw)
    }
}

/// Drives the Activity sheet: polls `GetSessionActivity` while the view is on
/// screen and the session still has work running.
@MainActor
@Observable
public final class SessionActivityModel {
    public private(set) var nodes: [Ycc_V1_ActivityNode] = []
    public private(set) var live = false
    public private(set) var hasLoaded = false
    public private(set) var errorMessage: String?
    public private(set) var unauthorized = false
    /// Ids whose children are hidden in the tree.
    public var collapsed: Set<String> = []

    public static let activePollInterval: TimeInterval = 2
    /// Slower cadence while the session is live but nothing is running, so a
    /// sheet left open still notices new work without hammering the daemon.
    public static let idlePollInterval: TimeInterval = 10
    /// First retry delay after a failed request; doubles up to the maximum.
    public nonisolated static let retryInterval: TimeInterval = 1
    public static let maxRetryInterval: TimeInterval = 30

    private let source: SessionActivitySource
    private let project: String
    private let sessionID: String
    private let retryInterval: TimeInterval

    public init(
        source: SessionActivitySource, project: String, sessionID: String,
        retryInterval: TimeInterval = SessionActivityModel.retryInterval
    ) {
        self.source = source
        self.project = project
        self.sessionID = sessionID
        self.retryInterval = retryInterval
    }

    public var rows: [ActivityTreeRow] { ActivityGraph.rows(nodes: nodes, collapsed: collapsed) }
    public var hasRunningWork: Bool { ActivityGraph.hasRunningWork(nodes) }
    public func node(id: String) -> Ycc_V1_ActivityNode? { nodes.first { $0.id == id } }

    public func toggleCollapsed(_ id: String) {
        if collapsed.contains(id) { collapsed.remove(id) } else { collapsed.insert(id) }
    }

    public func refresh() async {
        do {
            let response = try await source.getSessionActivity(project: project, sessionId: sessionID)
            // A superseded poll (Retry or dismissal cancelled it) must not
            // overwrite what its replacement fetched.
            if Task.isCancelled { return }
            nodes = response.nodes
            live = response.live
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch is CancellationError {
            return
        } catch {
            errorMessage = (error as? YccError)?.displayMessage ?? error.localizedDescription
        }
        hasLoaded = true
    }

    /// Refresh, then keep polling until cancelled (the view's `.task`) or a
    /// successful response says the session is no longer live. A failed
    /// request (including the first) is retried with backoff rather than
    /// taken as "not live".
    public func poll() async {
        var backoff = retryInterval
        while !Task.isCancelled {
            await refresh()
            guard !unauthorized else { return }
            let interval: TimeInterval
            if errorMessage != nil {
                interval = backoff
                backoff = min(backoff * 2, Self.maxRetryInterval)
            } else {
                backoff = retryInterval
                guard live else { return }
                interval = hasRunningWork ? Self.activePollInterval : Self.idlePollInterval
            }
            do { try await Task.sleep(nanoseconds: UInt64(interval * 1_000_000_000)) } catch { return }
        }
    }
}

/// Accumulates a job's output across `GetJobOutput` responses: decodes UTF-8
/// so a multi-byte character split across two chunks is not mangled, advances
/// the follow cursor, notes when bytes before the cursor were dropped, and
/// keeps only the newest `maxBytes` of text so a verbose job left open does
/// not grow without bound.
public struct JobOutputFollower: Equatable, Sendable {
    public private(set) var text = ""
    /// Absolute offset to request next.
    public private(set) var cursor: Int64 = 0
    /// End of the daemon's retained output as of the last response; more is
    /// pending locally while `cursor` is below it.
    public private(set) var retainedEnd: Int64 = 0
    /// The first chunk did not start at offset 0 (a tail, or an already
    /// trimmed buffer), so the beginning of the output is not shown.
    public private(set) var omittedHead = false
    /// While following, bytes before a chunk were dropped from the daemon's
    /// bounded buffer, so the shown text has a gap.
    public private(set) var droppedEarlier = false
    /// Older text was discarded locally to stay within `maxBytes`.
    public private(set) var trimmedHistory = false
    public private(set) var status = ""
    /// A trailing partial UTF-8 sequence held back until the next chunk.
    private var pending: [UInt8] = []
    public let maxBytes: Int

    public static let defaultMaxBytes = 256 * 1024

    public init(maxBytes: Int = JobOutputFollower.defaultMaxBytes) {
        self.maxBytes = maxBytes
    }

    /// Retained bytes the daemon has that we have not fetched yet.
    public var hasMore: Bool { cursor < retainedEnd }

    public mutating func apply(_ response: Ycc_V1_GetJobOutputResponse) {
        status = response.status
        retainedEnd = response.retainedEnd
        if cursor == 0 && text.isEmpty {
            omittedHead = response.start > 0
        } else if response.start > cursor || response.retainedStart > cursor {
            // Bytes between our cursor and this chunk are gone; a held-back
            // partial character can no longer be completed.
            droppedEarlier = true
            pending = []
        }
        append(bytes: Array(response.data))
        cursor = max(cursor, response.end)
    }

    private mutating func append(bytes: [UInt8]) {
        var buffer = pending + bytes
        pending = []
        let keep = Self.incompleteSuffixLength(buffer)
        if keep > 0 {
            pending = Array(buffer.suffix(keep))
            buffer.removeLast(keep)
        }
        guard !buffer.isEmpty else { return }
        text += String(decoding: buffer, as: UTF8.self)
        trim()
    }

    /// Drop the oldest text past `maxBytes`, cutting after the first newline
    /// near the cut point (not one far past it, which would discard most of
    /// the history of a long-lined log), otherwise at the next character
    /// boundary.
    private mutating func trim() {
        let utf8 = text.utf8
        let excess = utf8.count - maxBytes
        guard excess > 0 else { return }
        var cut = utf8.index(utf8.startIndex, offsetBy: excess)
        if let newline = utf8[cut...].firstIndex(of: 0x0A), utf8.distance(from: cut, to: newline) < 4096 {
            cut = utf8.index(after: newline)
        } else {
            while cut < utf8.endIndex, String.Index(cut, within: text) == nil {
                cut = utf8.index(after: cut)
            }
        }
        text = String(text[cut...])
        trimmedHistory = true
    }

    /// Length of a trailing, not-yet-complete UTF-8 sequence (0–3 bytes).
    static func incompleteSuffixLength(_ bytes: [UInt8]) -> Int {
        var i = bytes.count - 1
        var continuation = 0
        while i >= 0, continuation < 3 {
            let b = bytes[i]
            if b & 0xC0 == 0x80 {
                continuation += 1
                i -= 1
                continue
            }
            let needed: Int
            if b & 0xE0 == 0xC0 { needed = 2 } else if b & 0xF0 == 0xE0 { needed = 3 } else if b & 0xF8 == 0xF0 { needed = 4 } else { return 0 }
            return continuation + 1 < needed ? continuation + 1 : 0
        }
        return 0
    }
}

/// Loads a job's output tail, then follows it about once a second while the
/// job runs, and drains whatever the daemon still retains once it stops.
/// Falls back to the node's bounded `result` when the daemon cannot serve the
/// output (session not live, job unknown).
@MainActor
@Observable
public final class JobOutputModel {
    public private(set) var follower = JobOutputFollower()
    public private(set) var available = true
    public private(set) var unavailableReason = ""
    public private(set) var errorMessage: String?
    public private(set) var hasLoaded = false

    public static let tailLines: Int32 = 200
    public nonisolated static let followInterval: TimeInterval = 1
    /// First retry delay after a failed request; doubles up to the maximum.
    public nonisolated static let retryInterval: TimeInterval = 1
    public static let maxRetryInterval: TimeInterval = 30

    private let source: SessionActivitySource
    private let project: String
    private let sessionID: String
    private let jobID: String
    private let followInterval: TimeInterval
    private let retryInterval: TimeInterval

    public init(
        source: SessionActivitySource, project: String, sessionID: String, jobID: String,
        followInterval: TimeInterval = JobOutputModel.followInterval,
        retryInterval: TimeInterval = JobOutputModel.retryInterval
    ) {
        self.source = source
        self.project = project
        self.sessionID = sessionID
        self.jobID = jobID
        self.followInterval = followInterval
        self.retryInterval = retryInterval
    }

    public var text: String { follower.text }
    public var isRunning: Bool { ActivityStatus(follower.status).isActive }

    /// Tail first, then advance by cursor until the task is cancelled or the
    /// job has stopped *and* its retained output is drained: forward reads are
    /// capped, so a finished job can still have unread bytes. Catch-up reads
    /// are issued back to back; a failed request is retried with backoff.
    public func follow() async {
        var tail = true
        var backoff = retryInterval
        while !Task.isCancelled {
            let before = follower.cursor
            let delay: TimeInterval
            if await fetch(tail: tail) {
                tail = false
                backoff = retryInterval
                guard available else { return }
                if follower.hasMore {
                    // Only a read that advanced may skip the delay, so an
                    // unexpected empty response cannot spin.
                    if follower.cursor > before { continue }
                    delay = followInterval
                } else if isRunning {
                    delay = followInterval
                } else {
                    return
                }
            } else {
                delay = backoff
                backoff = min(backoff * 2, Self.maxRetryInterval)
            }
            do { try await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000)) } catch { return }
        }
    }

    /// One request; false when it failed (the error is in `errorMessage`).
    private func fetch(tail: Bool) async -> Bool {
        defer { hasLoaded = true }
        do {
            let response = try await source.getJobOutput(
                project: project, sessionId: sessionID, jobId: jobID,
                offset: tail ? 0 : follower.cursor, limit: 0, tailLines: tail ? Self.tailLines : 0)
            available = response.available
            unavailableReason = response.unavailableReason
            if response.available { follower.apply(response) }
            errorMessage = nil
            return true
        } catch is CancellationError {
            return false
        } catch {
            errorMessage = (error as? YccError)?.displayMessage ?? error.localizedDescription
            return false
        }
    }
}
