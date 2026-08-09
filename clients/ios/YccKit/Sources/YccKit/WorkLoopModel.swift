import Foundation
import Observation
import YccProto

/// The data source used by ``WorkLoopModel``. Keeping the three daemon RPCs
/// behind a protocol makes the lifecycle and digest logic testable without a
/// connection or simulator.
public protocol WorkLoopSource: Sendable {
    func startWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo
    func stopWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo?
    func getWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo?
}

extension YccClient: WorkLoopSource {}

/// The daemon-side work loop lifecycle. Unknown future wire values remain safe.
public enum WorkLoopState: String, Sendable, CaseIterable, Equatable {
    case running
    case waiting
    case stopping
    case finished
    case none
    case unknown

    public init(state: String?) {
        guard let state else {
            self = .none
            return
        }
        self = WorkLoopState(rawValue: state.lowercased()) ?? .unknown
    }

    public var title: String {
        switch self {
        case .running: return "Running"
        case .waiting: return "Waiting"
        case .stopping: return "Stopping"
        case .finished: return "Finished"
        case .none: return "Not running"
        case .unknown: return "Unknown"
        }
    }

    public var canStart: Bool { self == .none || self == .finished }
    public var canStop: Bool { self == .running || self == .waiting }
    public var isActive: Bool { self == .running || self == .waiting || self == .stopping }
    public var shouldPoll: Bool { isActive }
}

/// One ordered group in a completed work loop's backlog digest.
public struct WorkLoopDigestSection: Sendable, Identifiable {
    public let title: String
    public let systemImage: String
    public let rows: [Ycc_V1_WorkLoopDigestTask]

    public var id: String { title }

    public init(title: String, systemImage: String, rows: [Ycc_V1_WorkLoopDigestTask]) {
        self.title = title
        self.systemImage = systemImage
        self.rows = rows
    }
}

/// Drives start/observe/stop for the daemon-owned unattended backlog loop. The
/// daemon remains the source of truth and keeps working when the app suspends;
/// this model simply applies snapshots returned by actions and polls while active.
@MainActor
@Observable
public final class WorkLoopModel {
    public let project: String
    public private(set) var loop: Ycc_V1_WorkLoopInfo?
    public private(set) var state: WorkLoopState = .none
    public private(set) var isLoading = false
    public private(set) var isBusy = false
    public private(set) var errorMessage: String?
    public var actionError: String?
    public private(set) var unauthorized = false

    private let source: WorkLoopSource

    public init(source: WorkLoopSource, project: String) {
        self.source = source
        self.project = project
    }

    public var hasDigest: Bool {
        guard let loop else { return false }
        return !loop.completed.isEmpty || !loop.blocked.isEmpty
            || !loop.inReview.isEmpty || !loop.created.isEmpty
    }

    public var currentSessionID: String {
        Self.currentSessionID(for: loop)
    }

    public var canStart: Bool { state.canStart && !isBusy }
    public var canStop: Bool { state.canStop && !isBusy }
    public var shouldPoll: Bool { state.shouldPoll }

    /// Reload the persisted daemon snapshot. A load failure preserves the last
    /// good snapshot so transient network errors do not blank an active loop.
    public func refresh() async {
        guard !isBusy else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            apply(try await source.getWorkLoop(project: project))
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            errorMessage = Self.message(for: error)
        }
    }

    /// Start a loop and immediately apply the response without another RPC.
    public func start() async {
        guard !isBusy else { return }
        isBusy = true
        defer { isBusy = false }
        do {
            apply(try await source.startWorkLoop(project: project))
            actionError = nil
            errorMessage = nil
        } catch {
            handleAction(error)
        }
    }

    /// Gracefully stop a loop and immediately apply the stopping/finished snapshot.
    public func stop() async {
        guard !isBusy else { return }
        isBusy = true
        defer { isBusy = false }
        do {
            apply(try await source.stopWorkLoop(project: project))
            actionError = nil
            errorMessage = nil
        } catch {
            handleAction(error)
        }
    }

    private func apply(_ snapshot: Ycc_V1_WorkLoopInfo?) {
        loop = snapshot
        state = Self.state(for: snapshot)
    }

    private func handleAction(_ error: Error) {
        if case YccError.unauthorized = error {
            unauthorized = true
        } else {
            actionError = Self.message(for: error)
        }
    }

    private static func message(for error: Error) -> String {
        (error as? YccError)?.displayMessage ?? error.localizedDescription
    }

    // MARK: - Pure helpers

    public static func state(for loop: Ycc_V1_WorkLoopInfo?) -> WorkLoopState {
        WorkLoopState(state: loop?.state)
    }

    public static func currentSessionID(for loop: Ycc_V1_WorkLoopInfo?) -> String {
        loop?.currentSessionID.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    }

    /// Digest groups always appear in product order and empty groups disappear.
    public static func digestSections(for loop: Ycc_V1_WorkLoopInfo) -> [WorkLoopDigestSection] {
        [
            WorkLoopDigestSection(title: "Completed", systemImage: "checkmark.circle", rows: loop.completed),
            WorkLoopDigestSection(title: "Blocked", systemImage: "exclamationmark.octagon", rows: loop.blocked),
            WorkLoopDigestSection(title: "In review", systemImage: "eye", rows: loop.inReview),
            WorkLoopDigestSection(title: "Created", systemImage: "plus.circle", rows: loop.created),
        ].filter { !$0.rows.isEmpty }
    }

    /// A compact status line for the backlog's work-loop banner.
    public static func bannerLine(for loop: Ycc_V1_WorkLoopInfo?) -> String {
        guard let loop else { return WorkLoopState.none.title }
        switch state(for: loop) {
        case .running:
            return "Running · \(summaryLine(for: loop))"
        case .waiting:
            return waitingLine(for: loop)
        case .stopping:
            return "Stopping…"
        case .finished:
            return "Finished · \(summaryLine(for: loop))"
        case .none, .unknown:
            return state(for: loop).title
        }
    }

    /// A compact lifecycle/digest summary such as
    /// "3 sessions · 2 completed, 1 blocked".
    public static func summaryLine(for loop: Ycc_V1_WorkLoopInfo) -> String {
        let sessionCount = Int(loop.sessionsRun)
        let sessionPart = "\(sessionCount) \(sessionCount == 1 ? "session" : "sessions")"
        let counts: [(Int, String)] = [
            (loop.completed.count, "completed"),
            (loop.blocked.count, "blocked"),
            (loop.inReview.count, "in review"),
            (loop.created.count, "created"),
        ]
        let digestPart = counts.compactMap { count, label in
            count == 0 ? nil : "\(count) \(label)"
        }.joined(separator: ", ")
        return digestPart.isEmpty ? sessionPart : "\(sessionPart) · \(digestPart)"
    }

    public static func totalsLine(for loop: Ycc_V1_WorkLoopInfo) -> String {
        "\(UsageModel.formatTokens(loop.totalTokens)) tokens · \(formatCost(loop.totalCost, status: loop.costStatus))"
    }

    public static func totalsLine(tokens: Int64, cost: Double, priceStatus: String) -> String {
        "\(UsageModel.formatTokens(tokens)) tokens · \(formatCost(cost, status: priceStatus))"
    }

    /// Compact wall-clock duration for a completed work-loop session.
    public static func durationText(secs: Int64) -> String? {
        guard secs > 0 else { return nil }
        if secs < 60 {
            return "\(secs)s"
        }
        if secs < 3_600 {
            return "\(secs / 60)m \(secs % 60)s"
        }
        return "\(secs / 3_600)h \((secs % 3_600) / 60)m"
    }

    /// Cost text never presents incomplete pricing as exact.
    public static func formatCost(_ cost: Double, status: String) -> String {
        let status = status.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !status.isEmpty else { return "unpriced" }
        switch PriceStatus(status: status) {
        case .priced:
            return String(format: "$%.4f", cost)
        case .partial:
            return String(format: "≈$%.4f (partial)", cost)
        case .unpriced:
            return "unpriced"
        }
    }

    public static func startedAtDate(for loop: Ycc_V1_WorkLoopInfo) -> Date? {
        parseTimestamp(loop.startedAt)
    }

    public static func resumeAtDate(for loop: Ycc_V1_WorkLoopInfo) -> Date? {
        parseTimestamp(loop.resumeAt)
    }

    /// A localized waiting status such as
    /// "Waiting for provider (rate_limit) — resumes 03:15".
    public static func waitingLine(for loop: Ycc_V1_WorkLoopInfo) -> String {
        let kind = loop.waitKind.trimmingCharacters(in: .whitespacesAndNewlines)
        var line = "Waiting for provider"
        if !kind.isEmpty {
            line += " (\(kind))"
        }
        if let resumeAt = resumeAtDate(for: loop) {
            line += " — resumes \(resumeAt.formatted(date: .omitted, time: .shortened))"
        }
        return line
    }

    private static func parseTimestamp(_ value: String) -> Date? {
        guard !value.isEmpty else { return nil }
        return isoWithFraction.date(from: value) ?? isoPlain.date(from: value)
    }

    private static let isoWithFraction: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()

    private static let isoPlain: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter
    }()
}
