import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A path-keyed in-memory directory source. It records the suggest bit as well
/// as the path so request shaping is covered without a daemon connection.
private final class MockDirectoryBrowserSource: DirectoryBrowserSource, @unchecked Sendable {
    var responses: [String: Ycc_V1_ListDirResponse] = [:]
    var errors: [String: Error] = [:]
    private(set) var requests: [(path: String, suggest: Bool)] = []

    func listDir(path: String, suggest: Bool) async throws -> Ycc_V1_ListDirResponse {
        requests.append((path, suggest))
        if let error = errors[path] { throw error }
        guard let response = responses[path] else {
            throw YccError.rpc(message: "unexpected path: \(path)")
        }
        return response
    }
}

/// Holds a request open so the model's loading and overlapping-request guards
/// can be tested deterministically.
private actor BlockingDirectoryBrowserSource: DirectoryBrowserSource {
    private var requests: [(path: String, suggest: Bool)] = []
    private var pending: CheckedContinuation<Ycc_V1_ListDirResponse, Never>?

    func listDir(path: String, suggest: Bool) async throws -> Ycc_V1_ListDirResponse {
        requests.append((path, suggest))
        return await withCheckedContinuation { continuation in
            pending = continuation
        }
    }

    func waitUntilRequested() async {
        while pending == nil {
            await Task.yield()
        }
    }

    func requestCount() -> Int { requests.count }

    func resume(with response: Ycc_V1_ListDirResponse) {
        let continuation = pending
        pending = nil
        continuation?.resume(returning: response)
    }
}

private func dirEntry(
    _ name: String,
    isGitRepo: Bool = false,
    isRegistered: Bool = false
) -> Ycc_V1_DirEntry {
    var entry = Ycc_V1_DirEntry()
    entry.name = name
    entry.isGitRepo = isGitRepo
    entry.isRegistered = isRegistered
    return entry
}

private func dirResponse(
    path: String,
    parent: String,
    entries: [Ycc_V1_DirEntry] = [],
    suggestions: [String] = []
) -> Ycc_V1_ListDirResponse {
    var response = Ycc_V1_ListDirResponse()
    response.path = path
    response.parent = parent
    response.entries = entries
    response.suggestions = suggestions
    return response
}

@MainActor
final class DirectoryBrowserModelTests: XCTestCase {
    func testInitialLoadResolvesHomeAndSuggestions() async {
        let source = MockDirectoryBrowserSource()
        source.responses[""] = dirResponse(
            path: "/Users/me",
            parent: "/Users",
            entries: [dirEntry("code")],
            suggestions: ["/Users/me/code/ycc", "/Users/me/code/other"])
        let model = DirectoryBrowserModel(source: source)

        await model.loadInitial()

        XCTAssertEqual(model.path, "/Users/me")
        XCTAssertEqual(model.parent, "/Users")
        XCTAssertEqual(model.entries.map(\.name), ["code"])
        XCTAssertEqual(model.suggestions, ["/Users/me/code/ycc", "/Users/me/code/other"])
        XCTAssertEqual(source.requests.count, 1)
        XCTAssertEqual(source.requests.first?.path, "")
        XCTAssertEqual(source.requests.first?.suggest, true)
        XCTAssertFalse(model.isLoading)

        // A successful initial load is not repeated when the view reappears.
        await model.loadInitial()
        XCTAssertEqual(source.requests.count, 1)
    }

    func testOpenDrillsDownAndClearsError() async {
        let source = MockDirectoryBrowserSource()
        source.responses[""] = dirResponse(path: "/Users/me", parent: "/Users")
        source.responses["/Users/me/code"] = dirResponse(
            path: "/Users/me/code",
            parent: "/Users/me",
            entries: [dirEntry("ycc")])
        let model = DirectoryBrowserModel(source: source)
        await model.loadInitial()
        model.errorMessage = "old error"

        await model.open("/Users/me/code")

        XCTAssertEqual(model.path, "/Users/me/code")
        XCTAssertEqual(model.parent, "/Users/me")
        XCTAssertEqual(model.entries.map(\.name), ["ycc"])
        XCTAssertNil(model.errorMessage)
        XCTAssertEqual(source.requests.last?.path, "/Users/me/code")
        XCTAssertEqual(source.requests.last?.suggest, false)
    }

    func testNavigateUpUsesParentAndStopsAtRoot() async {
        let source = MockDirectoryBrowserSource()
        source.responses[""] = dirResponse(path: "/Users", parent: "/")
        source.responses["/"] = dirResponse(path: "/", parent: "", entries: [dirEntry("Users")])
        let model = DirectoryBrowserModel(source: source)
        await model.loadInitial()

        XCTAssertTrue(model.canGoUp)
        await model.navigateUp()

        XCTAssertEqual(model.path, "/")
        XCTAssertEqual(model.parent, "")
        XCTAssertFalse(model.canGoUp)
        XCTAssertEqual(source.requests.last?.path, "/")
        XCTAssertEqual(source.requests.last?.suggest, false)

        let requestCount = source.requests.count
        await model.navigateUp()
        XCTAssertEqual(source.requests.count, requestCount)
    }

    func testEntryAnnotationsFlowThrough() async {
        let source = MockDirectoryBrowserSource()
        source.responses[""] = dirResponse(
            path: "/srv",
            parent: "/",
            entries: [
                dirEntry("repo", isGitRepo: true),
                dirEntry("registered", isGitRepo: true, isRegistered: true),
            ])
        let model = DirectoryBrowserModel(source: source)

        await model.loadInitial()

        XCTAssertTrue(model.entries[0].isGitRepo)
        XCTAssertFalse(model.entries[0].isRegistered)
        XCTAssertTrue(model.entries[1].isGitRepo)
        XCTAssertTrue(model.entries[1].isRegistered)
    }

    func testFailedOpenKeepsCurrentListingAndSetsError() async {
        let source = MockDirectoryBrowserSource()
        let originalEntries = [dirEntry("code", isGitRepo: true)]
        source.responses[""] = dirResponse(
            path: "/Users/me", parent: "/Users", entries: originalEntries)
        source.errors["/denied"] = YccError.rpc(message: "permission denied")
        let model = DirectoryBrowserModel(source: source)
        await model.loadInitial()

        await model.open("/denied")

        XCTAssertEqual(model.path, "/Users/me")
        XCTAssertEqual(model.parent, "/Users")
        XCTAssertEqual(model.entries, originalEntries)
        XCTAssertEqual(model.errorMessage, "permission denied")
        XCTAssertFalse(model.isLoading)
    }

    func testUnauthorizedSetsFlagWithoutReplacingListing() async {
        let source = MockDirectoryBrowserSource()
        source.responses[""] = dirResponse(path: "/srv", parent: "/")
        source.errors["/secret"] = YccError.unauthorized
        let model = DirectoryBrowserModel(source: source)
        await model.loadInitial()

        await model.open("/secret")

        XCTAssertTrue(model.unauthorized)
        XCTAssertEqual(model.path, "/srv")
        XCTAssertNil(model.errorMessage)
    }

    func testOverlappingOpenIsIgnoredWhileLoading() async {
        let source = BlockingDirectoryBrowserSource()
        let model = DirectoryBrowserModel(source: source)

        let first = Task { await model.open("/first") }
        await source.waitUntilRequested()
        XCTAssertTrue(model.isLoading)

        await model.open("/ignored")
        let requestCount = await source.requestCount()
        XCTAssertEqual(requestCount, 1)

        await source.resume(with: dirResponse(path: "/first", parent: "/"))
        await first.value

        XCTAssertFalse(model.isLoading)
        XCTAssertEqual(model.path, "/first")
    }
}
