import XCTest
import YccProto
@testable import YccKit

private final class MockStartWorkSource: StartWorkSource, @unchecked Sendable {
    var models = Ycc_V1_ListModelsResponse()
    var roleError: Error?
    private(set) var startCoordinator: String?
    private(set) var spawnArgs: (coordinator: String, taskID: String)?
    private(set) var roleCalls: [(sessionId: String, implementer: String, reviewers: [String])] = []

    func listModels() async throws -> Ycc_V1_ListModelsResponse { models }

    func startSession(
        project: String, mode: String, prompt: String, coordinatorModel: String,
        images: [MessageImage]
    ) async throws -> String {
        startCoordinator = coordinatorModel
        return "s_new"
    }

    func spawnWorkstream(
        project: String, mode: String, prompt: String, coordinatorModel: String,
        preset: String, taskID: String, images: [MessageImage]
    ) async throws -> Ycc_V1_WorkstreamInfo {
        spawnArgs = (coordinatorModel, taskID)
        var info = Ycc_V1_WorkstreamInfo()
        info.sessionID = "s_ws"
        return info
    }

    func setRoleConfig(
        sessionId: String, coordinator: String, implementer: String, reviewers: [String]
    ) async throws {
        roleCalls.append((sessionId, implementer, reviewers))
        if let roleError { throw roleError }
    }
}

@MainActor
final class StartWorkOptionsModelTests: XCTestCase {
    private func makeModel() async -> (StartWorkOptionsModel, MockStartWorkSource) {
        let source = MockStartWorkSource()
        source.models.models = ["a", "b", "c"].map { name in
            var info = Ycc_V1_ModelInfo()
            info.name = name
            return info
        }
        source.models.coordinator = "a"
        source.models.implementer = "a"
        source.models.reviewers = ["b"]
        let model = StartWorkOptionsModel(
            source: source, project: "p", taskID: "0042", prompt: "Work on task 0042.")
        await model.load()
        return (model, source)
    }

    func testUnchangedChoicesStartWithTheDefaults() async {
        let (model, source) = await makeModel()
        let started = await model.start()
        XCTAssertEqual(started, StartedWork(sessionID: "s_new", roleError: nil))
        XCTAssertEqual(source.startCoordinator, "")
        XCTAssertTrue(source.roleCalls.isEmpty)
    }

    func testChangedRolesApplyToTheStartedSessionOnly() async {
        let (model, source) = await makeModel()
        model.coordinator = "b"
        model.implementer = "c"
        model.separateWorkstream = true
        let started = await model.start()
        XCTAssertEqual(started?.sessionID, "s_ws")
        XCTAssertEqual(source.spawnArgs?.coordinator, "b")
        XCTAssertEqual(source.spawnArgs?.taskID, "0042")
        XCTAssertEqual(source.roleCalls.count, 1)
        XCTAssertEqual(source.roleCalls.first?.sessionId, "s_ws")
        XCTAssertEqual(source.roleCalls.first?.implementer, "c")
        XCTAssertEqual(source.roleCalls.first?.reviewers, [], "unchanged reviewers stay untouched")
    }

    func testRoleFailureStillReportsTheStartedSession() async {
        let (model, source) = await makeModel()
        source.roleError = YccError.rpc(message: "unknown model")
        model.toggleReviewer("c")
        let started = await model.start()
        XCTAssertEqual(source.roleCalls.first?.reviewers, ["b", "c"])
        XCTAssertEqual(started, StartedWork(sessionID: "s_new", roleError: "unknown model"))
    }
}
