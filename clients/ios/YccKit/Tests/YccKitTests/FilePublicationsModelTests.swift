import Foundation
import XCTest
import YccProto
@testable import YccKit

/// Canned publication source: `responses` are returned by successive
/// `getPublications` calls (an empty queue throws).
private final class MockPublicationSource: FilePublicationSource, @unchecked Sendable {
    var responses: [Ycc_V1_GetPublicationsResponse] = []
    var publishResult: Result<Ycc_V1_PublishFileResponse, Error> = .failure(YccError.rpc(message: "unset"))
    var unpublishError: Error?
    private(set) var unpublished: [String] = []

    func getPublications(project: String, sessionID: String, path: String) async throws -> Ycc_V1_GetPublicationsResponse {
        guard !responses.isEmpty else { throw YccError.rpc(message: "unavailable") }
        return responses.removeFirst()
    }

    func publishFile(project: String, sessionID: String, path: String) async throws -> Ycc_V1_PublishFileResponse {
        try publishResult.get()
    }

    func unpublishFile(project: String, id: String) async throws {
        if let unpublishError { throw unpublishError }
        unpublished.append(id)
    }
}

private func publication(_ id: String, current: Bool = false) -> Ycc_V1_Publication {
    var p = Ycc_V1_Publication()
    p.id = id
    p.url = "https://paste.example.com/\(id)"
    p.current = current
    p.publishedAt = "2026-11-18T10:00:00Z"
    return p
}

private func listing(_ publications: [Ycc_V1_Publication], target: Bool = true) -> Ycc_V1_GetPublicationsResponse {
    var r = Ycc_V1_GetPublicationsResponse()
    if target {
        var t = Ycc_V1_PublishTarget()
        t.name = "paste.example.com"
        r.target = t
    }
    r.publications = publications
    return r
}

@MainActor
final class FilePublicationsModelTests: XCTestCase {
    private let route = FileRoute(project: "ycc", reference: FileReference(path: "docs/a.md"))

    func testLoadSplitsCurrentFromEarlierAndHidesWithoutTarget() async {
        let source = MockPublicationSource()
        source.responses = [listing([publication("new", current: true), publication("old")]),
                            listing([], target: false)]
        let model = FilePublicationsModel(source: source, route: route)
        await model.load()
        XCTAssertEqual(model.target?.name, "paste.example.com")
        XCTAssertEqual(model.current?.id, "new")
        XCTAssertEqual(model.earlier.map(\.id), ["old"])

        // Publishing switched off daemon-side: the section goes away.
        await model.load()
        XCTAssertNil(model.target)
        XCTAssertNil(model.current)
    }

    func testFailedLoadHidesThenKeepsLastGoodState() async {
        let source = MockPublicationSource()
        let model = FilePublicationsModel(source: source, route: route)
        await model.load()
        XCTAssertNil(model.target)
        XCTAssertNil(model.errorMessage, "a load failure is silent")

        source.responses = [listing([publication("a", current: true)])]
        await model.load()
        await model.load() // fails: keeps the listing
        XCTAssertEqual(model.current?.id, "a")
    }

    func testPublishMakesNewLinkCurrentEvenIfRefreshFails() async {
        let source = MockPublicationSource()
        source.responses = [listing([publication("old", current: true)])]
        let model = FilePublicationsModel(source: source, route: route)
        await model.load()

        var response = Ycc_V1_PublishFileResponse()
        response.publication = publication("new")
        source.publishResult = .success(response)
        let ok = await model.publish()
        XCTAssertTrue(ok)
        XCTAssertEqual(model.current?.id, "new")
        XCTAssertEqual(model.earlier.map(\.id), ["old"])
        XCTAssertEqual(model.lastPublished?.publication.id, "new")
        XCTAssertEqual(model.lastPublished?.reused, false)
        XCTAssertFalse(model.isPublishing)
    }

    func testReusedPublishKeepsOnePerLink() async {
        let source = MockPublicationSource()
        source.responses = [listing([publication("b"), publication("a")])]
        let model = FilePublicationsModel(source: source, route: route)
        await model.load()

        var response = Ycc_V1_PublishFileResponse()
        response.publication = publication("a")
        response.reused = true
        source.publishResult = .success(response)
        await model.publish()
        XCTAssertEqual(model.publications.map(\.id), ["b", "a"])
        XCTAssertEqual(model.current?.id, "a")
        XCTAssertEqual(model.lastPublished?.reused, true)
    }

    func testPublishFailureReportsDaemonMessage() async {
        let source = MockPublicationSource()
        source.responses = [listing([])]
        let model = FilePublicationsModel(source: source, route: route)
        await model.load()
        source.publishResult = .failure(YccError.failedPrecondition(message: "file too large to publish"))
        let ok = await model.publish()
        XCTAssertFalse(ok)
        XCTAssertEqual(model.errorMessage, "file too large to publish")
        XCTAssertNil(model.lastPublished)
        XCTAssertTrue(model.publications.isEmpty)
    }

    func testUnpublishRemovesLinkAndItsConfirmation() async {
        let source = MockPublicationSource()
        source.responses = [listing([])]
        let model = FilePublicationsModel(source: source, route: route)
        await model.load()
        var response = Ycc_V1_PublishFileResponse()
        response.publication = publication("x")
        source.publishResult = .success(response)
        await model.publish()

        let ok = await model.unpublish(id: "x")
        XCTAssertTrue(ok)
        XCTAssertEqual(source.unpublished, ["x"])
        XCTAssertNil(model.current)
        XCTAssertNil(model.lastPublished)

        source.unpublishError = YccError.notFound(message: "unknown publication")
        let failed = await model.unpublish(id: "gone")
        XCTAssertFalse(failed)
        XCTAssertEqual(model.errorMessage, "unknown publication")
    }

    func testPublicationDates() {
        var p = publication("a")
        p.expiresAt = ""
        XCTAssertNil(p.expiryDate, "empty expiry means never")
        p.expiresAt = "2026-12-18T10:00:00.5Z"
        XCTAssertEqual(p.expiryDate?.timeIntervalSince1970 ?? 0, 1_797_588_000.5, accuracy: 0.01)
        XCTAssertNotNil(p.publishedDate)
    }
}
