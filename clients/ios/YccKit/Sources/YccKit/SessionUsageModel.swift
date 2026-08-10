import Foundation
import Observation
import YccProto

/// The narrow data source a ``SessionUsageModel`` reads from — just the priced
/// usage breakdown RPC. Abstracted behind a protocol (like ``UsageSource``) so
/// the filtering / totalling logic is unit-testable headlessly; ``YccClient``
/// is the production conformer.
public protocol SessionUsageSource: Sendable {
    /// Priced token-usage breakdown, grouped and filtered (`GetUsage`).
    func getUsage(project: String, groupBy: [String], since: String, until: String)
        async throws -> (rows: [Ycc_V1_UsageRow], total: Ycc_V1_UsageRow, workspace: String)
}

extension YccClient: SessionUsageSource {}

/// Drives the per-session usage sheet: "what has *this* session spent so far",
/// the iOS counterpart of the TUI's Σ status-bar readout.
///
/// The daemon's `GetUsageRequest` has no session filter, but it does support
/// multi-dimension grouping — so this model asks for
/// `group_by: ["session", "model"]` and keeps only the rows for its session id,
/// yielding a priced per-model breakdown scoped to exactly one session. Usage
/// events are persisted as they land, so a refresh mid-session is current up to
/// the last completed model turn. `@MainActor` because it publishes observable
/// UI state.
@MainActor
@Observable
public final class SessionUsageModel {
    /// Per-model rows for this session, in the daemon's order (tokens desc).
    public private(set) var rows: [Ycc_V1_UsageRow] = []
    /// The session total summed over ``rows`` (nil when there is no usage yet).
    public private(set) var total: Ycc_V1_UsageRow?

    public private(set) var isLoading = false
    public private(set) var errorMessage: String?
    /// Set when a load failed with ``YccError/unauthorized``; the view observes
    /// this to route back to the connect screen via `AppModel.handleUnauthorized`.
    public private(set) var unauthorized = false

    private let source: SessionUsageSource
    private let project: String
    private let sessionID: String

    public init(source: SessionUsageSource, project: String, sessionID: String) {
        self.source = source
        self.project = project
        self.sessionID = sessionID
    }

    /// Whether the last successful load found any usage for this session.
    public var hasUsage: Bool { !rows.isEmpty }

    /// (Re)load the session's per-model breakdown. Unauthorized bubbles up via
    /// ``unauthorized`` for the view to handle.
    public func refresh() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let (allRows, _, _) = try await source.getUsage(
                project: project,
                groupBy: ["session", "model"],
                since: "",
                until: "")
            let breakdown = Self.sessionBreakdown(rows: allRows, sessionID: sessionID)
            rows = breakdown.rows
            total = breakdown.total
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

    // MARK: - Pure aggregation (unit-tested)

    /// Filter a `["session", "model"]`-grouped breakdown down to one session and
    /// sum a total row. Returns `total: nil` when the session has no usage.
    ///
    /// The total's `price_status` merges the kept rows' statuses the way the
    /// daemon does across models (internal/usage): all priced → `priced`, all
    /// unpriced → `unpriced`, any mix (or an explicitly partial row) →
    /// `partial`. Costs sum directly — the daemon already prices each row (an
    /// unpriced row carries cost 0, never an invented number).
    public static func sessionBreakdown(
        rows: [Ycc_V1_UsageRow], sessionID: String
    ) -> (rows: [Ycc_V1_UsageRow], total: Ycc_V1_UsageRow?) {
        let kept = rows.filter { $0.session == sessionID }
        guard !kept.isEmpty else { return ([], nil) }

        var total = Ycc_V1_UsageRow()
        total.session = sessionID
        var sawPriced = false
        var sawUnpriced = false
        var sawPartial = false
        for row in kept {
            total.input += row.input
            total.output += row.output
            total.cacheRead += row.cacheRead
            total.cacheWrite += row.cacheWrite
            total.total += row.total
            total.cost += row.cost
            switch PriceStatus(status: row.priceStatus) {
            case .priced: sawPriced = true
            case .unpriced: sawUnpriced = true
            case .partial: sawPartial = true
            }
        }
        if sawPartial || (sawPriced && sawUnpriced) {
            total.priceStatus = PriceStatus.partial.rawValue
        } else if sawUnpriced {
            total.priceStatus = PriceStatus.unpriced.rawValue
        } else {
            total.priceStatus = PriceStatus.priced.rawValue
        }
        return (kept, total)
    }
}
