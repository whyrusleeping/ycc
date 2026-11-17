import Foundation
import Observation
import YccProto

/// The `ListCommits` RPC the History screen pages through.
public protocol CommitHistorySource: Sendable {
    func listCommits(
        project: String, branch: String, limit: Int32, cursor: String
    ) async throws -> Ycc_V1_ListCommitsResponse
}

extension YccClient: CommitHistorySource {
    /// One page of a local branch's git log, newest first. An empty `branch` is
    /// the checked-out HEAD; `cursor` is the previous page's `nextCursor` for the
    /// same branch (it pins the first page's tip).
    public func listCommits(
        project: String, branch: String, limit: Int32, cursor: String
    ) async throws -> Ycc_V1_ListCommitsResponse {
        var request = Ycc_V1_ListCommitsRequest()
        request.project = project
        request.branch = branch
        request.limit = limit
        request.cursor = cursor
        // `git log` plus per-commit stats can outlast an ordinary call.
        switch await generatedBulk.listCommits(request: request).result {
        case .success(let message): return message
        case .failure(let error): throw Self.map(error)
        }
    }
}

// MARK: - Presentation

/// Labels for commit rows and the branch picker, worded like the web client's.
@MainActor
public enum CommitFormat {
    public static func shortSha(_ sha: String) -> String { String(sha.prefix(7)) }

    public static func isMerge(_ commit: Ycc_V1_CommitSummary) -> Bool { commit.parents.count > 1 }

    /// The row's time: when it was authored, else committed.
    public static func date(_ commit: Ycc_V1_CommitSummary) -> Date? {
        SessionListModel.parseTimestamp(commit.authoredAt)
            ?? SessionListModel.parseTimestamp(commit.committedAt)
    }

    public struct Stats: Equatable, Sendable {
        public let additions: String
        public let deletions: String
        public let files: String
    }

    /// "+12", "−3", "4 files"; nil when git reported none (an empty or merge commit).
    public static func stats(_ commit: Ycc_V1_CommitSummary) -> Stats? {
        if commit.filesChanged == 0 && commit.additions == 0 && commit.deletions == 0 { return nil }
        return Stats(
            additions: "+\(commit.additions.formatted())",
            deletions: "\u{2212}\(commit.deletions.formatted())",
            files: "\(commit.filesChanged) \(commit.filesChanged == 1 ? "file" : "files")")
    }

    /// The picker's two groups: plain branches (the checked-out one first, then
    /// the daemon's most-recent-tip order) and in-flight workstream branches. A
    /// checked-out workstream branch stays among the plain ones.
    public static func groupBranches(
        _ branches: [Ycc_V1_BranchRef]
    ) -> (local: [Ycc_V1_BranchRef], workstreams: [Ycc_V1_BranchRef]) {
        var current: [Ycc_V1_BranchRef] = []
        var local: [Ycc_V1_BranchRef] = []
        var workstreams: [Ycc_V1_BranchRef] = []
        for branch in branches {
            if branch.current {
                current.append(branch)
            } else if branch.workstreamID.isEmpty {
                local.append(branch)
            } else {
                workstreams.append(branch)
            }
        }
        return (current + local, workstreams)
    }

    /// A picker entry's subtitle: "current", "workstream · 0559", or "".
    public static func branchNote(_ branch: Ycc_V1_BranchRef) -> String {
        var notes: [String] = []
        if branch.current { notes.append("current") }
        if !branch.workstreamID.isEmpty {
            notes.append(branch.taskID.isEmpty ? "workstream" : "workstream · \(branch.taskID)")
        }
        return notes.joined(separator: " · ")
    }
}

// MARK: - Model

/// A project's git history for the History screen: one local branch's
/// commits, newest first, paged with `ListCommits`, plus the branch list for
/// the picker. An empty ``branch`` is the checked-out HEAD.
@MainActor
@Observable
public final class CommitHistoryModel {
    /// Commits per `ListCommits` page.
    public static let pageSize: Int32 = 50

    public let project: String
    /// The requested branch; "" is the checked-out HEAD.
    public private(set) var branch = ""
    /// Every loaded page of ``branch``, newest first, each sha once.
    public private(set) var commits: [Ycc_V1_CommitSummary] = []
    /// Local branches from the latest first page (current first, then by recent tip).
    public private(set) var branches: [Ycc_V1_BranchRef] = []
    /// The first page of ``branch`` has loaded.
    public private(set) var hasLoaded = false
    public private(set) var isLoading = false
    public private(set) var isLoadingMore = false
    /// Why the latest first-page load failed; loaded rows stay.
    public private(set) var errorMessage: String?
    /// Why the latest next-page load failed; scrolling no longer retries it.
    public private(set) var loadMoreError: String?
    /// The project is not a git repository.
    public private(set) var notGit = false
    public private(set) var unauthorized = false

    private let source: CommitHistorySource
    private var nextCursor = ""
    /// Bumped by every first-page load, so a response for an earlier branch
    /// (or a page from before a refresh) is dropped.
    @ObservationIgnored private var generation = 0

    public init(project: String, source: CommitHistorySource) {
        self.project = project
        self.source = source
    }

    public var hasMore: Bool { !nextCursor.isEmpty }

    /// The picker's selection: the requested branch, else the checked-out one
    /// ("" for a detached HEAD).
    public var selectedBranch: String {
        branch.isEmpty ? (branches.first(where: \.current)?.name ?? "") : branch
    }

    public var selectedRef: Ycc_V1_BranchRef? {
        let name = selectedBranch
        return branches.first { $0.name == name }
    }

    /// Reload the first page of ``branch``, dropping older pages.
    public func refresh() async {
        generation += 1
        let current = generation
        isLoading = true
        isLoadingMore = false
        loadMoreError = nil
        defer { if current == generation { isLoading = false } }
        do {
            let page = try await source.listCommits(
                project: project, branch: branch, limit: Self.pageSize, cursor: "")
            guard current == generation else { return }
            commits = Self.unique(page.commits, after: [])
            if page.branches != branches { branches = page.branches }
            nextCursor = page.nextCursor
            hasLoaded = true
            notGit = false
            errorMessage = nil
        } catch YccError.unauthorized {
            unauthorized = true
        } catch YccError.failedPrecondition(let message) {
            guard current == generation else { return }
            notGit = true
            errorMessage = message
        } catch {
            guard current == generation else { return }
            errorMessage = Self.message(error)
        }
    }

    /// Show another branch. The checked-out branch is requested as HEAD (""),
    /// so the default view follows a checkout.
    public func selectBranch(_ name: String) async {
        let requested = branches.first(where: { $0.name == name })?.current == true ? "" : name
        guard requested != branch else { return }
        branch = requested
        commits = []
        nextCursor = ""
        hasLoaded = false
        errorMessage = nil
        notGit = false
        await refresh()
    }

    /// Load the next page when `commit` is the last loaded row and no earlier
    /// attempt failed (the footer's retry clears that).
    public func loadMoreIfNeeded(after commit: Ycc_V1_CommitSummary) async {
        guard loadMoreError == nil, commit.sha == commits.last?.sha else { return }
        await loadMore()
    }

    public func loadMore() async {
        guard hasLoaded, hasMore, !isLoading, !isLoadingMore else { return }
        let current = generation
        let cursor = nextCursor
        isLoadingMore = true
        loadMoreError = nil
        defer { if current == generation { isLoadingMore = false } }
        do {
            let page = try await source.listCommits(
                project: project, branch: branch, limit: Self.pageSize, cursor: cursor)
            guard current == generation else { return }
            commits += Self.unique(page.commits, after: commits)
            nextCursor = page.nextCursor
        } catch YccError.unauthorized {
            unauthorized = true
        } catch {
            guard current == generation else { return }
            loadMoreError = Self.message(error)
        }
    }

    /// `page` without shas already in `existing` (or repeated within it).
    private static func unique(
        _ page: [Ycc_V1_CommitSummary], after existing: [Ycc_V1_CommitSummary]
    ) -> [Ycc_V1_CommitSummary] {
        var seen = Set(existing.map(\.sha))
        return page.filter { seen.insert($0.sha).inserted }
    }

    private static func message(_ error: Error) -> String {
        (error as? YccError)?.displayMessage ?? error.localizedDescription
    }
}
