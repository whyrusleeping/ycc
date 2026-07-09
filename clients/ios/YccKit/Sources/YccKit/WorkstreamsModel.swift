import Foundation
import Observation
import YccProto

/// The data source a ``WorkstreamsModel`` reads from and drives. Abstracting it
/// behind a protocol lets the listing / merge-gate logic be unit-tested
/// headlessly with an in-memory mock — no network, no simulator. ``YccClient``
/// is the production conformer. (Mirrors the ``BacklogSource`` pattern.)
public protocol WorkstreamsSource: Sendable {
    /// List a project's workstreams (empty project => all projects).
    func listWorkstreams(project: String) async throws -> [Ycc_V1_WorkstreamInfo]
    /// List the daemon's registered projects (drives the project filter).
    func listProjects() async throws -> [Ycc_V1_ProjectInfo]
    /// Trial-merge without mutating: (clean, conflicts, diff).
    func previewMerge(workstreamId: String) async throws -> (clean: Bool, conflicts: [String], diff: String)
    /// Integrate the branch back to base honouring the accept gate.
    func mergeWorkstream(workstreamId: String, accept: Bool) async throws
        -> (merged: Bool, commit: String, needsAccept: Bool, diff: String, conflicts: [String])
    /// Abandon a workstream without merging.
    func discardWorkstream(workstreamId: String) async throws
}

extension YccClient: WorkstreamsSource {}

/// A workstream's lifecycle status (proto `WorkstreamInfo.status`: `active |
/// merged | discarded | stale`), parsed from the daemon's free-form string.
/// Kept here so the badge colour/label mapping is a single, unit-testable source
/// of truth. Unknown strings fall back to ``unknown`` rather than crashing.
public enum WorkstreamStatus: String, Sendable, CaseIterable, Equatable {
    case active
    case merged
    case discarded
    case stale
    case unknown

    public init(status: String) {
        self = WorkstreamStatus(rawValue: status.lowercased()) ?? .unknown
    }

    /// A human-facing badge label.
    public var title: String {
        switch self {
        case .active: return "Active"
        case .merged: return "Merged"
        case .discarded: return "Discarded"
        case .stale: return "Stale"
        case .unknown: return "Unknown"
        }
    }

    /// Whether merge/discard actions apply — only a live (`active`/`stale`)
    /// workstream can be merged or discarded.
    public var isActionable: Bool {
        self == .active || self == .stale
    }
}

/// The outcome of a ``WorkstreamsModel/preview(_:)`` call: a clean trial merge
/// with its integrated diff, or the conflicted paths.
public enum PreviewOutcome: Sendable, Equatable {
    case clean(diff: String)
    case conflicts([String])
}

/// The outcome of a ``WorkstreamsModel/merge(_:accept:)`` call, mirroring the
/// proto's accept-gate: `merged` integrated the branch; `needsAccept` is a clean
/// but review-gated merge whose diff must be shown before re-calling with
/// `accept: true`; `conflicts` lists the conflicted paths (base untouched).
public enum MergeOutcome: Sendable, Equatable {
    case merged(commit: String)
    case needsAccept(diff: String)
    case conflicts([String])
}

/// Drives the workstreams pane (docs/design/ios-client.md §6 phase 3 step 10,
/// spec §14.1, design/parallel-workstreams.md §6): lists ``ListWorkstreams`` with
/// per-stream status, and runs the review-gated ``PreviewMerge`` /
/// ``MergeWorkstream`` / ``DiscardWorkstream`` actions. The data source is
/// injected (``WorkstreamsSource``) so the listing / gate logic is testable
/// headlessly. `@MainActor` because it publishes observable UI state.
@MainActor
@Observable
public final class WorkstreamsModel {
    /// Workstreams from the last successful load, in daemon order.
    public private(set) var workstreams: [Ycc_V1_WorkstreamInfo] = []
    /// Registered projects; drives the project filter menu.
    public private(set) var projects: [Ycc_V1_ProjectInfo] = []
    /// The selected project filter. `""` lists all workstreams across projects.
    /// Setting it does not auto-refresh — the view calls ``refresh()``.
    public var selectedProject: String = ""

    public private(set) var isLoading = false
    public private(set) var errorMessage: String?
    /// Set when a load/action failed with ``YccError/unauthorized``; the view
    /// observes this to route back to the connect screen via
    /// `AppModel.handleUnauthorized`.
    public private(set) var unauthorized = false

    /// The id of the workstream with an action currently in flight, so the view
    /// can disable that row's buttons. `nil` when idle.
    public private(set) var busyWorkstreamID: String?
    /// A destructive/merge action failure message, surfaced inline/as an alert.
    public var actionError: String?

    private let source: WorkstreamsSource

    public init(source: WorkstreamsSource, selectedProject: String = "") {
        self.source = source
        self.selectedProject = selectedProject
    }

    /// The project filter is only meaningful with more than one project.
    public var showsProjectFilter: Bool { projects.count > 1 }

    /// Whether the last successful load produced any workstreams.
    public var hasWorkstreams: Bool { !workstreams.isEmpty }

    /// (Re)load the workstreams for the selected project and the project list.
    /// Unauthorized bubbles up via ``unauthorized`` for the view to handle.
    public func refresh() async {
        isLoading = true
        defer { isLoading = false }
        do {
            async let list = source.listWorkstreams(project: selectedProject)
            async let projectList = source.listProjects()
            let (loaded, loadedProjects) = try await (list, projectList)
            workstreams = loaded
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

    /// Trial-merge a workstream WITHOUT mutating anything (`PreviewMerge`).
    /// Returns the outcome for the view to present (a diff sheet or the
    /// conflicted paths), or `nil` on failure (see ``actionError`` /
    /// ``unauthorized``).
    public func preview(_ workstream: Ycc_V1_WorkstreamInfo) async -> PreviewOutcome? {
        guard busyWorkstreamID == nil else { return nil }
        busyWorkstreamID = workstream.id
        defer { busyWorkstreamID = nil }
        do {
            let result = try await source.previewMerge(workstreamId: workstream.id)
            actionError = nil
            return result.clean ? .clean(diff: result.diff) : .conflicts(result.conflicts)
        } catch {
            handle(error)
            return nil
        }
    }

    /// Merge a workstream honouring the accept gate (`MergeWorkstream`). Pass
    /// `accept: false` for the first pass (a clean review-gated merge returns
    /// ``MergeOutcome/needsAccept(diff:)`` without mutating); re-call with
    /// `accept: true` after the user reviews the diff. On ``MergeOutcome/merged``
    /// the list is refreshed so the row's status updates. Returns `nil` on
    /// failure.
    public func merge(_ workstream: Ycc_V1_WorkstreamInfo, accept: Bool) async -> MergeOutcome? {
        guard busyWorkstreamID == nil else { return nil }
        busyWorkstreamID = workstream.id
        defer { busyWorkstreamID = nil }
        do {
            let result = try await source.mergeWorkstream(workstreamId: workstream.id, accept: accept)
            actionError = nil
            if result.merged {
                await refreshAfterAction()
                return .merged(commit: result.commit)
            }
            if result.needsAccept {
                return .needsAccept(diff: result.diff)
            }
            return .conflicts(result.conflicts)
        } catch {
            handle(error)
            return nil
        }
    }

    /// Discard a workstream (`DiscardWorkstream`), then refresh so the row's
    /// status flips to discarded / it drops from the list. Returns `true` on
    /// success.
    @discardableResult
    public func discard(_ workstream: Ycc_V1_WorkstreamInfo) async -> Bool {
        guard busyWorkstreamID == nil else { return false }
        busyWorkstreamID = workstream.id
        defer { busyWorkstreamID = nil }
        do {
            try await source.discardWorkstream(workstreamId: workstream.id)
            actionError = nil
            await refreshAfterAction()
            return true
        } catch {
            handle(error)
            return false
        }
    }

    /// Refresh the list after a mutating action without toggling the row's busy
    /// flag off first (it is cleared by the caller's `defer`). Errors here are
    /// swallowed to the list-level `errorMessage`, not the action alert.
    private func refreshAfterAction() async {
        do {
            workstreams = try await source.listWorkstreams(project: selectedProject)
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            errorMessage = (error as? YccError)?.displayMessage ?? error.localizedDescription
        }
    }

    private func handle(_ error: Error) {
        switch error {
        case YccError.unauthorized:
            unauthorized = true
        default:
            actionError = (error as? YccError)?.displayMessage ?? error.localizedDescription
        }
    }

    // MARK: - Pure helpers (unit-tested)

    /// The lifecycle status for a workstream row.
    public static func status(for workstream: Ycc_V1_WorkstreamInfo) -> WorkstreamStatus {
        WorkstreamStatus(status: workstream.status)
    }

    /// A short one-line commit summary for a row, e.g. "3 commits" / "1 commit"
    /// / "no commits".
    public static func commitSummary(for workstream: Ycc_V1_WorkstreamInfo) -> String {
        switch workstream.commitCount {
        case ..<1: return "no commits"
        case 1: return "1 commit"
        default: return "\(workstream.commitCount) commits"
        }
    }
}
