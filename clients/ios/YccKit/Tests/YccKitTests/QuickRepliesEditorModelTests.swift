import XCTest
import YccProto
@testable import YccKit

private final class MockQuickRepliesSource: QuickRepliesSource, @unchecked Sendable {
    var stored: [Ycc_V1_QuickReply] = []
    var error: Error?
    private(set) var sent: [Ycc_V1_QuickReply]?

    func getGlobalQuickReplies() async throws -> [Ycc_V1_QuickReply] {
        if let error { throw error }
        return stored
    }
    func setGlobalQuickReplies(_ replies: [Ycc_V1_QuickReply]) async throws -> [Ycc_V1_QuickReply] {
        if let error { throw error }
        sent = replies
        // Like the daemon: trimmed, and only default ids survive.
        stored = replies.map { reply in
            var out = reply
            out.label = reply.label.trimmingCharacters(in: .whitespaces)
            if !["go_ahead", "continue", "commit_push"].contains(reply.id) { out.id = "" }
            return out
        }
        return stored
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
        model.rows.append(QuickReplyDraft(label: "Deploy ", text: "Deploy to prod"))
        XCTAssertTrue(model.canSave)

        let ok = await model.save()
        XCTAssertTrue(ok)
        XCTAssertEqual(source.sent?.map(\.id), ["go_ahead", ""], "an edited default keeps its id")
        XCTAssertEqual(model.rows.map(\.label), ["Go ahead", "Deploy"])
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

    func testAnalyticsKindNeverCarriesUserText() {
        XCTAssertEqual(QuickReplyChoice(reply: reply("continue", "Go on", "Go on"), isGlobal: true).analyticsKind, "continue")
        XCTAssertEqual(QuickReplyChoice(reply: reply("", "Deploy", "Deploy"), isGlobal: true).analyticsKind, "global")
        XCTAssertEqual(QuickReplyChoice(reply: reply("", "Deploy", "Deploy"), isGlobal: false).analyticsKind, "project")
    }
}
