import XCTest
import YccProto
@testable import YccKit

private final class MockStartWorkSource: StartWorkSource, @unchecked Sendable {
    var models = Ycc_V1_ListModelsResponse()
    var roleError: Error?
    var thinkingError: Error?
    private(set) var startCoordinator: String?
    private(set) var startThinking: String?
    private(set) var spawnArgs: (coordinator: String, thinking: String, taskID: String)?
    private(set) var roleCalls: [(sessionId: String, implementer: String, reviewers: [String])] = []
    private(set) var thinkingCalls: [(sessionId: String, level: String, role: String)] = []
    /// RPC order, to check levels follow the role change.
    private(set) var calls: [String] = []

    func listModels() async throws -> Ycc_V1_ListModelsResponse { models }

    func startSession(
        project: String, mode: String, prompt: String, coordinatorModel: String,
        coordinatorThinking: String, images: [MessageImage]
    ) async throws -> String {
        startCoordinator = coordinatorModel
        startThinking = coordinatorThinking
        return "s_new"
    }

    func spawnWorkstream(
        project: String, mode: String, prompt: String, coordinatorModel: String,
        coordinatorThinking: String, preset: String, taskID: String, images: [MessageImage]
    ) async throws -> Ycc_V1_WorkstreamInfo {
        spawnArgs = (coordinatorModel, coordinatorThinking, taskID)
        var info = Ycc_V1_WorkstreamInfo()
        info.sessionID = "s_ws"
        return info
    }

    func setRoleConfig(
        sessionId: String, coordinator: String, implementer: String, reviewers: [String]
    ) async throws {
        roleCalls.append((sessionId, implementer, reviewers))
        calls.append("role")
        if let roleError { throw roleError }
    }

    func setThinking(sessionId: String, level: String, role: String) async throws {
        thinkingCalls.append((sessionId, level, role))
        calls.append("thinking:\(role)")
        if let thinkingError { throw thinkingError }
    }
}

@MainActor
final class StartWorkOptionsModelTests: XCTestCase {
    private func makeModel() async -> (StartWorkOptionsModel, MockStartWorkSource) {
        let source = MockStartWorkSource()
        // "c" is an Ollama-style model: thinking on or off only.
        source.models.models = ["a", "b", "c"].map { name in
            var info = Ycc_V1_ModelInfo()
            info.name = name
            if name == "c" { info.thinkingLevels = ["off", "high"] }
            return info
        }
        source.models.coordinator = "a"
        source.models.implementer = "a"
        source.models.reviewers = ["b"]
        source.models.coordinatorThinking = "medium"
        source.models.implementerThinking = "medium"
        source.models.reviewersThinking = "low"
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
        XCTAssertEqual(source.startThinking, "", "default thinking is not an override")
        XCTAssertTrue(source.roleCalls.isEmpty)
        XCTAssertTrue(source.thinkingCalls.isEmpty)
    }

    func testThinkingPicksApplyToTheStartedSession() async {
        let (model, source) = await makeModel()
        XCTAssertEqual(model.defaultThinking(for: .coordinator), .medium)
        XCTAssertEqual(model.defaultThinking(for: .reviewers), .low)
        model.implementer = "c"
        model.chooseThinking(.max, for: .coordinator)
        model.chooseThinking(.off, for: .implementer)
        model.chooseThinking(.xhigh, for: .reviewers)
        model.separateWorkstream = true
        let started = await model.start()
        XCTAssertEqual(started, StartedWork(sessionID: "s_ws", roleError: nil))
        XCTAssertEqual(source.spawnArgs?.thinking, "max")
        XCTAssertNil(model.defaultThinking(for: .implementer), "a changed model's default level is unknown")
        XCTAssertEqual(source.calls, ["role", "thinking:implementer", "thinking:reviewers"])
        XCTAssertEqual(source.thinkingCalls.map(\.sessionId), ["s_ws", "s_ws"])
        XCTAssertEqual(source.thinkingCalls.map(\.level), ["off", "xhigh"])
    }

    /// The daemon keeps one level per model, so a pick reaches every role
    /// sharing the model; the last pick is what all of them show and run.
    func testPickReachesRolesSharingTheModel() async {
        let (model, source) = await makeModel()
        model.chooseThinking(.high, for: .reviewers) // "b"
        model.toggleReviewer("a") // reviewers now share "a" with the coordinator and implementer
        XCTAssertEqual(model.reviewersThinking, .high)
        model.chooseThinking(.low, for: .coordinator) // "a", shared with implementer and reviewers
        XCTAssertEqual(model.implementerThinking, .low)
        XCTAssertEqual(model.reviewersThinking, .low)
        XCTAssertNil(model.thinkingConflict)
        _ = await model.start()
        XCTAssertEqual(source.startThinking, "low")
        XCTAssertEqual(source.thinkingCalls.map(\.level), ["low", "low"])
    }

    func testModelChangeAdoptsTheSharedModelsPick() async {
        let (model, _) = await makeModel()
        model.chooseThinking(.medium, for: .reviewers) // "b" only
        XCTAssertNil(model.coordinatorThinking)
        model.coordinator = "b"
        XCTAssertEqual(model.coordinatorThinking, .medium)
    }

    func testConflictingPicksOnASharedModelBlockStarting() async {
        let (model, _) = await makeModel()
        model.implementer = "b"
        model.chooseThinking(.high, for: .implementer) // also reviewers ("b")
        model.chooseThinking(.low, for: .coordinator) // "a" alone
        XCTAssertNil(model.thinkingConflict)
        // A new reviewer set sharing both models now carries two levels.
        model.toggleReviewer("a")
        XCTAssertNotNil(model.thinkingConflict)
        XCTAssertFalse(model.canStart)
        model.chooseThinking(.high, for: .reviewers)
        XCTAssertNil(model.thinkingConflict)
        XCTAssertTrue(model.canStart)
    }

    func testModelChangeMovesThinkingToASupportedLevel() async {
        let (model, _) = await makeModel()
        XCTAssertEqual(model.thinkingLevels(for: .coordinator), ThinkingLevel.allCases)
        model.implementer = "b"
        model.chooseThinking(.medium, for: .coordinator)
        model.coordinator = "c"
        XCTAssertEqual(model.thinkingLevels(for: .coordinator), [.off, .high])
        XCTAssertEqual(model.coordinatorThinking, .high)
        model.implementer = "c"
        XCTAssertEqual(model.implementerThinking, .high, "adopts the shared model's level")
    }

    func testThinkingFailureStillReportsTheStartedSession() async {
        let (model, source) = await makeModel()
        source.thinkingError = YccError.rpc(message: "bad level")
        model.chooseThinking(.high, for: .reviewers)
        let started = await model.start()
        XCTAssertEqual(source.startThinking, "")
        XCTAssertEqual(started, StartedWork(sessionID: "s_new", roleError: "bad level"))
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
