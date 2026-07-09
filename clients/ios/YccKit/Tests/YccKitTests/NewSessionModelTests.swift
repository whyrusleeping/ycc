import Foundation
import XCTest
import YccProto
@testable import YccKit

/// A scripted in-memory ``NewSessionSource`` for headless model tests. Records
/// the last start/resume arguments so the request round-trip is testable.
private final class MockNewSessionSource: NewSessionSource, @unchecked Sendable {
    var modes: [Ycc_V1_Mode] = []
    var presets: [Ycc_V1_Preset] = []
    var projects: [Ycc_V1_ProjectInfo] = []
    var modesError: Error?
    var startError: Error?
    var resumeError: Error?
    var startedSessionId = "s_new"

    private(set) var startArgs: (project: String, mode: String, prompt: String, level: String)?
    private(set) var resumeArgs: (project: String, sessionId: String)?

    func listModes() async throws -> (modes: [Ycc_V1_Mode], presets: [Ycc_V1_Preset]) {
        if let modesError { throw modesError }
        return (modes, presets)
    }

    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        projects
    }

    func startSession(
        project: String, mode: String, prompt: String, interactionLevel: String
    ) async throws -> String {
        startArgs = (project, mode, prompt, interactionLevel)
        if let startError { throw startError }
        return startedSessionId
    }

    func resumeSession(project: String, sessionId: String) async throws -> String {
        resumeArgs = (project, sessionId)
        if let resumeError { throw resumeError }
        return sessionId
    }
}

/// An in-memory ``SessionDefaultsStore`` so recall/persist is testable.
private final class MockDefaults: SessionDefaultsStore {
    var lastMode: String?
    var lastInteractionLevel: String?
    var lastProject: String?
}

@MainActor
final class NewSessionModelTests: XCTestCase {
    private func mode(_ name: String, description: String = "") -> Ycc_V1_Mode {
        var m = Ycc_V1_Mode()
        m.name = name
        m.title = name.capitalized
        m.description_p = description
        return m
    }

    private func preset(_ name: String, mode: String, prompt: String) -> Ycc_V1_Preset {
        var p = Ycc_V1_Preset()
        p.name = name
        p.mode = mode
        p.openingPrompt = prompt
        return p
    }

    private func project(_ name: String) -> Ycc_V1_ProjectInfo {
        var p = Ycc_V1_ProjectInfo()
        p.name = name
        p.path = "/tmp/\(name)"
        return p
    }

    // MARK: - Loading + defaults

    func testLoadPopulatesAndDefaultsToFirstMode() async {
        let source = MockNewSessionSource()
        source.modes = [mode("work", description: "Do work"), mode("pm")]
        source.projects = [project("one"), project("two")]
        let model = NewSessionModel(source: source, defaults: MockDefaults())

        await model.load()

        XCTAssertEqual(model.modes.count, 2)
        XCTAssertEqual(model.selectedMode, "work")
        XCTAssertEqual(model.selectedModeDescription, "Do work")
        XCTAssertTrue(model.showsProjectPicker)
        XCTAssertNil(model.errorMessage)
    }

    func testLoadRecallsRememberedSelections() async {
        let defaults = MockDefaults()
        defaults.lastMode = "pm"
        defaults.lastInteractionLevel = "autonomous"
        defaults.lastProject = "two"
        let source = MockNewSessionSource()
        source.modes = [mode("work"), mode("pm")]
        source.projects = [project("one"), project("two")]
        let model = NewSessionModel(source: source, defaults: defaults)

        // Recalled before load, from the defaults store.
        XCTAssertEqual(model.selectedMode, "pm")
        XCTAssertEqual(model.interactionLevel, .autonomous)
        XCTAssertEqual(model.selectedProject, "two")

        await model.load()

        // Still honoured after load because they exist.
        XCTAssertEqual(model.selectedMode, "pm")
        XCTAssertEqual(model.selectedProject, "two")
    }

    func testLoadDropsStaleRememberedModeAndProject() async {
        let defaults = MockDefaults()
        defaults.lastMode = "gone"
        defaults.lastProject = "gone"
        let source = MockNewSessionSource()
        source.modes = [mode("work"), mode("pm")]
        source.projects = [project("one")]
        let model = NewSessionModel(source: source, defaults: defaults)

        await model.load()

        XCTAssertEqual(model.selectedMode, "work")   // fell back to first
        XCTAssertEqual(model.selectedProject, "")     // fell back to default
    }

    func testInvalidRememberedLevelFallsBackToJudgement() {
        let defaults = MockDefaults()
        defaults.lastInteractionLevel = "nonsense"
        let model = NewSessionModel(source: MockNewSessionSource(), defaults: defaults)
        XCTAssertEqual(model.interactionLevel, .judgement)
    }

    // MARK: - Validation

    func testCanStartRequiresModeAndPrompt() async {
        let source = MockNewSessionSource()
        source.modes = [mode("work")]
        let model = NewSessionModel(source: source, defaults: MockDefaults())
        await model.load()

        XCTAssertFalse(model.canStart)          // empty prompt
        model.prompt = "   "
        XCTAssertFalse(model.canStart)          // whitespace-only prompt
        model.prompt = "do the thing"
        XCTAssertTrue(model.canStart)
        model.selectedMode = ""
        XCTAssertFalse(model.canStart)          // no mode
    }

    // MARK: - Presets

    func testApplyPresetSetsModeAndPrompt() {
        let model = NewSessionModel(source: MockNewSessionSource(), defaults: MockDefaults())
        model.apply(preset: preset("spec", mode: "pm", prompt: "Write a spec for…"))
        XCTAssertEqual(model.selectedMode, "pm")
        XCTAssertEqual(model.prompt, "Write a spec for…")
    }

    // MARK: - Start

    func testStartSendsTrimmedRequestAndRemembersSelections() async {
        let defaults = MockDefaults()
        let source = MockNewSessionSource()
        source.modes = [mode("work"), mode("pm")]
        source.projects = [project("one")]
        source.startedSessionId = "s_123"
        let model = NewSessionModel(source: source, defaults: defaults)
        await model.load()
        model.selectedMode = "pm"
        model.interactionLevel = .interactive
        model.selectedProject = "one"
        model.prompt = "  build a thing  "

        let id = await model.start()

        XCTAssertEqual(id, "s_123")
        XCTAssertEqual(source.startArgs?.project, "one")
        XCTAssertEqual(source.startArgs?.mode, "pm")
        XCTAssertEqual(source.startArgs?.prompt, "build a thing")
        XCTAssertEqual(source.startArgs?.level, "interactive")
        // Selections persisted for next time.
        XCTAssertEqual(defaults.lastMode, "pm")
        XCTAssertEqual(defaults.lastInteractionLevel, "interactive")
        XCTAssertEqual(defaults.lastProject, "one")
    }

    func testStartWithoutValidStateReturnsNil() async {
        let source = MockNewSessionSource()
        let model = NewSessionModel(source: source, defaults: MockDefaults())
        // No mode, no prompt.
        let id = await model.start()
        XCTAssertNil(id)
        XCTAssertNil(source.startArgs)   // never called
    }

    func testStartSurfacesRpcError() async {
        let source = MockNewSessionSource()
        source.modes = [mode("work")]
        source.startError = YccError.rpc(message: "unknown project")
        let model = NewSessionModel(source: source, defaults: MockDefaults())
        await model.load()
        model.prompt = "go"

        let id = await model.start()

        XCTAssertNil(id)
        XCTAssertEqual(model.errorMessage, "unknown project")
        XCTAssertFalse(model.unauthorized)
    }

    func testStartSurfacesNotFoundError() async {
        let source = MockNewSessionSource()
        source.modes = [mode("work")]
        source.startError = YccError.notFound(message: "no such project")
        let model = NewSessionModel(source: source, defaults: MockDefaults())
        await model.load()
        model.prompt = "go"

        let id = await model.start()

        XCTAssertNil(id)
        XCTAssertEqual(model.errorMessage, "no such project")
    }

    func testStartSurfacesUnauthorized() async {
        let source = MockNewSessionSource()
        source.modes = [mode("work")]
        source.startError = YccError.unauthorized
        let model = NewSessionModel(source: source, defaults: MockDefaults())
        await model.load()
        model.prompt = "go"

        let id = await model.start()

        XCTAssertNil(id)
        XCTAssertTrue(model.unauthorized)
    }

    func testLoadSurfacesUnauthorized() async {
        let source = MockNewSessionSource()
        source.modesError = YccError.unauthorized
        let model = NewSessionModel(source: source, defaults: MockDefaults())

        await model.load()

        XCTAssertTrue(model.unauthorized)
    }
}
