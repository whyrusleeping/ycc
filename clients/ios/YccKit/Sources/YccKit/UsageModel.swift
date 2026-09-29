import Foundation
import Observation
import YccProto

/// The data source a ``UsageModel`` reads from. Abstracting it behind a protocol
/// lets the grouping / formatting logic be unit-tested headlessly with an
/// in-memory mock — no network, no simulator. ``YccClient`` is the production
/// conformer. (Mirrors the ``BacklogSource`` pattern.)
public protocol UsageSource: Sendable {
    /// Priced token-usage breakdown, grouped and filtered (`GetUsage`).
    func getUsage(project: String, groupBy: [String], since: String, until: String)
        async throws -> (rows: [Ycc_V1_UsageRow], total: Ycc_V1_UsageRow, workspace: String)
    /// Provider-side allowance for configured OAuth subscription accounts.
    func getSubscriptionUsage(refresh: Bool) async throws -> [Ycc_V1_SubscriptionUsageAccount]
    /// The configured spend-guard caps (`GetBudget`).
    func getBudget() async throws -> Ycc_V1_GetBudgetResponse
    /// List the daemon's registered projects (drives the project filter).
    func listProjects() async throws -> [Ycc_V1_ProjectInfo]
}

extension YccClient: UsageSource {}

/// The dimension a usage breakdown is grouped by. The daemon
/// accepts `task | model | session | agent | day`; single-select here keeps the
/// row label unambiguous. Kept as a single source of truth for the picker
/// choices, the wire value, and the per-row label selection.
public enum UsageGrouping: String, Sendable, CaseIterable, Identifiable {
    case task
    case model
    case session
    case agent
    case day

    public var id: String { rawValue }

    /// The value sent in `GetUsageRequest.group_by`.
    public var wireValue: String { rawValue }

    /// A human-facing label for the picker.
    public var title: String {
        switch self {
        case .task: return "Task"
        case .model: return "Model"
        case .session: return "Session"
        case .agent: return "Agent"
        case .day: return "Day"
        }
    }

    /// The label for a row under this grouping — the value of the grouped
    /// dimension, with a sensible placeholder when the daemon left it blank
    /// (e.g. usage with no task focus grouped by task).
    public func rowLabel(for row: Ycc_V1_UsageRow) -> String {
        let value: String
        switch self {
        case .task: value = row.task
        case .model: value = row.model
        case .session: value = row.session
        case .agent: value = row.agent
        case .day: value = row.day
        }
        return value.isEmpty ? "—" : value
    }
}

/// The pricing confidence of a usage row: `priced` (all models had
/// prices), `unpriced` (none did), or `partial` (some did). Unknown strings fall
/// back to ``priced`` so a row without an explicit status still renders a cost.
public enum PriceStatus: String, Sendable {
    case priced
    case unpriced
    case partial

    public init(status: String) {
        self = PriceStatus(rawValue: status.lowercased()) ?? .priced
    }

    /// A short badge label, or `nil` when fully priced (no annotation needed).
    public var badge: String? {
        switch self {
        case .priced: return nil
        case .unpriced: return "unpriced"
        case .partial: return "partial"
        }
    }
}

/// Drives the usage & budget views by loading ``GetUsage`` (grouped/filtered)
/// and ``GetBudget``,
/// holds the selected project, grouping, and optional since/until date filters,
/// and exposes formatting helpers so the token/cost rendering matches the TUI's
/// `ycc cost`. The data source is injected (``UsageSource``) so the grouping /
/// formatting logic is testable headlessly. `@MainActor` because it publishes
/// observable UI state.
@MainActor
@Observable
public final class UsageModel {
    /// Per-group rows from the last successful load.
    public private(set) var rows: [Ycc_V1_UsageRow] = []
    /// The totals row (summed across all groups) from the last successful load.
    public private(set) var total: Ycc_V1_UsageRow?
    /// The resolved workspace path the usage was computed over.
    public private(set) var workspace: String = ""
    /// The configured spend-guard caps from the last successful load.
    public private(set) var budget: Ycc_V1_GetBudgetResponse?
    /// Provider-side shared subscription allowance (separate from local usage).
    public private(set) var subscriptionAccounts: [Ycc_V1_SubscriptionUsageAccount] = []
    /// Registered projects; drives the project filter menu.
    public private(set) var projects: [Ycc_V1_ProjectInfo] = []

    /// The selected registered project. Empty means all projects (overall usage).
    /// Setting it does not auto-refresh — the view calls ``refresh()``.
    public var selectedProject: String = ""
    /// The grouping dimension. Setting it does not auto-refresh.
    public var grouping: UsageGrouping = .task
    /// Whether the since/until date filter is active. When `false` the dates are
    /// not sent (unbounded).
    public var filterByDate = false
    /// The inclusive start of the date filter (sent as `YYYY-MM-DD`).
    public var since = Date()
    /// The inclusive end of the date filter (sent as `YYYY-MM-DD`).
    public var until = Date()

    public private(set) var isLoading = false
    public private(set) var errorMessage: String?
    /// Set when a load failed with ``YccError/unauthorized``; the view observes
    /// this to route back to the connect screen via `AppModel.handleUnauthorized`.
    public private(set) var unauthorized = false

    /// What a revisited usage screen shows before its revalidation lands: the
    /// last rows/total/budget plus the query that produced them (so the pickers
    /// reopen on the same grouping and date filter).
    struct Snapshot {
        var rows: [Ycc_V1_UsageRow]
        var total: Ycc_V1_UsageRow?
        var workspace: String
        var budget: Ycc_V1_GetBudgetResponse?
        var grouping: UsageGrouping
        var filterByDate: Bool
        var since: Date
        var until: Date
    }

    private let source: UsageSource
    private let cache: AppDataCache?
    private let cacheGeneration: UInt64
    private var activeLoads = 0
    /// Bumped per usage request; only the newest request may publish, so an
    /// overlapping grouping/date change can never be overwritten by an older,
    /// slower response.
    private var usageGeneration: UInt64 = 0
    @ObservationIgnored private var usageTask: Task<Void, Never>?

    public init(source: UsageSource, selectedProject: String = "", cache: AppDataCache? = nil) {
        self.source = source
        self.cache = cache
        self.cacheGeneration = cache?.generation ?? 0
        self.selectedProject = selectedProject
        if let cachedProjects = cache?.projects { projects = cachedProjects }
        if let accounts = cache?.value(.subscriptionUsage, as: [Ycc_V1_SubscriptionUsageAccount].self) {
            subscriptionAccounts = accounts
        }
        if let snapshot = cache?.value(.usage(selectedProject), as: Snapshot.self) {
            rows = snapshot.rows
            total = snapshot.total
            workspace = snapshot.workspace
            budget = snapshot.budget
            grouping = snapshot.grouping
            filterByDate = snapshot.filterByDate
            since = snapshot.since
            until = snapshot.until
            rowsQuery = RowsQuery(
                project: selectedProject, grouping: snapshot.grouping,
                filterByDate: snapshot.filterByDate, since: snapshot.since, until: snapshot.until)
        }
    }

    /// The project picker is useful only when there is a real choice.
    public var showsProjectFilter: Bool { projects.count > 1 }

    /// Whether the last successful load produced any usage rows.
    public var hasUsage: Bool { !rows.isEmpty }

    /// Screen appearance: revalidate usage and budget (and projects only when
    /// no app-level list is cached). The provider-side subscription allowance
    /// is force-refreshed only on the first load of this connection; later
    /// visits reuse the cached accounts until the user pulls to refresh.
    public func load() async {
        let hasSubscriptionSnapshot =
            cache?.value(.subscriptionUsage, as: [Ycc_V1_SubscriptionUsageAccount].self) != nil
        await loadAll(refreshSubscription: !hasSubscriptionSnapshot)
    }

    /// Explicit (pull-to-)refresh: reload usage, budget, and — force-refreshed
    /// from the provider — the subscription allowance. Unauthorized bubbles up
    /// via ``unauthorized`` for the view to handle.
    public func refresh() async {
        await loadAll(refreshSubscription: true)
    }

    /// A grouping / date-filter / project change: reload only the usage rows.
    /// Budget and subscription allowance do not depend on these filters. A
    /// newer call cancels and supersedes any usage request still in flight.
    public func reloadUsage() async {
        usageTask?.cancel()
        usageGeneration &+= 1
        let generation = usageGeneration
        let project = selectedProject
        let grouping = grouping
        let (sinceValue, untilValue) = dateFilter
        let query = (filterByDate, since, until)
        let source = source
        let task = Task { @MainActor [weak self] in
            do {
                let result = try await source.getUsage(
                    project: project,
                    groupBy: [grouping.wireValue],
                    since: sinceValue,
                    until: untilValue)
                guard let self, generation == self.usageGeneration, !Task.isCancelled else { return }
                self.rows = result.rows
                self.total = result.total
                self.workspace = result.workspace
                self.errorMessage = nil
                self.rowsQuery = RowsQuery(
                    project: project, grouping: grouping,
                    filterByDate: query.0, since: query.1, until: query.2)
                self.storeCache()
            } catch {
                guard let self, generation == self.usageGeneration, !Task.isCancelled,
                      !(error is CancellationError) else { return }
                self.handleLoad(error)
            }
        }
        usageTask = task
        beginLoad()
        await task.value
        endLoad()
        if generation == usageGeneration { usageTask = nil }
    }

    private func loadAll(refreshSubscription: Bool) async {
        beginLoad()
        defer { endLoad() }
        let source = source
        let fetchProjects = cache?.projects == nil
        if let cachedProjects = cache?.projects, cachedProjects != projects {
            projects = cachedProjects
        }
        async let budgetCaps: Result<Ycc_V1_GetBudgetResponse, Error> = Self.capture {
            try await source.getBudget()
        }
        async let projectList: Result<[Ycc_V1_ProjectInfo]?, Error> = Self.capture {
            guard fetchProjects else { return nil }
            return try await source.listProjects()
        }
        async let accounts: Result<[Ycc_V1_SubscriptionUsageAccount]?, Error> = Self.capture {
            guard refreshSubscription else { return nil }
            return try await source.getSubscriptionUsage(refresh: true)
        }
        await reloadUsage()
        switch await budgetCaps {
        case .success(let loaded):
            budget = loaded
            // Stored under the query that produced the *rows* (a project switch
            // mid-load must not file one project's rows under another).
            storeCache()
        case .failure(let error):
            if errorMessage == nil || unauthorizedError(error) { handleLoad(error) }
        }
        switch await projectList {
        case .success(let loaded?):
            projects = loaded
            cache?.updateProjects(loaded, ifGeneration: cacheGeneration)
        case .success(nil):
            break
        case .failure(let error):
            if unauthorizedError(error) { unauthorized = true }
        }
        // Provider allowance is informational and served best-effort. A
        // telemetry failure must not hide local token usage or budget data.
        switch await accounts {
        case .success(let loaded?):
            subscriptionAccounts = loaded
            cache?.store(loaded, for: .subscriptionUsage, ifGeneration: cacheGeneration)
        case .success(nil):
            break
        case .failure(let error):
            if unauthorizedError(error) { unauthorized = true }
            // Otherwise preserve the last known account snapshot, if any.
        }
    }

    nonisolated private static func capture<T>(
        _ body: @Sendable () async throws -> T
    ) async -> Result<T, Error> {
        do { return .success(try await body()) } catch { return .failure(error) }
    }

    private func unauthorizedError(_ error: Error) -> Bool {
        if case YccError.unauthorized = error { return true }
        return false
    }

    private func beginLoad() {
        activeLoads += 1
        isLoading = true
    }

    private func endLoad() {
        activeLoads -= 1
        isLoading = activeLoads > 0
    }

    /// The query that produced the currently displayed ``rows``.
    private struct RowsQuery {
        var project: String
        var grouping: UsageGrouping
        var filterByDate: Bool
        var since: Date
        var until: Date
    }

    private var rowsQuery: RowsQuery?

    private func storeCache() {
        guard let query = rowsQuery else { return }
        let snapshot = Snapshot(
            rows: rows, total: total, workspace: workspace, budget: budget,
            grouping: query.grouping, filterByDate: query.filterByDate,
            since: query.since, until: query.until)
        cache?.store(snapshot, for: .usage(query.project), ifGeneration: cacheGeneration)
    }

    private func handleLoad(_ error: Error) {
        switch error {
        case YccError.unauthorized:
            unauthorized = true
        case let YccError.rpc(message), let YccError.notFound(message),
             let YccError.failedPrecondition(message):
            errorMessage = message
        default:
            errorMessage = error.localizedDescription
        }
    }

    /// The `since`/`until` wire values for the current filter state: empty
    /// strings (unbounded) unless ``filterByDate`` is on.
    public var dateFilter: (since: String, until: String) {
        guard filterByDate else { return ("", "") }
        return (Self.wireDate(since), Self.wireDate(until))
    }

    /// The label for a given row under the active grouping.
    public func label(for row: Ycc_V1_UsageRow) -> String {
        grouping.rowLabel(for: row)
    }

    // MARK: - Pure formatting (unit-tested)

    /// A shared `YYYY-MM-DD` formatter in UTC (matches the daemon's date keys).
    private static let wireFormatter: DateFormatter = {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = TimeZone(identifier: "UTC")
        f.dateFormat = "yyyy-MM-dd"
        return f
    }()

    /// Format a date as the `YYYY-MM-DD` value the daemon expects.
    public static func wireDate(_ date: Date) -> String {
        wireFormatter.string(from: date)
    }

    /// Render a token count compactly: "842", "12.3k", "1.2M" (mirrors the TUI's
    /// `fmtTokens`).
    public static func formatTokens(_ n: Int64) -> String {
        let value = abs(n)
        let sign = n < 0 ? "-" : ""
        switch value {
        case 1_000_000...:
            return "\(sign)\(String(format: "%.1fM", Double(value) / 1_000_000))"
        case 1_000...:
            return "\(sign)\(String(format: "%.1fk", Double(value) / 1_000))"
        default:
            return "\(n)"
        }
    }

    /// Render a row's cost cell, honouring its price status (mirrors the TUI's
    /// `costCellTUI`): "—" for unpriced, a trailing "*" for partial pricing.
    public static func formatCost(_ cost: Double, status: PriceStatus) -> String {
        switch status {
        case .unpriced:
            return "—"
        case .partial:
            return String(format: "$%.4f*", cost)
        case .priced:
            return String(format: "$%.4f", cost)
        }
    }

    /// Convenience: the formatted cost for a usage row.
    public static func formatCost(_ row: Ycc_V1_UsageRow) -> String {
        formatCost(row.cost, status: PriceStatus(status: row.priceStatus))
    }

    /// Format a budget cap that counts total tokens: "Unlimited" for 0.
    public static func formatTokenCap(_ tokens: Int64) -> String {
        tokens <= 0 ? "Unlimited" : formatTokens(tokens)
    }

    /// Format a budget cost cap in US dollars: "Unlimited" for 0.
    public static func formatCostCap(_ cost: Double) -> String {
        cost <= 0 ? "Unlimited" : String(format: "$%.2f", cost)
    }
}
