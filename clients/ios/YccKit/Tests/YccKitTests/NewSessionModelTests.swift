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
    var models = Ycc_V1_ListModelsResponse()
    var modesError: Error?
    var modelsError: Error?
    var startError: Error?
    var resumeError: Error?
    var startedSessionId = "s_new"

    private(set) var startArgs: (
        project: String, mode: String, prompt: String, coordinatorModel: String,
        images: [MessageImage])?
    private(set) var resumeArgs: (project: String, sessionId: String)?

    func listModes() async throws -> (modes: [Ycc_V1_Mode], presets: [Ycc_V1_Preset]) {
        if let modesError { throw modesError }
        return (modes, presets)
    }

    func listProjects() async throws -> [Ycc_V1_ProjectInfo] {
        projects
    }

    func listModels() async throws -> Ycc_V1_ListModelsResponse {
        if let modelsError { throw modelsError }
        return models
    }

    func startSession(
        project: String, mode: String, prompt: String, coordinatorModel: String,
        images: [MessageImage]
    ) async throws -> String {
        startArgs = (project, mode, prompt, coordinatorModel, images)
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

    private func project(_ name: String, needsOnboarding: Bool? = nil) -> Ycc_V1_ProjectInfo {
        var p = Ycc_V1_ProjectInfo()
        p.name = name
        p.path = "/tmp/\(name)"
        if let needsOnboarding {
            p.needsOnboarding = needsOnboarding
        }
        return p
    }

    private func preset(_ name: String) -> Ycc_V1_Preset {
        var p = Ycc_V1_Preset()
        p.name = name
        p.title = name.capitalized
        p.mode = "pm"
        return p
    }

    /// A `ListModels` response with the given logical models and default
    /// coordinator assignment.
    private func modelList(_ names: [String], coordinator: String) -> Ycc_V1_ListModelsResponse {
        var response = Ycc_V1_ListModelsResponse()
        response.models = names.map { name in
            var info = Ycc_V1_ModelInfo()
            info.name = name
            info.backend = "anthropic"
            info.model = "\(name)-latest"
            return info
        }
        response.coordinator = coordinator
        return response
    }

    // MARK: - Model picker

    func testLoadOffersConfiguredModelsWithoutSelectingAnOverride() async {
        let source = MockNewSessionSource()
        source.modes = [mode("work")]
        source.models = modelList(["claude", "gpt"], coordinator: "claude")
        let model = NewSessionModel(source: source, defaults: MockDefaults())

        await model.load()

        XCTAssertEqual(model.models.map(\.name), ["claude", "gpt"])
        XCTAssertEqual(model.selectedModel, "")
    }

    func testDisabledDefaultRequiresEnabledOverride() async {
        let source = MockNewSessionSource()
        source.modes = [mode("work")]
        var listed = modelList(["claude", "gpt"], coordinator: "claude")
        listed.models[0].disabled = true
        source.models = listed
        let model = NewSessionModel(source: source, defaults: MockDefaults())

        await model.load()

        XCTAssertEqual(model.models.map(\.name), ["gpt"])
        XCTAssertTrue(model.defaultModelDisabled)
        XCTAssertTrue(model.showsModelPicker)
        XCTAssertEqual(model.selectedModelTitle, "Choose model")
        XCTAssertFalse(model.canStart)
        model.selectedModel = "gpt"
        XCTAssertTrue(model.canStart)
    }

    func testStartSendsSelectedModelAsOverride() async {
        let source = MockNewSessionSource()
        source.modes = [mode("work")]
        source.models = modelList(["claude", "gpt"], coordinator: "claude")
        let model = NewSessionModel(source: source, defaults: MockDefaults())
        await model.load()
        model.selectedModel = "gpt"
        model.prompt = "go"

        _ = await model.start()

        XCTAssertEqual(source.startArgs?.coordinatorModel, "gpt")
    }

    func testStartWithoutModelChoiceSendsEmptyOverride() async {
        let source = MockNewSessionSource()
        source.modes = [mode("work")]
        source.models = modelList(["claude", "gpt"], coordinator: "claude")
        let model = NewSessionModel(source: source, defaults: MockDefaults())
        await model.load()
        model.prompt = "go"

        _ = await model.start()

        XCTAssertEqual(source.startArgs?.coordinatorModel, "")
    }

    func testModelChoiceIsNotRemembered() async {
        // The pick is a per-session override, so the next composer opens back on
        // the daemon's default (unlike mode/project, which are sticky).
        let defaults = MockDefaults()
        let source = MockNewSessionSource()
        source.modes = [mode("work")]
        source.models = modelList(["claude", "gpt"], coordinator: "claude")
        let first = NewSessionModel(source: source, defaults: defaults)
        await first.load()
        first.selectedModel = "gpt"
        first.prompt = "go"
        _ = await first.start()

        let second = NewSessionModel(source: source, defaults: defaults)
        await second.load()
        XCTAssertEqual(second.selectedModel, "")
    }

    func testLoadDropsOverrideForRemovedModel() async {
        let source = MockNewSessionSource()
        source.modes = [mode("work")]
        source.models = modelList(["claude", "gpt"], coordinator: "claude")
        let model = NewSessionModel(source: source, defaults: MockDefaults())
        await model.load()
        model.selectedModel = "gpt"

        source.models = modelList(["claude", "glm"], coordinator: "claude")
        await model.load()

        XCTAssertEqual(model.selectedModel, "")
    }

    func testListModelsFailureStillAllowsStarting() async {
        // The picker is a convenience: a ListModels failure hides the chip but
        // must not block the composer.
        let source = MockNewSessionSource()
        source.modes = [mode("work")]
        source.modelsError = YccError.rpc(message: "boom")
        let model = NewSessionModel(source: source, defaults: MockDefaults())

        await model.load()

        XCTAssertTrue(model.models.isEmpty)
        XCTAssertFalse(model.showsModelPicker)
        XCTAssertNil(model.errorMessage)
        XCTAssertTrue(model.canStart)      // work mode, empty prompt is fine
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

    func testOnboardingSuggestionTracksSelectedProjectStatus() async {
        let source = MockNewSessionSource()
        source.modes = [mode("pm")]
        source.presets = [preset("onboard"), preset("spec-doctor")]
        source.projects = [
            project("new", needsOnboarding: true),
            project("established", needsOnboarding: false),
        ]
        let model = NewSessionModel(
            source: source, defaults: MockDefaults(), initialProject: "established")

        await model.load()

        XCTAssertEqual(model.suggestedPresets.map(\.name), ["spec-doctor"])
        model.selectedProject = "new"
        XCTAssertEqual(model.suggestedPresets.map(\.name), ["onboard", "spec-doctor"])
    }

    func testOnboardingSuggestionIsHiddenWithoutASelectedProject() async {
        let source = MockNewSessionSource()
        source.modes = [mode("pm")]
        source.presets = [preset("onboard"), preset("spec-doctor")]
        source.projects = [
            project("one", needsOnboarding: true),
            project("two", needsOnboarding: true),
        ]
        let model = NewSessionModel(source: source, defaults: MockDefaults())

        await model.load()

        XCTAssertEqual(model.selectedProject, "")
        XCTAssertEqual(model.suggestedPresets.map(\.name), ["spec-doctor"])
    }

    func testOnboardingSuggestionRemainsWhenOlderDaemonOmitsStatus() async {
        let source = MockNewSessionSource()
        source.modes = [mode("pm")]
        source.presets = [preset("onboard")]
        source.projects = [project("legacy")]
        let model = NewSessionModel(source: source, defaults: MockDefaults())

        await model.load()

        XCTAssertEqual(model.suggestedPresets.map(\.name), ["onboard"])
    }

    func testLoadRecallsRememberedSelections() async {
        let defaults = MockDefaults()
        defaults.lastMode = "pm"
        defaults.lastProject = "two"
        let source = MockNewSessionSource()
        source.modes = [mode("work"), mode("pm")]
        source.projects = [project("one"), project("two")]
        let model = NewSessionModel(source: source, defaults: defaults)

        // Recalled before load, from the defaults store.
        XCTAssertEqual(model.selectedMode, "pm")
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
        XCTAssertEqual(model.selectedProject, "one")  // sole project auto-selected
    }

    func testInitialProjectOverridesRememberedProject() async {
        // The landing screen's current filter must win over the remembered
        // last-used project, so a new session starts where the user is looking.
        let defaults = MockDefaults()
        defaults.lastProject = "two"
        let source = MockNewSessionSource()
        source.modes = [mode("work")]
        source.projects = [project("one"), project("two")]
        let model = NewSessionModel(
            source: source, defaults: defaults, initialProject: "one")

        XCTAssertEqual(model.selectedProject, "one")
        await model.load()
        XCTAssertEqual(model.selectedProject, "one")

        _ = await model.start()
        XCTAssertEqual(source.startArgs?.project, "one")
    }

    // MARK: - Validation

    func testCanStartRequiresModeAndPrompt() async {
        let source = MockNewSessionSource()
        source.modes = [mode("pm")]
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

    func testWorkModeAllowsEmptyPrompt() async {
        // Mirrors the TUI: plain "work" starts without a prompt (the agent picks
        // the next ready backlog task); other modes still require one.
        let source = MockNewSessionSource()
        source.modes = [mode("work"), mode("pm")]
        let model = NewSessionModel(source: source, defaults: MockDefaults())
        await model.load()

        model.selectedMode = "work"
        XCTAssertTrue(model.promptIsOptional)
        XCTAssertTrue(model.canStart)
        _ = await model.start()
        XCTAssertEqual(source.startArgs?.mode, "work")
        XCTAssertEqual(source.startArgs?.prompt, "")

        model.selectedMode = "pm"
        XCTAssertFalse(model.promptIsOptional)
        XCTAssertFalse(model.canStart)
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
        model.selectedProject = "one"
        model.prompt = "  build a thing  "

        let id = await model.start()

        XCTAssertEqual(id, "s_123")
        XCTAssertEqual(source.startArgs?.project, "one")
        XCTAssertEqual(source.startArgs?.mode, "pm")
        XCTAssertEqual(source.startArgs?.prompt, "build a thing")
        // Selections persisted for next time.
        XCTAssertEqual(defaults.lastMode, "pm")
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

    // MARK: - Opening-prompt pictures (task 0257)

    /// Pictures ride along with `StartSession`, so the agent sees the screenshot
    /// on its FIRST turn instead of a turn later via SendInput.
    func testStartSendsAttachedPictures() async {
        let source = MockNewSessionSource()
        source.modes = [mode("chat")]
        let model = NewSessionModel(source: source, defaults: MockDefaults())
        await model.load()
        model.prompt = "what is this?"
        model.images = [MessageImage(
            data: Data([0x89, 0x50, 0x4E, 0x47]), mediaType: "image/png", filename: "shot.png")]

        _ = await model.start()

        XCTAssertEqual(source.startArgs?.images.count, 1)
        XCTAssertEqual(source.startArgs?.images.first?.mediaType, "image/png")
        XCTAssertEqual(source.startArgs?.images.first?.filename, "shot.png")
    }

    /// A picture is itself a prompt: an attachment alone unlocks the send button
    /// even in a mode that normally demands text.
    func testPictureAloneEnablesStart() async {
        let source = MockNewSessionSource()
        source.modes = [mode("chat")]
        let model = NewSessionModel(source: source, defaults: MockDefaults())
        await model.load()
        XCTAssertFalse(model.canStart)          // no text, no picture

        model.images = [MessageImage(data: Data([0xFF]), mediaType: "image/jpeg")]
        XCTAssertTrue(model.canStart)
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
