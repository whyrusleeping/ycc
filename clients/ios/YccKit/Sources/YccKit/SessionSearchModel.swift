import Foundation
import Observation
import YccProto

/// The `SearchSessions` RPC behind the project session list's search field.
public protocol SessionSearchSource: Sendable {
    func searchSessions(
        project: String, query: String, limit: Int32
    ) async throws -> Ycc_V1_SearchSessionsResponse
}

extension YccClient: SessionSearchSource {
    /// Full-text search over one project's session messages: one hit per
    /// session, best match first.
    public func searchSessions(
        project: String, query: String, limit: Int32
    ) async throws -> Ycc_V1_SearchSessionsResponse {
        var request = Ycc_V1_SearchSessionsRequest()
        request.project = project
        request.query = query
        request.limit = limit
        switch await generated.searchSessions(request: request).result {
        case .success(let message): return message
        case .failure(let error): throw Self.map(error)
        }
    }
}

/// Message search for a project's session list: the hits for the latest
/// query, whether the daemon's index is still building, and the request
/// pacing. ``run(project:input:)`` is meant for a `.task(id:)` keyed on the
/// project and the field's text, so a newer keystroke cancels the older
/// search (its debounce, request or indexing poll).
@MainActor
@Observable
public final class SessionSearchModel {
    /// Shorter queries are not sent: a one-letter prefix matches nearly everything.
    public static let minQueryLength = 2
    public static let limit: Int32 = 30
    static let debounceNanoseconds: UInt64 = 250_000_000
    /// While the index builds, ask again so hits fill in as sessions are indexed.
    static let indexingPollNanoseconds: UInt64 = 2_000_000_000

    /// The project ``hits`` belong to.
    public private(set) var project: String?
    /// The query ``hits`` answer ("" before any answer).
    public private(set) var query = ""
    public private(set) var hits: [Ycc_V1_SessionSearchHit] = []
    /// The latest answer came from a still-building index, so hits may be missing.
    public private(set) var indexing = false
    public private(set) var indexedSessions = 0
    public private(set) var totalSessions = 0
    /// A search for the current field text has not answered yet.
    public private(set) var isSearching = false
    public private(set) var errorMessage: String?
    public private(set) var unauthorized = false

    private let source: SessionSearchSource
    private let sleep: @Sendable (UInt64) async throws -> Void

    public init(
        source: SessionSearchSource,
        sleep: @escaping @Sendable (UInt64) async throws -> Void = { try await Task.sleep(nanoseconds: $0) }
    ) {
        self.source = source
        self.sleep = sleep
    }

    /// The trimmed query worth sending for `input`, else "".
    public static func searchableQuery(_ input: String) -> String {
        let query = input.trimmingCharacters(in: .whitespacesAndNewlines)
        return query.count >= minQueryLength ? query : ""
    }

    /// Search `project` for `input` after a short debounce, then re-ask every
    /// two seconds while the daemon reports it is still indexing. Returns when
    /// the answer is complete, the input is not searchable, or the task is
    /// cancelled. Hits from another project are dropped at once (they would
    /// open in the wrong project); the same project's stay on screen until
    /// the new query answers.
    public func run(project: String?, input: String) async {
        let query = Self.searchableQuery(input)
        if project != self.project {
            self.project = project
            clearResults()
        }
        guard let project, !query.isEmpty else {
            clearResults()
            isSearching = false
            return
        }
        isSearching = query != self.query
        do {
            try await sleep(Self.debounceNanoseconds)
            while true {
                let response = try await source.searchSessions(
                    project: project, query: query, limit: Self.limit)
                try Task.checkCancellation()
                self.query = query
                hits = response.hits
                indexing = response.indexing
                indexedSessions = Int(response.indexedSessions)
                totalSessions = Int(response.totalSessions)
                errorMessage = nil
                isSearching = false
                guard response.indexing else { return }
                try await sleep(Self.indexingPollNanoseconds)
            }
        } catch is CancellationError {
            // A newer input (or leaving the list) took over.
        } catch YccError.unauthorized {
            unauthorized = true
            isSearching = false
        } catch {
            guard !Task.isCancelled else { return }
            errorMessage = (error as? YccError)?.displayMessage ?? error.localizedDescription
            isSearching = false
        }
    }

    private func clearResults() {
        query = ""
        hits = []
        indexing = false
        indexedSessions = 0
        totalSessions = 0
        errorMessage = nil
    }

    // MARK: - Presentation

    /// Says the index is still building (results so far may be incomplete); nil when complete.
    public var indexingNote: String? {
        guard indexing else { return nil }
        let progress = totalSessions > 0 ? " (\(indexedSessions) of \(totalSessions) sessions)" : ""
        return "Still indexing session history\(progress); results may be incomplete."
    }

    /// Who wrote the matching message.
    public static func kindLabel(_ kind: String) -> String {
        switch kind {
        case "user": return "You"
        case "assistant": return "Agent"
        case "question": return "Question"
        case "answer": return "Answer"
        default: return kind
        }
    }

    /// When the matching message was written.
    public static func date(of hit: Ycc_V1_SessionSearchHit) -> Date? {
        SessionListModel.parseTimestamp(hit.timestamp)
    }

    /// The snippet as one line of text (newlines folded to spaces).
    public static func snippetText(_ spans: [Ycc_V1_SessionSearchSpan]) -> String {
        spans.map(\.text).joined().replacingOccurrences(of: "\n", with: " ")
    }
}
