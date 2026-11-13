import Foundation
import Observation
import YccProto

/// The watch RPCs the Watches screens use (docs/design/watches.md, spec §9.2).
public protocol WatchesSource: Sendable {
    func listWatches(project: String) async throws -> [Ycc_V1_Watch]
    func getWatch(project: String, name: String, reportLimit: Int32) async throws -> Ycc_V1_GetWatchResponse
    func setWatchPaused(project: String, name: String, paused: Bool) async throws -> Ycc_V1_Watch
    func runWatchNow(project: String, name: String) async throws
    func deleteWatch(project: String, name: String) async throws
    func createWatch(project: String, definition: Ycc_V1_WatchDefinition) async throws -> Ycc_V1_Watch
    func updateWatch(project: String, definition: Ycc_V1_WatchDefinition) async throws -> Ycc_V1_Watch
    /// The editor's model picker.
    func listModels() async throws -> Ycc_V1_ListModelsResponse
}

extension YccClient: WatchesSource {
    public func listWatches(project: String) async throws -> [Ycc_V1_Watch] {
        var request = Ycc_V1_ListWatchesRequest()
        request.project = project
        switch await generated.listWatches(request: request).result {
        case .success(let message): return message.watches
        case .failure(let error): throw Self.map(error)
        }
    }

    public func getWatch(project: String, name: String, reportLimit: Int32) async throws -> Ycc_V1_GetWatchResponse {
        var request = Ycc_V1_GetWatchRequest()
        request.project = project
        request.name = name
        request.reportLimit = reportLimit
        switch await generated.getWatch(request: request).result {
        case .success(let message): return message
        case .failure(let error): throw Self.map(error)
        }
    }

    public func setWatchPaused(project: String, name: String, paused: Bool) async throws -> Ycc_V1_Watch {
        var request = Ycc_V1_SetWatchPausedRequest()
        request.project = project
        request.name = name
        request.paused = paused
        switch await generated.setWatchPaused(request: request).result {
        case .success(let message): return message.watch
        case .failure(let error): throw Self.map(error)
        }
    }

    public func runWatchNow(project: String, name: String) async throws {
        var request = Ycc_V1_RunWatchNowRequest()
        request.project = project
        request.name = name
        if case .failure(let error) = await generated.runWatchNow(request: request).result {
            throw Self.map(error)
        }
    }

    public func deleteWatch(project: String, name: String) async throws {
        var request = Ycc_V1_DeleteWatchRequest()
        request.project = project
        request.name = name
        if case .failure(let error) = await generated.deleteWatch(request: request).result {
            throw Self.map(error)
        }
    }

    public func createWatch(project: String, definition: Ycc_V1_WatchDefinition) async throws -> Ycc_V1_Watch {
        var request = Ycc_V1_CreateWatchRequest()
        request.project = project
        request.definition = definition
        switch await generated.createWatch(request: request).result {
        case .success(let message): return message.watch
        case .failure(let error): throw Self.map(error)
        }
    }

    public func updateWatch(project: String, definition: Ycc_V1_WatchDefinition) async throws -> Ycc_V1_Watch {
        var request = Ycc_V1_UpdateWatchRequest()
        request.project = project
        request.definition = definition
        switch await generated.updateWatch(request: request).result {
        case .success(let message): return message.watch
        case .failure(let error): throw Self.map(error)
        }
    }
}

// MARK: - Presentation

/// How loud a watch's state is; the view maps it to a colour.
public enum WatchTone: Int, Sendable, Comparable {
    case alert, warn, ok, muted

    public static func < (a: WatchTone, b: WatchTone) -> Bool { a.rawValue < b.rawValue }
}

/// Labels and ordering for watches, worded like the web client's.
@MainActor
public enum WatchFormat {
    public static func name(_ watch: Ycc_V1_Watch) -> String { watch.definition.name }

    /// An RFC 3339 field as a date; nil when empty (unset) or unparseable.
    public static func date(_ value: String) -> Date? {
        SessionListModel.parseTimestamp(value)
    }

    /// The latest report, or nil before the first one.
    public static func last(_ watch: Ycc_V1_Watch) -> Ycc_V1_WatchReport? {
        watch.hasLast ? watch.last : nil
    }

    /// A report's label: its status (ok/warn/alert), else its kind (failed/skipped).
    public static func reportLabel(_ report: Ycc_V1_WatchReport?) -> String {
        guard let report else { return "no runs yet" }
        if report.kind == "failed" { return "failed" }
        if !report.status.isEmpty { return report.status }
        return report.kind.isEmpty ? "unknown" : report.kind
    }

    /// Failed runs count as warn (the design doc); skipped entries carry no verdict.
    public static func reportTone(_ report: Ycc_V1_WatchReport?) -> WatchTone {
        guard let report else { return .muted }
        if report.kind == "failed" { return .warn }
        switch report.status {
        case "ok": return .ok
        case "warn": return .warn
        case "alert": return .alert
        default: return .muted
        }
    }

    /// What produced a report, when its badge doesn't already say.
    public static func kindNote(_ kind: String) -> String {
        switch kind {
        case "run": return "model run"
        case "probe": return "probe only"
        case "skipped", "failed": return ""
        default: return kind
        }
    }

    /// "30m", "2h", "1d 6h": an interval in its two largest units.
    public static func interval(_ seconds: Int64) -> String {
        guard seconds > 0 else { return "—" }
        var rest = seconds
        var parts: [String] = []
        for (unit, size) in [("d", Int64(86_400)), ("h", 3_600), ("m", 60), ("s", 1)] where rest >= size {
            parts.append("\(rest / size)\(unit)")
            rest %= size
        }
        return parts.prefix(2).joined(separator: " ")
    }

    /// "due now", "in 5m", "in 3h", "in 2d" for a future time.
    public static func until(_ date: Date, now: Date = Date()) -> String {
        let seconds = Int((date.timeIntervalSince(now)).rounded())
        if seconds < 45 { return "due now" }
        let minutes = Int((Double(seconds) / 60).rounded())
        if minutes < 60 { return "in \(minutes)m" }
        let hours = Int((Double(minutes) / 60).rounded())
        if hours < 48 { return "in \(hours)h" }
        return "in \(Int((Double(hours) / 24).rounded()))d"
    }

    public struct Schedule: Equatable, Sendable {
        public let label: String
        public let running: Bool
    }

    /// The schedule line: paused or expired, running, or when the next run is due.
    public static func schedule(_ watch: Ycc_V1_Watch, now: Date = Date()) -> Schedule {
        if watch.paused { return Schedule(label: isExpired(watch) ? "expired" : "paused", running: false) }
        if watch.running { return Schedule(label: "running", running: true) }
        guard let due = SessionListModel.parseTimestamp(watch.nextDue) else {
            return Schedule(label: "scheduled", running: false)
        }
        return Schedule(label: "next \(until(due, now: now))", running: false)
    }

    /// Paused because its expiry passed; resuming needs a later expiry first.
    public static func isExpired(_ watch: Ycc_V1_Watch) -> Bool {
        watch.paused && watch.pausedReason == "expired"
    }

    /// Runs parked on a question, as a short indicator ("" when none).
    public static func parkedLabel(_ watch: Ycc_V1_Watch) -> String {
        switch watch.parkedSessionIds.count {
        case 0: return ""
        case 1: return "1 question waiting"
        case let n: return "\(n) questions waiting"
        }
    }

    /// Waiting questions, then alert, warn, ok, never-run; paused last; by name within.
    public static func sorted(_ watches: [Ycc_V1_Watch]) -> [Ycc_V1_Watch] {
        func rank(_ watch: Ycc_V1_Watch) -> Int {
            if !watch.parkedSessionIds.isEmpty { return -1 }
            if watch.paused { return 4 }
            return reportTone(last(watch)).rawValue
        }
        return watches.sorted { a, b in
            let (ra, rb) = (rank(a), rank(b))
            return ra != rb ? ra < rb : name(a) < name(b)
        }
    }

    /// Poll quickly while a run is active (a report lands soon), else slowly.
    public static func pollInterval(_ watches: [Ycc_V1_Watch]) -> TimeInterval {
        watches.contains(where: \.running) ? 5 : 30
    }
}

// MARK: - Models

/// Orders responses so a slow poll issued before an action completed can't
/// replace the state that action returned.
private struct ResponseOrder {
    private var issued = 0
    private var applied = 0

    mutating func issue() -> Int {
        issued += 1
        return issued
    }

    /// Whether a response for `id` is newer than everything applied so far.
    mutating func accept(_ id: Int) -> Bool {
        guard id > applied else { return false }
        applied = id
        return true
    }
}

/// A project's watches for the Watches screen, with pause/resume, run now
/// and delete.
@MainActor
@Observable
public final class WatchesModel {
    public let project: String
    /// Sorted for display (``WatchFormat/sorted(_:)``).
    public private(set) var watches: [Ycc_V1_Watch] = []
    public private(set) var hasLoaded = false
    public private(set) var isLoading = false
    /// Why the latest load failed; the list keeps its last rows.
    public private(set) var errorMessage: String?
    public private(set) var unauthorized = false
    /// Watches with an action in flight.
    public private(set) var busyNames: Set<String> = []
    /// Why the latest action failed, for an alert.
    public var actionError: String?

    private let source: WatchesSource
    @ObservationIgnored private var order = ResponseOrder()

    public init(project: String, source: WatchesSource) {
        self.project = project
        self.source = source
    }

    public var pollInterval: TimeInterval { WatchFormat.pollInterval(watches) }

    public func refresh() async {
        let id = order.issue()
        isLoading = true
        defer { isLoading = false }
        do {
            let loaded = try await source.listWatches(project: project)
            guard order.accept(id) else { return }
            let sorted = WatchFormat.sorted(loaded)
            if sorted != watches { watches = sorted }
            hasLoaded = true
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            errorMessage = watchErrorMessage(error)
        }
    }

    public func setPaused(_ name: String, paused: Bool) async {
        await perform(name) {
            let updated = try await self.source.setWatchPaused(project: self.project, name: name, paused: paused)
            self.install(updated)
        }
    }

    public func runNow(_ name: String) async {
        let ran = await perform(name) {
            try await self.source.runWatchNow(project: self.project, name: name)
        }
        if ran { await refresh() }
    }

    @discardableResult
    public func delete(_ name: String) async -> Bool {
        await perform(name) {
            try await self.source.deleteWatch(project: self.project, name: name)
            _ = self.order.accept(self.order.issue())
            self.watches.removeAll { WatchFormat.name($0) == name }
        }
    }

    /// Show a watch the editor just created or saved.
    public func didSave(_ watch: Ycc_V1_Watch) { install(watch) }

    private func install(_ watch: Ycc_V1_Watch) {
        guard order.accept(order.issue()) else { return }
        var list = watches
        if let index = list.firstIndex(where: { WatchFormat.name($0) == WatchFormat.name(watch) }) {
            list[index] = watch
        } else {
            list.append(watch)
        }
        watches = WatchFormat.sorted(list)
    }

    @discardableResult
    private func perform(_ name: String, _ body: () async throws -> Void) async -> Bool {
        guard !busyNames.contains(name) else { return false }
        busyNames.insert(name)
        defer { busyNames.remove(name) }
        do {
            try await body()
            return true
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            actionError = watchErrorMessage(error)
        }
        return false
    }
}

/// One watch with its report timeline, for the watch detail screen.
@MainActor
@Observable
public final class WatchDetailModel {
    /// Reports fetched per page of the timeline.
    public static let reportPage: Int32 = 20

    public let project: String
    public let name: String
    public private(set) var watch: Ycc_V1_Watch?
    /// Newest first.
    public private(set) var reports: [Ycc_V1_WatchReport] = []
    public private(set) var reportLimit: Int32 = WatchDetailModel.reportPage
    public private(set) var isLoading = false
    /// Why the latest load failed; the screen keeps its last state.
    public private(set) var errorMessage: String?
    /// The daemon has no such watch (deleted elsewhere).
    public private(set) var notFound = false
    public private(set) var unauthorized = false
    public private(set) var isBusy = false
    public var actionError: String?

    private let source: WatchesSource
    @ObservationIgnored private var order = ResponseOrder()

    public init(project: String, name: String, source: WatchesSource) {
        self.project = project
        self.name = name
        self.source = source
    }

    /// A full page came back, so older reports may exist.
    public var mayHaveOlderReports: Bool { reports.count >= Int(reportLimit) }

    public var pollInterval: TimeInterval { watch?.running == true ? 5 : 30 }

    public func refresh() async {
        let id = order.issue()
        isLoading = true
        defer { isLoading = false }
        do {
            let response = try await source.getWatch(project: project, name: name, reportLimit: reportLimit)
            guard order.accept(id) else { return }
            if response.watch != watch { watch = response.watch }
            if response.reports != reports { reports = response.reports }
            notFound = false
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch YccError.notFound {
            guard order.accept(id) else { return }
            notFound = true
        } catch {
            errorMessage = watchErrorMessage(error)
        }
    }

    /// Extend the timeline by another page of older reports.
    public func loadOlder() async {
        guard mayHaveOlderReports, !isLoading else { return }
        reportLimit += Self.reportPage
        await refresh()
    }

    public func setPaused(_ paused: Bool) async {
        await perform {
            let updated = try await self.source.setWatchPaused(project: self.project, name: self.name, paused: paused)
            if self.order.accept(self.order.issue()) { self.watch = updated }
        }
    }

    public func runNow() async {
        if await perform({ try await self.source.runWatchNow(project: self.project, name: self.name) }) {
            await refresh()
        }
    }

    /// True once the watch is gone; the screen then leaves.
    public func delete() async -> Bool {
        await perform { try await self.source.deleteWatch(project: self.project, name: self.name) }
    }

    /// Show the definition the editor just saved.
    public func didSave(_ updated: Ycc_V1_Watch) {
        if order.accept(order.issue()) { watch = updated }
    }

    @discardableResult
    private func perform(_ body: () async throws -> Void) async -> Bool {
        guard !isBusy else { return false }
        isBusy = true
        defer { isBusy = false }
        do {
            try await body()
            return true
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            actionError = watchErrorMessage(error)
        }
        return false
    }
}

private func watchErrorMessage(_ error: Error) -> String {
    (error as? YccError)?.displayMessage ?? error.localizedDescription
}
