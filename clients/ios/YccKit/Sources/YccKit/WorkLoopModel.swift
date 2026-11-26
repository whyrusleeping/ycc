import Foundation
import Observation
import YccProto

/// The data source used by ``WorkLoopModel``. Keeping the three daemon RPCs
/// behind a protocol makes the lifecycle and digest logic testable without a
/// connection or simulator.
public protocol WorkLoopSource: Sendable {
    /// `parallel` 0 uses the project's configured slot count.
    func startWorkLoop(project: String, parallel: Int32) async throws -> Ycc_V1_WorkLoopInfo
    func stopWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo?
    /// Change how many sessions a running loop may work at once (1...16).
    /// Applies to that loop only, not the project's `[work_loop] parallel`.
    /// A non-empty `loopID` makes the daemon refuse the change once another
    /// loop has replaced that one.
    func resizeWorkLoop(project: String, loopID: String, parallel: Int32) async throws -> Ycc_V1_WorkLoopInfo?
    func getWorkLoop(project: String) async throws -> Ycc_V1_WorkLoopInfo?
    func getWorkLoopPreflight(project: String) async throws -> Ycc_V1_GetWorkLoopPreflightResponse?
    /// The start sheet's context: ready tasks, the work implementation, and
    /// the budget caps the loop will capture.
    func listBacklog(project: String) async throws -> [Ycc_V1_BacklogTaskSummary]
    func listModels() async throws -> Ycc_V1_ListModelsResponse
    func getBudget() async throws -> Ycc_V1_GetBudgetResponse
    func setWorkImplementation(_ implementation: String) async throws
}

/// What a ``WorkLoopSource`` without the start sheet's optional context throws.
struct WorkLoopSourceUnsupported: Error {}

extension WorkLoopSource {
    public func resizeWorkLoop(project: String, loopID: String, parallel: Int32) async throws -> Ycc_V1_WorkLoopInfo? {
        throw WorkLoopSourceUnsupported()
    }
    /// Sources without a preflight report nothing to warn about.
    public func getWorkLoopPreflight(project: String) async throws -> Ycc_V1_GetWorkLoopPreflightResponse? { nil }
    public func listBacklog(project: String) async throws -> [Ycc_V1_BacklogTaskSummary] {
        throw WorkLoopSourceUnsupported()
    }
    public func listModels() async throws -> Ycc_V1_ListModelsResponse { throw WorkLoopSourceUnsupported() }
    public func getBudget() async throws -> Ycc_V1_GetBudgetResponse { throw WorkLoopSourceUnsupported() }
    public func setWorkImplementation(_ implementation: String) async throws { throw WorkLoopSourceUnsupported() }
}

/// A work implementation choice: who writes the code in a work session.
public struct WorkImplementationOption: Sendable, Identifiable, Equatable {
    public let value: String
    public let label: String
    public let detail: String
    public var id: String { value }

    public static let all = [
        WorkImplementationOption(
            value: "direct", label: "Main agent",
            detail: "The main agent writes the code; separate agents review it."),
        WorkImplementationOption(
            value: "delegate", label: "Implementer",
            detail: "The main agent plans and checks; the implementer model writes the code."),
    ]
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
    /// A stopping loop starts no more sessions and a finished one none at
    /// all, so only a running or waiting loop takes a new session count.
    public var canResize: Bool { canStop }
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
    public var completionMessage: String?
    public private(set) var unauthorized = false
    /// In-review work a loop started now would route around; nil until loaded
    /// or when the daemon cannot tell.
    public private(set) var preflight: Ycc_V1_GetWorkLoopPreflightResponse?
    /// The slot count picked for the next start; nil keeps the project's
    /// configured count (Start sends 0). A successful start clears it.
    public var parallelChoice: Int?
    /// The start sheet's ready tasks, highest priority first; nil until
    /// loaded or when the backlog could not be read.
    public private(set) var readyTasks: [Ycc_V1_BacklogTaskSummary]?
    /// The daemon's work implementation; nil until loaded or when unknown.
    public private(set) var workImplementation: String?
    /// The implementation picked for the next start; nil keeps the daemon's.
    public var implementationChoice: String?
    /// The budget caps a loop started now captures; nil until loaded.
    public private(set) var budget: Ycc_V1_GetBudgetResponse?
    public private(set) var isLoadingStartOptions = false
    /// The session count a resize in flight asks for; nil otherwise.
    public private(set) var resizeTarget: Int?

    private let source: WorkLoopSource
    private let cache: AppDataCache?
    private let cacheGeneration: UInt64
    private let now: () -> Date
    /// When the current snapshot was last confirmed by the daemon (a load or an
    /// action response). A cache-seeded snapshot is unconfirmed (nil).
    private var confirmedAt: Date?
    /// Counts action responses applied, so a load that was already in flight
    /// when an action landed does not put back the snapshot it replaced.
    private var actionsApplied = 0

    /// The last snapshot a loop screen saw for a project. Wrapped so a cached
    /// "no loop yet" is distinguishable from "never loaded".
    struct Snapshot {
        var loop: Ycc_V1_WorkLoopInfo?
    }

    public init(
        source: WorkLoopSource, project: String, cache: AppDataCache? = nil,
        now: @escaping () -> Date = Date.init
    ) {
        self.source = source
        self.project = project
        self.cache = cache
        self.cacheGeneration = cache?.generation ?? 0
        self.now = now
        if let snapshot = cache?.value(.workLoop(project), as: Snapshot.self) {
            loop = snapshot.loop
            state = Self.state(for: snapshot.loop)
        }
    }

    /// Whether a daemon-confirmed snapshot is younger than `maxAge`.
    public func isFresh(maxAge: TimeInterval) -> Bool {
        guard let confirmedAt else { return false }
        return now().timeIntervalSince(confirmedAt) < maxAge
    }

    /// Refresh unless the snapshot was confirmed within `maxAge` — e.g. a Start
    /// or Stop response that already carried the new state. Avoids a redundant
    /// `GetWorkLoop` when polling restarts right after an action.
    public func refreshIfStale(maxAge: TimeInterval = 2) async {
        guard !isFresh(maxAge: maxAge) else { return }
        await refresh()
    }

    public var hasDigest: Bool {
        guard let loop else { return false }
        return !loop.completed.isEmpty || !loop.blocked.isEmpty
            || !loop.inReview.isEmpty || !loop.created.isEmpty
            || !loop.uncommittedInReview.isEmpty
    }

    /// The start confirmation's warning, or nil when there is nothing to warn about.
    public var preflightWarning: String? {
        guard let preflight else { return nil }
        return Self.uncommittedInReviewLine(
            tasks: preflight.uncommittedInReview, paths: Int(preflight.uncommittedPaths))
    }

    /// The project's `[work_loop] parallel` from the preflight; nil until it
    /// loads or from a daemon that does not report it.
    public var configuredParallel: Int? {
        guard let value = preflight?.configuredParallel, value > 0 else { return nil }
        return Int(value)
    }

    /// The slot count the next start runs: the pick, else the configured count.
    public var parallel: Int {
        get { parallelChoice ?? configuredParallel ?? 1 }
        set { parallelChoice = min(max(newValue, 1), Self.maxParallel) }
    }

    /// The most loop sessions StartWorkLoop accepts at once.
    public static let maxParallel = 16

    /// StartWorkLoopRequest.parallel: 0 (the project's configured count
    /// applies) unless a count other than the configured one was picked.
    public static func parallelToSend(choice: Int?, configured: Int?) -> Int32 {
        guard let choice, choice != configured else { return 0 }
        return Int32(min(max(choice, 1), maxParallel))
    }

    /// How the slots fill: one at a time, or staggered by task claims.
    public static func parallelExplanation(parallel: Int) -> String {
        parallel <= 1
            ? "One session at a time."
            : "Each session starts once the previous one has picked its task. They share one working tree (experimental)."
    }

    /// The implementation the next start uses: the pick, else the daemon's.
    public var implementation: String {
        get { implementationChoice ?? workImplementation ?? "direct" }
        set { implementationChoice = newValue == workImplementation ? nil : newValue }
    }

    /// Load everything the start sheet shows: the preflight, ready tasks, the
    /// work implementation, and the budget caps. Each part is optional; a
    /// failure (for example an older daemon without the RPC) leaves it out
    /// rather than blocking Start.
    public func loadStartOptions() async {
        isLoadingStartOptions = true
        defer { isLoadingStartOptions = false }
        let source = source
        let project = project
        async let preflight = try? source.getWorkLoopPreflight(project: project)
        async let backlog = try? source.listBacklog(project: project)
        async let models = try? source.listModels()
        async let budget = try? source.getBudget()
        self.preflight = await preflight
        readyTasks = await backlog.map(NewSessionModel.spawnableTasks)
        if let models = await models {
            workImplementation = models.workImplementation.isEmpty ? "direct" : models.workImplementation
            if implementationChoice == workImplementation { implementationChoice = nil }
        }
        self.budget = await budget
    }

    /// "Caps: $5.00 or 2M tokens per session · none per loop", or that no
    /// caps are set.
    public static func budgetLine(_ budget: Ycc_V1_GetBudgetResponse) -> String {
        func cap(tokens: Int64, cost: Double) -> String {
            let parts = [
                cost > 0 ? String(format: "$%.2f", cost) : nil,
                tokens > 0 ? "\(UsageModel.formatTokens(tokens)) tokens" : nil,
            ].compactMap { $0 }
            return parts.isEmpty ? "none" : parts.joined(separator: " or ")
        }
        let session = cap(tokens: budget.sessionTokens, cost: budget.sessionCost)
        let loop = cap(tokens: budget.loopTokens, cost: budget.loopCost)
        if session == "none" && loop == "none" { return "No budget caps set." }
        return "Caps: \(session) per session · \(loop) per loop."
    }

    public var currentSessionID: String {
        Self.currentSessionID(for: loop)
    }

    public var canStart: Bool { state.canStart && !isBusy }
    public var canStop: Bool { state.canStop && !isBusy }
    /// Whether the session-count control shows: a running or waiting loop
    /// that reports its count (loops recorded before slots report 0).
    public var showsResize: Bool { state.canResize && (loop?.parallel ?? 0) > 0 }
    public var canResize: Bool { showsResize && !isBusy }
    /// The session count the control shows: a resize in flight, else the loop's.
    public var displayedParallel: Int { resizeTarget ?? Int(loop?.parallel ?? 0) }
    public var shouldPoll: Bool { state.shouldPoll }

    /// Reload the persisted daemon snapshot. A load failure preserves the last
    /// good snapshot so transient network errors do not blank an active loop.
    public func refresh() async {
        guard !isBusy else { return }
        isLoading = true
        defer { isLoading = false }
        let actions = actionsApplied
        do {
            let snapshot = try await source.getWorkLoop(project: project)
            guard actions == actionsApplied else { return }
            apply(snapshot)
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
        completionMessage = nil
        do {
            // The implementation is daemon-wide and read when each session
            // starts, so it must land before the loop's first session.
            if let choice = implementationChoice, choice != workImplementation {
                try await source.setWorkImplementation(choice)
                workImplementation = choice
            }
            implementationChoice = nil
            let parallel = Self.parallelToSend(choice: parallelChoice, configured: configuredParallel)
            apply(try await source.startWorkLoop(project: project, parallel: parallel), fromStart: true, fromAction: true)
            parallelChoice = nil
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
            apply(try await source.stopWorkLoop(project: project), fromAction: true)
            actionError = nil
            errorMessage = nil
        } catch {
            handleAction(error)
        }
    }

    /// Change how many sessions the running loop may work at once and apply
    /// the returned snapshot. Raising it starts more sessions as tasks are
    /// ready; lowering it interrupts none, and no replacement starts until
    /// fewer than the new count are working.
    public func resize(to parallel: Int) async {
        guard canResize else { return }
        let target = min(max(parallel, 1), Self.maxParallel)
        guard target != Int(loop?.parallel ?? 0) else { return }
        isBusy = true
        resizeTarget = target
        defer {
            isBusy = false
            resizeTarget = nil
        }
        do {
            // The displayed loop's id, so a screen showing a cached or
            // replaced loop cannot resize its successor.
            let loopID = loop?.loopID ?? ""
            apply(
                try await source.resizeWorkLoop(project: project, loopID: loopID, parallel: Int32(target)),
                fromAction: true)
            actionError = nil
            errorMessage = nil
        } catch is WorkLoopSourceUnsupported {
            actionError = Self.resizeUnsupportedMessage
        } catch {
            handleAction(error)
        }
    }

    public nonisolated static let resizeUnsupportedMessage =
        "This daemon cannot change a running loop's session count. Update the daemon, or stop the loop and start it again."

    private func apply(_ snapshot: Ycc_V1_WorkLoopInfo?, fromStart: Bool = false, fromAction: Bool = false) {
        if fromAction { actionsApplied += 1 }
        let next = Self.state(for: snapshot)
        // Start can return an already-finished snapshot if setup failed quickly.
        // Otherwise surface the first active -> finished transition, not every
        // refresh or a historical completion when opening the screen.
        if next == .finished, fromStart || state.isActive,
           let outcome = snapshot?.outcome, !outcome.isEmpty {
            completionMessage = outcome
        }
        if loop != snapshot { loop = snapshot }
        if state != next { state = next }
        confirmedAt = now()
        cache?.store(Snapshot(loop: snapshot), for: .workLoop(project), ifGeneration: cacheGeneration)
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

    private static func isDone(_ task: Ycc_V1_WorkLoopDigestTask) -> Bool {
        task.status.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() == "done"
    }

    /// Every task that reached done during the loop: ones that existed at
    /// start, then ones it created.
    public static func completedTasks(for loop: Ycc_V1_WorkLoopInfo) -> [Ycc_V1_WorkLoopDigestTask] {
        loop.completed + loop.created.filter(isDone)
    }

    /// Tasks created during the loop that are not done.
    public static func createdOpenTasks(for loop: Ycc_V1_WorkLoopInfo) -> [Ycc_V1_WorkLoopDigestTask] {
        loop.created.filter { !isDone($0) }
    }

    /// Digest groups always appear in product order and empty groups disappear.
    public static func digestSections(for loop: Ycc_V1_WorkLoopInfo) -> [WorkLoopDigestSection] {
        [
            WorkLoopDigestSection(title: "Completed", systemImage: "checkmark.circle", rows: completedTasks(for: loop)),
            WorkLoopDigestSection(title: "Blocked", systemImage: "exclamationmark.octagon", rows: loop.blocked),
            WorkLoopDigestSection(title: "In review", systemImage: "eye", rows: loop.inReview),
            WorkLoopDigestSection(title: "Unfinished", systemImage: "arrow.clockwise.circle", rows: loop.unfinished),
            WorkLoopDigestSection(
                title: "Created, still open", systemImage: "plus.circle", rows: createdOpenTasks(for: loop)),
            WorkLoopDigestSection(
                title: uncommittedSectionTitle, systemImage: "tray.full", rows: loop.uncommittedInReview),
        ].filter { !$0.rows.isEmpty }
    }

    public static let uncommittedSectionTitle = "Uncommitted in review at start"

    /// "2 in-review tasks have uncommitted changes in 5 files" and the task ids,
    /// or nil when none: loop sessions leave that work to its owner and route
    /// around those files.
    public static func uncommittedInReviewLine(tasks: [Ycc_V1_WorkLoopDigestTask], paths: Int) -> String? {
        guard !tasks.isEmpty else { return nil }
        let subject = tasks.count == 1 ? "1 in-review task has" : "\(tasks.count) in-review tasks have"
        let files = paths == 1 ? "1 file" : "\(paths) files"
        let ids = tasks.prefix(8).map(\.id).joined(separator: ", ") + (tasks.count > 8 ? ", …" : "")
        return "\(subject) uncommitted changes in \(files) (\(ids))"
    }

    /// A compact status line for the backlog's work-loop banner.
    public static func bannerLine(for loop: Ycc_V1_WorkLoopInfo?) -> String {
        guard let loop else { return WorkLoopState.none.title }
        switch state(for: loop) {
        case .running:
            let slots = slotsLine(for: loop)
            return ["Running", slots, summaryLine(for: loop, withLive: slots.isEmpty)]
                .filter { !$0.isEmpty }.joined(separator: " · ")
        case .waiting:
            return [waitingLine(for: loop), slotsLine(for: loop)].filter { !$0.isEmpty }.joined(separator: " · ")
        case .stopping:
            return "Stopping…"
        case .finished:
            return loop.outcome.isEmpty ? "Finished · \(summaryLine(for: loop))" : loop.outcome
        case .none, .unknown:
            return state(for: loop).title
        }
    }

    /// "2/3 working, 1 waiting": the loop's slots in use out of its
    /// parallelism, and its sessions waiting on background jobs. After the
    /// count was lowered below the sessions still working, "3 working
    /// (target 2), 1 waiting". Empty for snapshots without slots.
    public static func slotsLine(for loop: Ycc_V1_WorkLoopInfo) -> String {
        guard loop.parallel > 0 else { return "" }
        let working = workingCount(for: loop)
        let waiting = loop.slots.filter { $0.state == "waiting" }.count
        let workingPart = working > Int(loop.parallel)
            ? "\(working) working (target \(loop.parallel))"
            : "\(working)/\(loop.parallel) working"
        return "\(workingPart), \(waiting) waiting"
    }

    /// The loop's slots whose sessions are working, the ones its session
    /// count limits.
    public static func workingCount(for loop: Ycc_V1_WorkLoopInfo) -> Int {
        loop.slots.filter { $0.state == "working" }.count
    }

    /// While a running or waiting loop has more sessions working than its
    /// lowered count: they finish undisturbed and no replacement starts until
    /// fewer than the count are working. Nil otherwise.
    public static func drainLine(for loop: Ycc_V1_WorkLoopInfo) -> String? {
        let target = Int(loop.parallel)
        let working = workingCount(for: loop)
        guard state(for: loop).canResize, target > 0, working > target else { return nil }
        let until = target == 1 ? "none is working" : "fewer than \(target) are working"
        return "Lowered to \(target): the \(working) working sessions finish undisturbed, and no new one starts until \(until)."
    }

    /// The session-count control's footer: the drain while it lasts, else
    /// what changing the count does.
    public static func resizeFooter(for loop: Ycc_V1_WorkLoopInfo) -> String {
        drainLine(for: loop)
            ?? "Applies to this loop only, not the project default. Lowering it interrupts no working session; it only holds back new ones."
    }

    /// Whether the loop may run more than one session at once: its single
    /// current session then does not describe it; the slots do.
    public static func isParallel(_ loop: Ycc_V1_WorkLoopInfo) -> Bool {
        loop.parallel > 1 || loop.slots.count > 1
    }

    /// "working 12m 3s", "waiting 3h 20m": a slot's state and how long it
    /// has been in it; the state alone when its start is unknown.
    public static func slotStateText(_ slot: Ycc_V1_WorkLoopSlot, now: Date = Date()) -> String {
        let state = slot.state.isEmpty ? "working" : slot.state
        guard let since = parseTimestamp(slot.since),
              let elapsed = durationText(secs: Int64(now.timeIntervalSince(since)))
        else { return state }
        return "\(state) \(elapsed)"
    }

    /// When a slot's session last did anything; nil when unknown.
    public static func lastActivityDate(for slot: Ycc_V1_WorkLoopSlot) -> Date? {
        parseTimestamp(slot.lastActivity)
    }

    /// The Stop confirmation: how many working sessions finish, and that
    /// sessions the loop let wait on their background jobs keep running, even
    /// ones a job has woken. Snapshots without slots (older daemons) keep the
    /// one-session wording.
    public static func stopConfirmMessage(for loop: Ycc_V1_WorkLoopInfo?) -> String {
        let providerWait = loop.map { WorkLoopState(state: $0.state) == .waiting && $0.waitKind != "sessions_waiting" } ?? false
        let noMore = "The daemon will not pick another backlog task."
        guard let loop, loop.parallel > 0 else {
            return providerWait
                ? "The loop is waiting for the provider; stopping ends the wait at once. \(noMore)"
                : "The current session finishes its work; the daemon will not pick another backlog task."
        }
        let detached = loop.slots.filter { $0.detached || $0.state == "waiting" }
        let working = loop.slots.filter { !$0.detached && $0.state == "working" }.count
        let woken = detached.filter { $0.state == "working" }.count
        var parts: [String] = []
        if providerWait {
            parts.append("The loop is waiting for the provider; stopping ends the wait at once.")
        }
        if working > 0 {
            parts.append(working == 1 ? "1 working session finishes its work." : "\(working) working sessions finish their work.")
        }
        let n = detached.count
        if n > 0 && woken == 0 {
            parts.append(n == 1
                ? "1 session waiting on its background jobs keeps running."
                : "\(n) sessions waiting on their background jobs keep running.")
        } else if n > 0 {
            parts.append("\(n == 1 ? "1 session" : "\(n) sessions") let wait on background jobs \(n == 1 ? "keeps" : "keep") running (\(woken) working again).")
        }
        parts.append(noMore)
        return parts.joined(separator: " ")
    }

    private static func plural(_ count: Int, _ one: String) -> String {
        "\(count) \(count == 1 ? one : one + "s")"
    }

    /// Loop sessions running now: an active loop's slots that have a session.
    /// A slot still starting has none yet, and a finished loop's slots are
    /// history (a restored loop keeps its interrupted waiters).
    private static func liveSessionCount(for loop: Ycc_V1_WorkLoopInfo) -> Int {
        guard state(for: loop).isActive else { return 0 }
        return loop.slots.filter { !$0.sessionID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }.count
    }

    /// "3 sessions", or with live ones "3 sessions finished, 2 live". Without
    /// `withLive` (the slots line beside it names them) live ones are left out.
    private static func sessionsPart(for loop: Ycc_V1_WorkLoopInfo, withLive: Bool) -> String {
        let finished = Int(loop.sessionsRun)
        let live = liveSessionCount(for: loop)
        if live == 0 { return plural(finished, "session") }
        if !withLive { return finished > 0 ? "\(plural(finished, "session")) finished" : "" }
        return finished > 0
            ? "\(plural(finished, "session")) finished, \(live) live"
            : plural(live, "live session")
    }

    /// A compact lifecycle/digest summary such as "3 sessions finished, 1 live ·
    /// 4 completed, 1 blocked, 2 created still open, 1 other commit". Completed
    /// counts every task that reached done, including ones the loop created.
    public static func summaryLine(for loop: Ycc_V1_WorkLoopInfo, withLive: Bool = true) -> String {
        let sessionPart = sessionsPart(for: loop, withLive: withLive)
        let counts: [(Int, String)] = [
            (completedTasks(for: loop).count, "completed"),
            (loop.blocked.count, "blocked"),
            (loop.inReview.count, "in review"),
            (loop.unfinished.count, "unfinished"),
            (createdOpenTasks(for: loop).count, "created still open"),
        ]
        var parts = counts.compactMap { count, label in
            count == 0 ? nil : "\(count) \(label)"
        }
        if !loop.manualLandings.isEmpty {
            parts.append(plural(loop.manualLandings.count, "other commit"))
        }
        return [sessionPart, parts.joined(separator: ", ")].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    public static func totalsLine(for loop: Ycc_V1_WorkLoopInfo) -> String {
        "\(UsageModel.formatTokens(loop.totalTokens)) tokens · \(formatCost(loop.totalCost, status: loop.costStatus))"
    }

    public static func totalsLine(tokens: Int64, cost: Double, priceStatus: String) -> String {
        "\(UsageModel.formatTokens(tokens)) tokens · \(formatCost(cost, status: priceStatus))"
    }

    /// A digest row's usage, or nil for a task no loop session worked (nothing to price).
    public static func digestTotalsLine(for task: Ycc_V1_WorkLoopDigestTask) -> String? {
        if task.attempts == 0 && task.tokens == 0 { return nil }
        return totalsLine(tokens: task.tokens, cost: task.cost, priceStatus: task.priceStatus)
    }

    /// The resource envelope captured when the loop started. Zero-valued limits
    /// are intentionally unbounded rather than omitted or unknown.
    public static func resourceEnvelopeLines(for loop: Ycc_V1_WorkLoopInfo) -> [String] {
        guard loop.resourceEnvelopeCaptured else {
            return ["Resource envelope unavailable for this pre-upgrade snapshot."]
        }
        let session = resourceLimitLine(
            scope: "Session",
            tokens: loop.sessionTokenLimit,
            cost: loop.sessionCostLimit,
            timeSecs: loop.sessionTimeLimitSecs)
        let wholeLoop = resourceLimitLine(
            scope: "Loop",
            tokens: loop.loopTokenLimit,
            cost: loop.loopCostLimit,
            timeSecs: loop.loopTimeLimitSecs)
        let pricing = loop.costLimitsPricedOnly
            ? "Cost caps and totals count priced models only; tokens count all models."
            : "Cost-limit pricing semantics unavailable."
        let attempts = "Attempts: no fixed limit; sessions without progress back off, and 12h without progress stops the loop."
        return [session, wholeLoop, pricing, attempts]
    }

    /// One line standing in for the envelope when the loop set no token, cost
    /// or wall-time limit at all; nil when any limit is set or the snapshot
    /// predates the envelope.
    public static func unboundedEnvelopeLine(for loop: Ycc_V1_WorkLoopInfo) -> String? {
        guard loop.resourceEnvelopeCaptured,
              loop.sessionTokenLimit <= 0, loop.sessionCostLimit <= 0, loop.sessionTimeLimitSecs <= 0,
              loop.loopTokenLimit <= 0, loop.loopCostLimit <= 0, loop.loopTimeLimitSecs <= 0
        else { return nil }
        return "No limits: tokens, cost and wall time unbounded"
    }

    /// The title of a task the loop knows about (its digest groups and live
    /// slots), or "" when the snapshot does not name it. Ids compare in
    /// canonical form, so a focus recorded as "7" finds digest row "0007".
    public static func taskTitle(_ taskID: String, in loop: Ycc_V1_WorkLoopInfo) -> String {
        let id = CoordinationFormat.normalizeTaskID(taskID)
        guard !id.isEmpty else { return "" }
        let same = { (other: String) in CoordinationFormat.normalizeTaskID(other) == id }
        let groups = [loop.completed, loop.blocked, loop.inReview, loop.unfinished, loop.created, loop.uncommittedInReview]
        for group in groups {
            if let task = group.first(where: { same($0.id) }), !task.title.isEmpty { return task.title }
        }
        return loop.slots.first { same($0.taskID) && !$0.taskTitle.isEmpty }?.taskTitle ?? ""
    }

    /// "attempt N" for a finished session whose task the loop attempted more
    /// than once; nil for a task's only attempt or a session without a task.
    public static func attemptLabel(for session: Ycc_V1_WorkLoopSession, in loop: Ycc_V1_WorkLoopInfo) -> String? {
        let id = CoordinationFormat.normalizeTaskID(session.focus)
        guard !id.isEmpty else { return nil }
        let attempts = loop.sessions.filter { CoordinationFormat.normalizeTaskID($0.focus) == id }
        guard attempts.count > 1 else { return nil }
        let number = attempts.firstIndex { $0.sessionID == session.sessionID }.map { $0 + 1 } ?? Int(session.attempt)
        return "attempt \(number)"
    }

    private static func resourceLimitLine(scope: String, tokens: Int64, cost: Double, timeSecs: Int64) -> String {
        let tokenText = tokens > 0 ? "\(UsageModel.formatTokens(tokens)) tokens" : "tokens unbounded"
        let costText = cost > 0 ? String(format: "$%.2f priced cost", cost) : "cost unbounded"
        let timeText = durationText(secs: timeSecs).map { "\($0) wall time" } ?? "wall time unbounded"
        return "\(scope): \(tokenText) · \(costText) · \(timeText)"
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
    /// "Waiting for provider (rate_limit) — resumes 03:15", the
    /// no-progress backoff, or a wait on loop sessions that wait on their
    /// background jobs.
    public static func waitingLine(for loop: Ycc_V1_WorkLoopInfo) -> String {
        let kind = loop.waitKind.trimmingCharacters(in: .whitespacesAndNewlines)
        var line = "Waiting for provider"
        if kind == "no_progress" {
            line = "Waiting: last session made no progress"
        } else if kind == "sessions_waiting" {
            line = "Waiting: loop sessions are waiting on background jobs"
        } else if !kind.isEmpty {
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

/// Identity is the instance, so a screen can present a sheet for one model.
extension WorkLoopModel: Identifiable {}
