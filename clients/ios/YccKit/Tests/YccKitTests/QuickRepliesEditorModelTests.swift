import XCTest
import YccProto
@testable import YccKit

private final class MockQuickRepliesSource: QuickRepliesSource, @unchecked Sendable {
    var stored: [Ycc_V1_QuickReply] = []
    var projects: [String: [Ycc_V1_QuickReply]] = [:]
    var projectOrder: [String] = []
    var brokenProjects: Set<String> = []
    var error: Error?
    private(set) var sent: [Ycc_V1_QuickReply]?
    private(set) var sentScopes: [QuickReplyScope] = []

    func listProjectNames() async throws -> [String] { projectOrder }
    func getGlobalQuickReplies() async throws -> [Ycc_V1_QuickReply] {
        if let error { throw error }
        return stored
    }
    func getProjectQuickReplies(project: String) async throws -> [Ycc_V1_QuickReply] {
        if brokenProjects.contains(project) { throw YccError.rpc(message: "unreadable") }
        return projects[project] ?? []
    }
    func setQuickReplies(_ replies: [Ycc_V1_QuickReply], scope: QuickReplyScope) async throws -> [Ycc_V1_QuickReply] {
        if let error { throw error }
        sentScopes.append(scope)
        // Like the daemon: trimmed, and only default ids survive (daemon-wide list).
        let clean = replies.map { reply in
            var out = reply
            out.label = reply.label.trimmingCharacters(in: .whitespaces)
            if scope != .global || !["go_ahead", "continue", "commit_push"].contains(reply.id) { out.id = "" }
            return out
        }
        switch scope {
        case .global:
            sent = replies
            stored = clean
        case .project(let name):
            projects[name] = clean
        }
        return clean
    }
}

private func reply(_ id: String, _ label: String, _ text: String) -> Ycc_V1_QuickReply {
    var reply = Ycc_V1_QuickReply()
    reply.id = id
    reply.label = label
    reply.text = text
    return reply
}

@MainActor
final class QuickRepliesEditorModelTests: XCTestCase {
    func testSaveSendsDefaultIDsAndAdoptsStoredList() async {
        let source = MockQuickRepliesSource()
        source.stored = [reply("go_ahead", "Go ahead", "Go ahead"), reply("continue", "Continue", "Continue")]
        let model = QuickRepliesEditorModel(source: source)
        await model.load()
        XCTAssertFalse(model.isDirty)
        XCTAssertFalse(model.canSave)

        model.rows[0].text = "Go ahead, then run the tests"
        model.rows.remove(at: 1)
        model.add(QuickReplyDraft(label: "Deploy ", text: "Deploy to prod", icon: "send"))
        XCTAssertTrue(model.canSave)

        let ok = await model.save()
        XCTAssertTrue(ok)
        XCTAssertEqual(source.sent?.map(\.id), ["go_ahead", ""], "an edited default keeps its id")
        XCTAssertEqual(model.rows.map(\.label), ["Go ahead", "Deploy"])
        XCTAssertEqual(model.rows.map(\.icon), ["", "send"])
        XCTAssertEqual(source.sentScopes, [.global], "unchanged lists are not written")
        XCTAssertFalse(model.isDirty, "the stored list becomes the baseline")
    }

    func testInvalidRowsBlockSaveAndDaemonErrorIsShown() async {
        let source = MockQuickRepliesSource()
        source.stored = [reply("go_ahead", "Go ahead", "Go ahead")]
        let model = QuickRepliesEditorModel(source: source)
        await model.load()

        model.rows[0].label = "  "
        XCTAssertNotNil(model.rows[0].problem)
        XCTAssertFalse(model.canSave)
        model.rows[0].label = String(repeating: "é", count: QuickRepliesEditorModel.maxLabelLength)
        XCTAssertNil(model.rows[0].problem, "32 characters fit even when they are multi-byte")
        model.rows[0].text = String(repeating: "x", count: QuickRepliesEditorModel.maxTextBytes + 1)
        XCTAssertFalse(model.canSave)
        model.rows[0].text = "Ship it"

        source.error = YccError.rpc(message: "invalid quick replies: at most 12 replies")
        let ok = await model.save()
        XCTAssertFalse(ok)
        XCTAssertEqual(model.errorMessage, "invalid quick replies: at most 12 replies")
        XCTAssertTrue(model.isDirty, "a failed save keeps the edits")

        source.error = nil
        source.stored = [reply("", "Other", "Other")]
        await model.load()
        XCTAssertEqual(model.rows.map(\.text), ["Ship it"], "a refresh never discards unsaved edits")
        model.revert()
        XCTAssertEqual(model.rows.map(\.label), ["Go ahead"])
    }

    func testRepliesMoveBetweenListsAndEachChangedListIsSaved() async {
        let source = MockQuickRepliesSource()
        source.stored = [reply("go_ahead", "Go ahead", "Go ahead"), reply("continue", "Continue", "Continue")]
        source.projectOrder = ["alpha", "beta", "gamma"]
        source.projects = ["alpha": [reply("", "Deploy", "Deploy it"), reply("", "Lint", "Run lint")]]
        source.brokenProjects = ["gamma"]
        let model = QuickRepliesEditorModel(source: source)
        await model.load()
        XCTAssertEqual(model.projects, ["alpha", "beta"])
        XCTAssertEqual(model.unavailableProjects, ["gamma"])
        XCTAssertEqual(model.visibleScopes, [.global, .project("alpha")], "a project shows once it has replies")

        let goAhead = model.rows[0].id
        model.setScope(.project("beta"), forRow: goAhead)
        XCTAssertEqual(model.visibleScopes, [.global, .project("alpha"), .project("beta")])
        model.move(in: .project("alpha"), fromOffsets: [1], toOffset: 0)
        XCTAssertEqual(model.rows(in: .project("alpha")).map(\.label), ["Lint", "Deploy"])
        model.remove(in: .global, atOffsets: [0])
        XCTAssertEqual(model.rows(in: .global).count, 0)

        let ok = await model.save()
        XCTAssertTrue(ok)
        XCTAssertEqual(Set(source.sentScopes), [.global, .project("alpha"), .project("beta")])
        XCTAssertEqual(source.stored.count, 0)
        XCTAssertEqual(source.projects["alpha"]?.map(\.label), ["Lint", "Deploy"])
        XCTAssertEqual(source.projects["beta"]?.map(\.label), ["Go ahead"])
        XCTAssertFalse(model.isDirty)
    }

    func testNewReplyGoesToTheChosenListAndRespectsItsLimit() async {
        let source = MockQuickRepliesSource()
        source.projectOrder = ["alpha"]
        source.projects = ["alpha": (0..<QuickRepliesEditorModel.maxReplies).map { reply("", "R\($0)", "R\($0)") }]
        let model = QuickRepliesEditorModel(source: source)
        await model.load()
        XCTAssertTrue(model.canAdd(to: .global))
        XCTAssertFalse(model.canAdd(to: .project("alpha")))
        model.add(QuickReplyDraft(label: "Extra", text: "Extra", scope: .project("alpha")))
        XCTAssertFalse(model.canSave, "a list over the limit cannot be saved")
    }

    func testTypingALabelFillsTheMessageUntilTheMessageIsEdited() {
        var draft = QuickReplyDraft()
        draft.labelFillingMessage = "Dep"
        draft.labelFillingMessage = "Deploy"
        XCTAssertEqual(draft.text, "Deploy")
        draft.message = "Deploy to prod"
        draft.labelFillingMessage = "Ship"
        XCTAssertEqual(draft.label, "Ship")
        XCTAssertEqual(draft.text, "Deploy to prod", "an edited message stays put")

        var existing = QuickReplyDraft(label: "Continue", text: "Continue")
        existing.labelFillingMessage = "Keep going"
        XCTAssertEqual(existing.text, "Continue", "renaming a saved reply leaves its message alone")
    }

    func testIconsResolveKnownNamesThenDefaultsThenFallback() {
        XCTAssertEqual(QuickReplyIcons.resolved(icon: "bug", replyID: "").symbol, "ladybug")
        XCTAssertEqual(QuickReplyIcons.resolved(icon: "", replyID: "continue").name, "forward")
        XCTAssertEqual(QuickReplyIcons.resolved(icon: "from_a_newer_client", replyID: "").name, QuickReplyIcons.fallback)
        XCTAssertEqual(Set(QuickReplyIcons.all.map(\.name)).count, QuickReplyIcons.all.count)
    }

    func testAnalyticsKindNeverCarriesUserText() {
        XCTAssertEqual(QuickReplyChoice(reply: reply("continue", "Go on", "Go on"), isGlobal: true).analyticsKind, "continue")
        XCTAssertEqual(QuickReplyChoice(reply: reply("", "Deploy", "Deploy"), isGlobal: true).analyticsKind, "global")
        XCTAssertEqual(QuickReplyChoice(reply: reply("", "Deploy", "Deploy"), isGlobal: false).analyticsKind, "project")
    }
}
