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
    /// The configured spend-guard caps (`GetBudget`).
    func getBudget() async throws -> Ycc_V1_GetBudgetResponse
    /// List the daemon's registered projects (drives the project filter).
    func listProjects() async throws -> [Ycc_V1_ProjectInfo]
}

extension YccClient: UsageSource {}

/// The dimension a usage breakdown is grouped by (spec §20.5). The daemon
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

/// The pricing confidence of a usage row (spec §20.5): `priced` (all models had
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

/// Drives the usage & budget views (docs/design/ios-client.md §6 phase 3 step 9,
/// spec §20.5/§20.6): loads ``GetUsage`` (grouped/filtered) and ``GetBudget``,
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
    /// Registered projects; drives the project filter menu.
    public private(set) var projects: [Ycc_V1_ProjectInfo] = []

    /// The selected project filter. `""` selects the daemon default workspace.
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

    private let source: UsageSource

    public init(source: UsageSource, selectedProject: String = "") {
        self.source = source
        self.selectedProject = selectedProject
    }

    /// The project filter is only meaningful with more than one project.
    public var showsProjectFilter: Bool { projects.count > 1 }

    /// Whether the last successful load produced any usage rows.
    public var hasUsage: Bool { !rows.isEmpty }

    /// (Re)load the usage breakdown, budget caps, and project list for the
    /// selected project / grouping / date filter. Unauthorized bubbles up via
    /// ``unauthorized`` for the view to handle.
    public func refresh() async {
        isLoading = true
        defer { isLoading = false }
        let (sinceValue, untilValue) = dateFilter
        do {
            async let usage = source.getUsage(
                project: selectedProject,
                groupBy: [grouping.wireValue],
                since: sinceValue,
                until: untilValue)
            async let budgetCaps = source.getBudget()
            async let projectList = source.listProjects()
            let ((loadedRows, loadedTotal, loadedWorkspace), loadedBudget, loadedProjects)
                = try await (usage, budgetCaps, projectList)
            rows = loadedRows
            total = loadedTotal
            workspace = loadedWorkspace
            budget = loadedBudget
            projects = loadedProjects
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch let YccError.rpc(message) {
            errorMessage = message
        } catch let YccError.notFound(message) {
            errorMessage = message
        } catch let YccError.failedPrecondition(message) {
            errorMessage = message
        } catch {
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
