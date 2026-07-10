import Foundation
import Observation
import YccProto

/// The data source a ``SessionListModel`` reads from. Abstracting it behind a
/// protocol lets the sorting / sectioning / filtering logic be unit-tested
/// headlessly with an in-memory mock — no network, no simulator. ``YccClient``
/// is the production conformer.
public protocol SessionListSource: Sendable {
    /// List session history for a project (empty => daemon default workspace).
    func listSessionHistory(project: String) async throws -> [Ycc_V1_SessionSummary]
    /// List the daemon's registered projects (drives the project filter).
    func listProjects() async throws -> [Ycc_V1_ProjectInfo]
}

extension YccClient: SessionListSource {}

/// The canonical status of a session, parsed from the daemon's free-form
/// `status` string (`running` | `idle` | `error` | `paused` | `stopped`). The
/// view maps each case to a colour + label; kept here so the mapping is
/// unit-testable and forward-compatible (unknown strings fall back to
/// ``unknown`` rather than crashing).
public enum SessionStatusKind: String, Sendable, CaseIterable {
    case running
    case idle
    case error
    case paused
    case stopped
    case unknown

    public init(status: String) {
        self = SessionStatusKind(rawValue: status.lowercased()) ?? .unknown
    }
}

/// A grouped list of sessions for display. Needs-answer sessions are pinned to
/// the top in their own section; the rest follow most-recent-first.
public struct SessionSection: Identifiable, Sendable {
    public enum Kind: String, Sendable {
        /// Live sessions blocked on an unanswered question — the loud, pinned
        /// section a phone client exists to surface.
        case needsAnswer
        /// Everything else, most-recent-first.
        case all
    }

    public let kind: Kind
    /// A section header title, or `nil` for the ungrouped remainder when there
    /// is no needs-answer section to distinguish it from.
    public let title: String?
    public let sessions: [Ycc_V1_SessionSummary]

    public var id: String { kind.rawValue }
}

/// Drives the session-list home screen: loads ``ListSessionHistory`` +
/// ``ListProjects``, holds the selected project filter, and exposes the sorted /
/// sectioned view of sessions. The data source is injected
/// (``SessionListSource``) so the sorting / filtering logic is testable
/// headlessly. `@MainActor` because it publishes observable UI state.
@MainActor
@Observable
public final class SessionListModel {
    /// Raw sessions from the last successful load (unsorted; view reads
    /// ``sections``).
    public private(set) var sessions: [Ycc_V1_SessionSummary] = []
    /// Registered projects; drives the project filter menu.
    public private(set) var projects: [Ycc_V1_ProjectInfo] = []
    /// The selected project filter. `""` selects the daemon default workspace.
    /// Setting it does not auto-refresh — the view calls ``refresh()``.
    public var selectedProject: String = ""

    public private(set) var isLoading = false
    public private(set) var errorMessage: String?
    /// Set when a load failed with ``YccError/unauthorized``; the view observes
    /// this to route back to the connect screen via `AppModel.handleUnauthorized`.
    public private(set) var unauthorized = false

    private let source: SessionListSource

    public init(source: SessionListSource, selectedProject: String = "") {
        self.source = source
        self.selectedProject = selectedProject
    }

    /// The project filter is meaningful whenever any project is registered:
    /// the picker always offers the implicit "Default" workspace (`""`) too,
    /// so even a single registered project gives two choices.
    public var showsProjectFilter: Bool { !projects.isEmpty }

    /// Sessions grouped into a pinned needs-answer section (when present) plus
    /// the recency-ordered remainder.
    public var sections: [SessionSection] { Self.sections(from: sessions) }

    /// (Re)load session history for the selected project and the project list.
    /// Unauthorized bubbles up via ``unauthorized`` for the view to handle.
    public func refresh() async {
        isLoading = true
        defer { isLoading = false }
        do {
            async let history = source.listSessionHistory(project: selectedProject)
            async let projectList = source.listProjects()
            let (loaded, loadedProjects) = try await (history, projectList)
            sessions = loaded
            projects = loadedProjects
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch let YccError.rpc(message) {
            errorMessage = message
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    // MARK: - Pure logic (unit-tested)

    /// Group + sort sessions: needs-answer rows (live && waitingInput) pinned to
    /// a top section, the remainder most-recent-first. Both sections are sorted
    /// by `lastActivity` (RFC3339) descending, falling back to `startedAt` then
    /// a stable original order when timestamps are missing/unparseable.
    public static func sections(from sessions: [Ycc_V1_SessionSummary]) -> [SessionSection] {
        // Stable partition preserving original order within each group so the
        // recency sort (which is stable) has a deterministic base.
        var needsAnswer: [Ycc_V1_SessionSummary] = []
        var rest: [Ycc_V1_SessionSummary] = []
        for session in sessions {
            if session.live && session.waitingInput {
                needsAnswer.append(session)
            } else {
                rest.append(session)
            }
        }

        var out: [SessionSection] = []
        if !needsAnswer.isEmpty {
            out.append(SessionSection(
                kind: .needsAnswer,
                title: "Needs answer",
                sessions: sortedByRecency(needsAnswer)))
        }
        if !rest.isEmpty {
            out.append(SessionSection(
                kind: .all,
                // Only label the remainder when there's a needs-answer section
                // above it to distinguish from.
                title: needsAnswer.isEmpty ? nil : "All sessions",
                sessions: sortedByRecency(rest)))
        }
        return out
    }

    /// Most-recent-first by `lastActivity` (fallback `startedAt`). Uses a stable
    /// sort so equal / unparseable timestamps keep their original relative order.
    static func sortedByRecency(_ sessions: [Ycc_V1_SessionSummary]) -> [Ycc_V1_SessionSummary] {
        enumeratedStableSort(sessions) { a, b in
            let da = recencyDate(a)
            let db = recencyDate(b)
            switch (da, db) {
            case let (x?, y?):
                return x > y
            case (_?, nil):
                return true   // rows with a date sort before rows without
            case (nil, _?):
                return false
            case (nil, nil):
                return false  // keep original order (stable)
            }
        }
    }

    /// The date to sort a session by: `lastActivity`, falling back to
    /// `startedAt`. Returns `nil` when neither parses.
    public static func recencyDate(_ session: Ycc_V1_SessionSummary) -> Date? {
        parseTimestamp(session.lastActivity) ?? parseTimestamp(session.startedAt)
    }

    /// Parse an RFC3339 / ISO8601 timestamp. Daemon timestamps may carry
    /// fractional seconds and a numeric offset, so try with fractional seconds
    /// first, then without. Empty / unparseable input returns `nil`.
    static func parseTimestamp(_ value: String) -> Date? {
        if value.isEmpty { return nil }
        return isoWithFraction.date(from: value) ?? isoPlain.date(from: value)
    }

    private static let isoWithFraction: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    private static let isoPlain: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()

    /// A row's display title: the derived title, falling back to
    /// `mode + short session id` when empty so no row is blank.
    public static func displayTitle(for session: Ycc_V1_SessionSummary) -> String {
        let title = session.title.trimmingCharacters(in: .whitespacesAndNewlines)
        if !title.isEmpty { return title }
        let shortID = String(session.sessionID.prefix(8))
        let mode = session.mode.isEmpty ? "session" : session.mode
        return shortID.isEmpty ? mode : "\(mode) · \(shortID)"
    }

    /// A stable sort: Swift's `sort(by:)` is not guaranteed stable, so decorate
    /// with the original index and break ties on it.
    private static func enumeratedStableSort(
        _ items: [Ycc_V1_SessionSummary],
        by areInIncreasingOrder: (Ycc_V1_SessionSummary, Ycc_V1_SessionSummary) -> Bool
    ) -> [Ycc_V1_SessionSummary] {
        items.enumerated()
            .sorted { lhs, rhs in
                if areInIncreasingOrder(lhs.element, rhs.element) { return true }
                if areInIncreasingOrder(rhs.element, lhs.element) { return false }
                return lhs.offset < rhs.offset
            }
            .map(\.element)
    }
}
