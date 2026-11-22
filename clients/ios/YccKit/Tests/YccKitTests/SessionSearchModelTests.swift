import Foundation
import XCTest
import YccProto
@testable import YccKit

private final class MockSearchSource: SessionSearchSource, @unchecked Sendable {
    struct Call: Equatable {
        let project: String
        let query: String
    }

    private let lock = NSLock()
    /// Answers handed out in order; the last one repeats.
    var responses: [Ycc_V1_SearchSessionsResponse] = []
    var error: Error?
    private var _calls: [Call] = []
    var calls: [Call] { lock.withLock { _calls } }

    func searchSessions(project: String, query: String, limit: Int32) async throws -> Ycc_V1_SearchSessionsResponse {
        let (error, response) = lock.withLock { () -> (Error?, Ycc_V1_SearchSessionsResponse) in
            _calls.append(Call(project: project, query: query))
            let response = responses.count > 1 ? responses.removeFirst() : (responses.first ?? .init())
            return (error, response)
        }
        if let error { throw error }
        return response
    }
}

private func hit(_ session: String, row: String) -> Ycc_V1_SessionSearchHit {
    var h = Ycc_V1_SessionSearchHit()
    h.session.sessionID = session
    h.rowID = row
    h.kind = "user"
    return h
}

private func response(_ hits: [Ycc_V1_SessionSearchHit], indexing: Bool = false, indexed: Int32 = 0, total: Int32 = 0) -> Ycc_V1_SearchSessionsResponse {
    var r = Ycc_V1_SearchSessionsResponse()
    r.hits = hits
    r.indexing = indexing
    r.indexedSessions = indexed
    r.totalSessions = total
    return r
}

@MainActor
final class SessionSearchModelTests: XCTestCase {
    private final class Sleeps: @unchecked Sendable {
        private let lock = NSLock()
        private var _values: [UInt64] = []
        var values: [UInt64] { lock.withLock { _values } }
        func record(_ value: UInt64) { lock.withLock { _values.append(value) } }
    }

    func testShortOrEmptyInputSendsNothing() async {
        let source = MockSearchSource()
        let model = SessionSearchModel(source: source, sleep: { _ in })
        await model.run(project: "p", input: " a ")
        await model.run(project: nil, input: "hello")
        XCTAssertEqual(source.calls, [])
        XCTAssertTrue(model.hits.isEmpty)
        XCTAssertFalse(model.isSearching)
    }

    func testSearchesAfterDebounceAndPollsWhileIndexing() async {
        let source = MockSearchSource()
        source.responses = [
            response([hit("s1", row: "seq-4")], indexing: true, indexed: 1, total: 3),
            response([hit("s1", row: "seq-4"), hit("s2", row: "seq-9")]),
        ]
        let sleeps = Sleeps()
        let model = SessionSearchModel(source: source, sleep: { sleeps.record($0) })
        await model.run(project: "p", input: "  deploy fix ")
        XCTAssertEqual(source.calls, [.init(project: "p", query: "deploy fix"), .init(project: "p", query: "deploy fix")])
        XCTAssertEqual(sleeps.values, [SessionSearchModel.debounceNanoseconds, SessionSearchModel.indexingPollNanoseconds])
        XCTAssertEqual(model.hits.map(\.rowID), ["seq-4", "seq-9"])
        XCTAssertEqual(model.query, "deploy fix")
        XCTAssertFalse(model.indexing)
        XCTAssertNil(model.indexingNote)
        XCTAssertFalse(model.isSearching)
    }

    func testIndexingNoteCarriesProgress() async {
        let source = MockSearchSource()
        source.responses = [response([], indexing: true, indexed: 2, total: 5)]
        // Cancel the indexing poll after the first answer.
        let model = SessionSearchModel(source: source, sleep: { value in
            if value == SessionSearchModel.indexingPollNanoseconds { throw CancellationError() }
        })
        await model.run(project: "p", input: "zz")
        XCTAssertTrue(model.indexing)
        XCTAssertEqual(model.indexingNote, "Still indexing session history (2 of 5 sessions); results may be incomplete.")
    }

    func testAnotherProjectDropsHitsAndErrorsAreReported() async {
        let source = MockSearchSource()
        source.responses = [response([hit("s1", row: "seq-4")])]
        let model = SessionSearchModel(source: source, sleep: { _ in })
        await model.run(project: "a", input: "hello")
        XCTAssertEqual(model.hits.count, 1)

        source.error = YccError.rpc(message: "boom")
        await model.run(project: "b", input: "hello")
        XCTAssertEqual(model.project, "b")
        XCTAssertTrue(model.hits.isEmpty, "another project's hits would open in the wrong project")
        XCTAssertEqual(model.errorMessage, YccError.rpc(message: "boom").displayMessage)

        source.error = YccError.unauthorized
        await model.run(project: "b", input: "hello again")
        XCTAssertTrue(model.unauthorized)
    }

    func testPresentationHelpers() {
        XCTAssertEqual(SessionSearchModel.kindLabel("assistant"), "Agent")
        XCTAssertEqual(SessionSearchModel.kindLabel("answer"), "Answer")
        var a = Ycc_V1_SessionSearchSpan()
        a.text = "the\nfix "
        var b = Ycc_V1_SessionSearchSpan()
        b.text = "deploy"
        b.match = true
        XCTAssertEqual(SessionSearchModel.snippetText([a, b]), "the fix deploy")
        XCTAssertEqual(SessionSearchModel.searchableQuery(" x "), "")
        XCTAssertEqual(SessionSearchModel.searchableQuery(" xy "), "xy")
    }
}
