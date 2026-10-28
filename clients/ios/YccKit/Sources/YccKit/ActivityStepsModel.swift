import Foundation
import Observation
import YccProto

/// The RPC behind an Activity time breakdown, abstracted for headless tests.
public protocol ActivityStepsSource: Sendable {
    func getActivitySteps(
        project: String, sessionId: String, actor: String,
        since: String, until: String, limit: Int32
    ) async throws -> Ycc_V1_GetActivityStepsResponse
}

extension YccClient: ActivityStepsSource {}

/// One displayed step: a model turn or tool call, or the in-flight model
/// request inferred while a round runs with no open tool call.
public struct ActivityStepItem: Identifiable, Equatable, Sendable {
    /// Chronological position, stable while the list only grows at its end.
    public var id: Int
    public var kind: String
    public var name: String
    public var summary: String
    public var status: String
    public var start: Date?
    /// Measured duration, or the time elapsed so far while running.
    public var duration: TimeInterval
    public var outputTokens: Int64
    public var rowID: String
    /// Inferred rather than reported: the model is working on its next turn.
    public var isInferred: Bool

    public var isRunning: Bool { status == "running" }
    public var isModel: Bool { kind == "model" }
}

/// Count and summed time of one tool (or the model) across the range.
public struct ActivityStepTotalItem: Identifiable, Equatable, Sendable {
    public var kind: String
    public var name: String
    public var count: Int
    public var duration: TimeInterval
    public var id: String { "\(kind)/\(name)" }
}

/// Where a round's (or the coordinator's) wall time went.
public struct ActivityBreakdown: Equatable, Sendable {
    public var wall: TimeInterval
    public var model: TimeInterval
    public var modelCount: Int
    public var tools: TimeInterval
    public var toolCount: Int
    /// Longest first.
    public var totals: [ActivityStepTotalItem]
    /// Wall time outside any model request or tool call: idle waits for the
    /// user, scheduling, and loop overhead.
    public var other: TimeInterval { max(0, wall - model - tools) }
}

public enum ActivityStepOrder: String, CaseIterable, Sendable {
    case time = "In order"
    case slowest = "Slowest"
}

/// Pure step/breakdown helpers over a `GetActivitySteps` response.
public enum ActivitySteps {
    /// Steps in order with live durations. While `running` and no reported
    /// step is open, the model is computing its next turn: an inferred model
    /// step runs from the last step's end (or `rangeStart`) to `now`.
    public static func items(
        steps: [Ycc_V1_ActivityStep], rangeStart: Date?, running: Bool, model: String, now: Date
    ) -> [ActivityStepItem] {
        var out: [ActivityStepItem] = []
        var lastEnd = rangeStart
        for (index, step) in steps.enumerated() {
            let start = ActivityGraph.parseTime(step.startedAt)
            let end = ActivityGraph.parseTime(step.finishedAt)
            let open = step.status == "running"
            var duration = Double(step.durationMs) / 1000
            if open, let start { duration = max(0, now.timeIntervalSince(start)) }
            out.append(ActivityStepItem(
                id: index, kind: step.kind, name: step.name, summary: step.summary, status: step.status,
                start: start, duration: duration, outputTokens: step.outputTokens, rowID: step.rowID,
                isInferred: false))
            if let end, end > (lastEnd ?? .distantPast) { lastEnd = end }
        }
        if running, !out.contains(where: \.isRunning), let lastEnd, lastEnd <= now {
            out.append(ActivityStepItem(
                id: out.count, kind: "model", name: model.isEmpty ? "model" : model,
                summary: "waiting for the model…", status: "running", start: lastEnd,
                duration: now.timeIntervalSince(lastEnd), outputTokens: 0, rowID: "", isInferred: true))
        }
        return out
    }

    /// Totals come from the listed items when the list is complete (so running
    /// steps tick live), otherwise from the daemon's whole-range totals plus
    /// any inferred in-flight step.
    public static func breakdown(
        items: [ActivityStepItem], totals: [Ycc_V1_ActivityStepTotal], complete: Bool, wall: TimeInterval
    ) -> ActivityBreakdown {
        var byKey: [String: ActivityStepTotalItem] = [:]
        func add(kind: String, name: String, count: Int, duration: TimeInterval) {
            let key = "\(kind)/\(name)"
            var total = byKey[key] ?? ActivityStepTotalItem(kind: kind, name: name, count: 0, duration: 0)
            total.count += count
            total.duration += duration
            byKey[key] = total
        }
        if complete {
            for item in items { add(kind: item.kind, name: item.name, count: 1, duration: item.duration) }
        } else {
            for total in totals {
                add(kind: total.kind, name: total.name, count: Int(total.count), duration: Double(total.durationMs) / 1000)
            }
            for item in items where item.isInferred {
                add(kind: item.kind, name: item.name, count: 1, duration: item.duration)
            }
        }
        let sorted = byKey.values.sorted {
            $0.duration != $1.duration ? $0.duration > $1.duration : $0.id < $1.id
        }
        let model = sorted.filter { $0.kind == "model" }
        let tools = sorted.filter { $0.kind != "model" }
        return ActivityBreakdown(
            wall: wall,
            model: model.reduce(0) { $0 + $1.duration }, modelCount: model.reduce(0) { $0 + $1.count },
            tools: tools.reduce(0) { $0 + $1.duration }, toolCount: tools.reduce(0) { $0 + $1.count },
            totals: sorted)
    }

    public static func ordered(_ items: [ActivityStepItem], by order: ActivityStepOrder) -> [ActivityStepItem] {
        switch order {
        case .time: return items
        case .slowest: return items.sorted { $0.duration != $1.duration ? $0.duration > $1.duration : $0.id < $1.id }
        }
    }
}

/// Loads one actor's steps for a range, refreshing while the range is open.
@MainActor
@Observable
public final class ActivityStepsModel {
    public private(set) var steps: [Ycc_V1_ActivityStep] = []
    public private(set) var totals: [Ycc_V1_ActivityStepTotal] = []
    public private(set) var omittedEarlier = 0
    public private(set) var hasLoaded = false
    public private(set) var errorMessage: String?
    public private(set) var unauthorized = false

    public static let limit: Int32 = 500
    public nonisolated static let pollInterval: TimeInterval = 2
    public static let maxRetryInterval: TimeInterval = 30

    private let source: ActivityStepsSource
    private let project: String
    private let sessionID: String
    private let actor: String
    private let pollInterval: TimeInterval

    public init(
        source: ActivityStepsSource, project: String, sessionID: String, actor: String,
        pollInterval: TimeInterval = ActivityStepsModel.pollInterval
    ) {
        self.source = source
        self.project = project
        self.sessionID = sessionID
        self.actor = actor
        self.pollInterval = pollInterval
    }

    /// Fetch, then keep refreshing while `open` (the range has no end yet)
    /// until cancelled; a failed request is retried with backoff.
    public func poll(since: String, until: String, open: Bool) async {
        var backoff = pollInterval
        while !Task.isCancelled {
            let ok = await refresh(since: since, until: until)
            if unauthorized || (ok && !open) { return }
            let delay = ok ? pollInterval : backoff
            backoff = ok ? pollInterval : min(backoff * 2, Self.maxRetryInterval)
            do { try await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000)) } catch { return }
        }
    }

    @discardableResult
    public func refresh(since: String, until: String) async -> Bool {
        do {
            let response = try await source.getActivitySteps(
                project: project, sessionId: sessionID, actor: actor,
                since: since, until: until, limit: Self.limit)
            if Task.isCancelled { return false }
            steps = response.steps
            totals = response.totals
            omittedEarlier = Int(response.omittedEarlier)
            errorMessage = nil
            hasLoaded = true
            return true
        } catch YccError.unauthorized {
            unauthorized = true
        } catch is CancellationError {
            return false
        } catch {
            errorMessage = (error as? YccError)?.displayMessage ?? error.localizedDescription
        }
        hasLoaded = true
        return false
    }
}
