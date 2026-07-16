import Foundation
import SwiftProtobuf
import XCTest
import YccProto
@testable import YccKit

final class SessionProjectionTests: XCTestCase {
    // MARK: - Fixture loading

    /// Decode the committed real-transcript fixture (wire shape: `dataJson` as an
    /// embedded string, `seq` as an int64-string) into `Event` messages.
    private func loadFixtureEvents() throws -> [Ycc_V1_Event] {
        let url = try XCTUnwrap(
            Bundle.module.url(forResource: "transcript", withExtension: "jsonl", subdirectory: "Fixtures")
                ?? Bundle.module.url(forResource: "transcript", withExtension: "jsonl"),
            "transcript.jsonl fixture not found in test bundle"
        )
        let text = try String(contentsOf: url, encoding: .utf8)
        var events: [Ycc_V1_Event] = []
        for line in text.split(separator: "\n") {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.isEmpty { continue }
            events.append(try Ycc_V1_Event(jsonString: trimmed))
        }
        return events
    }

    private func makeEvent(
        seq: Int64,
        type: String,
        actor: String = "coordinator",
        dataJson: String = "",
        transient: Bool = false,
        ts: String = ""
    ) -> Ycc_V1_Event {
        var e = Ycc_V1_Event()
        e.seq = seq
        e.type = type
        e.actor = actor
        e.dataJson = dataJson
        e.transient = transient
        e.ts = ts
        return e
    }

    private func delta(_ text: String, done: Bool = false) -> Ycc_V1_Event {
        let payload: String
        if done {
            payload = #"{"text":"","done":true}"#
        } else {
            let escaped = text.replacingOccurrences(of: "\"", with: "\\\"")
            payload = "{\"text\":\"\(escaped)\"}"
        }
        return makeEvent(seq: 0, type: "turn_delta", dataJson: payload, transient: true)
    }

    // MARK: - Fixture sanity

    func testFixtureLoadsAndFolds() throws {
        let events = try loadFixtureEvents()
        XCTAssertGreaterThan(events.count, 10, "fixture should carry a real mix of events")

        var proj = SessionProjection()
        proj.apply(events)

        XCTAssertFalse(proj.durableRows.isEmpty)
        // The last durable event in the fixture is session_idle → a final report.
        XCTAssertEqual(proj.lastPersistedSeq, events.map(\.seq).max())
        // A user_input bubble and at least one tool row must be present.
        XCTAssertTrue(proj.rows.contains { if case .userMessage = $0.kind { return true }; return false })
        XCTAssertTrue(proj.rows.contains { if case .tool = $0.kind { return true }; return false })
        XCTAssertTrue(proj.rows.contains { if case .finalReport = $0.kind { return true }; return false })
    }

    // MARK: - Acceptance: one-pass == disconnect + replay-from-seq

    func testDisconnectReplayFromSeqMatchesOnePass() throws {
        let durable = try loadFixtureEvents().sorted { $0.seq < $1.seq }

        // Find the first model_turn with real text (a streaming turn) and inject
        // transient turn_delta snapshots just before it, as a live stream would.
        let turnIdx = try XCTUnwrap(durable.firstIndex {
            $0.type == "model_turn"
                && !(SessionProjection.text(SessionProjection.parse($0.dataJson)).isEmpty)
        })
        let turn = durable[turnIdx]
        let full = SessionProjection.text(SessionProjection.parse(turn.dataJson))
        let deltas = [
            delta(String(full.prefix(5))),
            delta(String(full.prefix(15))),
            delta(full),
        ]

        // The full live sequence: durable events with the deltas interleaved
        // immediately before their model_turn.
        var live: [Ycc_V1_Event] = []
        for (i, e) in durable.enumerated() {
            if i == turnIdx { live.append(contentsOf: deltas) }
            live.append(e)
        }

        // One-pass fold of the whole live stream.
        var onePass = SessionProjection()
        onePass.apply(live)

        // Disconnect mid-turn: fold the prefix up to (but not including) the
        // model_turn — the live tail is showing and its durable turn hasn't
        // arrived yet. Then reconnect and replay only seq > lastPersistedSeq.
        let cutIndex = try XCTUnwrap(live.firstIndex { $0.seq == turn.seq })
        var replayed = SessionProjection()
        replayed.apply(live[0..<cutIndex])

        XCTAssertNotNil(replayed.liveTail, "prefix should end mid-turn with a live tail")
        let resumeSeq = replayed.lastPersistedSeq
        XCTAssertLessThan(resumeSeq, turn.seq)

        // Server replays durable events with seq > resumeSeq (never transient).
        let suffix = durable.filter { $0.seq > resumeSeq }
        replayed.apply(suffix)

        // Identical durable state, cursor, and no lingering tail on either path.
        XCTAssertEqual(replayed.durableRows, onePass.durableRows)
        XCTAssertEqual(replayed.lastPersistedSeq, onePass.lastPersistedSeq)
        XCTAssertNil(onePass.liveTail)
        XCTAssertNil(replayed.liveTail)
        XCTAssertEqual(replayed.rows, onePass.rows)
    }

    func testReplayToleratesOverlappingRedelivery() throws {
        let durable = try loadFixtureEvents().sorted { $0.seq < $1.seq }
        var proj = SessionProjection()
        proj.apply(durable)
        let rowsBefore = proj.durableRows
        let cursorBefore = proj.lastPersistedSeq

        // Re-deliver the whole log (inclusive/overlapping replay) — must be a
        // no-op: no duplicated rows, unchanged cursor.
        proj.apply(durable)
        XCTAssertEqual(proj.durableRows, rowsBefore)
        XCTAssertEqual(proj.lastPersistedSeq, cursorBefore)
    }

    // MARK: - Live tail behavior

    func testLiveTailReplacedThenClearedByDone() {
        var proj = SessionProjection()
        proj.apply(delta("Hel"))
        XCTAssertEqual(proj.rows.count, 1)
        guard case .liveTail(let t1)? = proj.rows.last?.kind else {
            return XCTFail("expected a live-tail row")
        }
        XCTAssertEqual(t1, "Hel")
        let idA = proj.rows.last?.id

        // Successive delta replaces (does not append) the tail.
        proj.apply(delta("Hello world"))
        XCTAssertEqual(proj.rows.count, 1)
        XCTAssertEqual(proj.rows.last?.id, idA, "tail row keeps a stable id")
        guard case .liveTail(let t2)? = proj.rows.last?.kind else {
            return XCTFail("expected a live-tail row")
        }
        XCTAssertEqual(t2, "Hello world")

        // Terminating delta clears the tail entirely.
        proj.apply(delta("", done: true))
        XCTAssertTrue(proj.rows.isEmpty)
        XCTAssertNil(proj.liveTail)
        XCTAssertEqual(proj.lastPersistedSeq, 0, "transient events never advance the cursor")
    }

    func testModelTurnClearsTailAndAppendsBubble() {
        var proj = SessionProjection()
        proj.apply(delta("partial answer so"))
        XCTAssertNotNil(proj.liveTail)

        proj.apply(makeEvent(seq: 7, type: "model_turn", dataJson: #"{"text":"final answer"}"#))
        XCTAssertNil(proj.liveTail, "durable model_turn clears the live tail")
        XCTAssertEqual(proj.durableRows.count, 1)
        guard case .modelMessage(let text)? = proj.durableRows.last?.kind else {
            return XCTFail("expected a model bubble")
        }
        XCTAssertEqual(text, "final answer")
        XCTAssertEqual(proj.lastPersistedSeq, 7)
    }

    func testEmptyModelTurnAppendsNoBubble() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 4, type: "model_turn", dataJson: #"{"text":"","tool_calls":1}"#))
        XCTAssertTrue(proj.durableRows.isEmpty, "a tool-use turn has no bubble")
        XCTAssertEqual(proj.lastPersistedSeq, 4)
    }

    // MARK: - Tolerance

    func testSeqlessTransientNeverAdvancesCursor() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 3, type: "user_input", dataJson: #"{"text":"hi"}"#))
        XCTAssertEqual(proj.lastPersistedSeq, 3)
        // Unknown transient type — ignored, cursor unchanged.
        proj.apply(makeEvent(seq: 0, type: "presence_ping", transient: true))
        proj.apply(delta("streaming"))
        XCTAssertEqual(proj.lastPersistedSeq, 3)
    }

    func testUnknownTypeBecomesGenericSystemRow() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 5, type: "future_widget", dataJson: #"{"text":"hello from the future"}"#))
        XCTAssertEqual(proj.durableRows.count, 1)
        guard case .system(let text)? = proj.durableRows.last?.kind else {
            return XCTFail("unknown type should degrade to a system row")
        }
        XCTAssertTrue(text.contains("future widget"))
        XCTAssertTrue(text.contains("hello from the future"))
    }

    func testMalformedDataJsonDoesNotCrash() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "user_input", dataJson: "{not valid json"))
        proj.apply(makeEvent(seq: 2, type: "model_turn", dataJson: ""))
        // Degrades to empty text; user_input bubble still appears (empty text).
        XCTAssertEqual(proj.lastPersistedSeq, 2)
        guard case .userMessage(let text)? = proj.durableRows.first?.kind else {
            return XCTFail("expected a user bubble")
        }
        XCTAssertEqual(text, "")
    }

    func testToolCallResultPairing() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "tool_call",
                             dataJson: #"{"id":"t1","name":"list_backlog","args":"{}"}"#))
        XCTAssertEqual(proj.durableRows.count, 1)
        guard case .tool(_, let s1, _, _)? = proj.durableRows.last?.kind else {
            return XCTFail("expected a tool row")
        }
        XCTAssertEqual(s1, .running)

        proj.apply(makeEvent(seq: 2, type: "tool_result",
                             dataJson: #"{"id":"t1","name":"list_backlog","result":"ok done","error":false}"#))
        XCTAssertEqual(proj.durableRows.count, 1, "result pairs into the call row, not a new row")
        guard case .tool(let name, let s2, let args, let output)? = proj.durableRows.last?.kind else {
            return XCTFail("expected a tool row")
        }
        XCTAssertEqual(name, "list_backlog")
        XCTAssertEqual(s2, .ok)
        XCTAssertEqual(args, "{}")
        XCTAssertEqual(output, "ok done")
    }

    func testOrphanToolResult() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "tool_result",
                             dataJson: #"{"id":"z9","name":"grep","result":"boom","error":true}"#))
        XCTAssertEqual(proj.durableRows.count, 1)
        guard case .tool(let name, let status, _, let output)? = proj.durableRows.last?.kind else {
            return XCTFail("orphan result should still render a tool row")
        }
        XCTAssertEqual(name, "grep")
        XCTAssertEqual(status, .error)
        XCTAssertEqual(output, "boom")
    }

    func testQuestionAskedThenAnswered() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "question_asked",
                             dataJson: #"{"question":"Proceed?","options":["yes","no"]}"#))
        XCTAssertNotNil(proj.pendingQuestion)
        XCTAssertEqual(proj.pendingQuestion?.prompt, "Proceed?")
        XCTAssertEqual(proj.pendingQuestion?.options, ["yes", "no"])
        guard case .question(_, _, let answer1)? = proj.durableRows.last?.kind else {
            return XCTFail("expected a question row")
        }
        XCTAssertNil(answer1)

        proj.apply(makeEvent(seq: 2, type: "question_answered", dataJson: #"{"answer":"yes"}"#))
        XCTAssertNil(proj.pendingQuestion, "answered question clears the pending state")
        guard case .question(_, _, let answer2)? = proj.durableRows.last?.kind else {
            return XCTFail("expected a resolved question row")
        }
        XCTAssertEqual(answer2, "yes")
    }

    func testBatchQuestionShape() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "question_asked",
                             dataJson: #"{"questions":[{"question":"First?"},{"question":"Second?"}]}"#))
        XCTAssertEqual(proj.pendingQuestion?.prompt, "First? (+1 more)")
    }

    func testBatchQuestionParsesEveryQuestionAndOptions() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "question_asked", dataJson: #"""
        {"questions":[{"question":"Which DB?","options":["pg","sqlite"]},{"question":"Deadline?"}]}
        """#))
        let pending = proj.pendingQuestion
        XCTAssertEqual(pending?.questions.count, 2)
        XCTAssertTrue(pending?.isBatch ?? false)
        XCTAssertEqual(pending?.questions.first?.prompt, "Which DB?")
        XCTAssertEqual(pending?.questions.first?.options, ["pg", "sqlite"])
        XCTAssertEqual(pending?.questions.last?.prompt, "Deadline?")
        XCTAssertEqual(pending?.questions.last?.options, [])
    }

    func testSingleQuestionCarriesOneQuestionEntry() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "question_asked",
                             dataJson: #"{"question":"Proceed?","options":["yes","no"]}"#))
        XCTAssertEqual(proj.pendingQuestion?.questions.count, 1)
        XCTAssertFalse(proj.pendingQuestion?.isBatch ?? true)
        XCTAssertEqual(proj.pendingQuestion?.questions.first?.options, ["yes", "no"])
    }

    func testBatchQuestionAnsweredClearsPendingAndResolvesRow() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "question_asked",
                             dataJson: #"{"questions":[{"question":"First?"},{"question":"Second?"}]}"#))
        proj.apply(makeEvent(seq: 2, type: "question_answered",
                             dataJson: #"{"answers":["a","b"]}"#))
        XCTAssertNil(proj.pendingQuestion)
        guard case .question(_, _, let answer)? = proj.durableRows.last?.kind else {
            return XCTFail("expected a resolved question row")
        }
        XCTAssertEqual(answer, "a; b")
    }

    // MARK: - Final report

    func testSessionIdleCreatesMarkdownFinalReportAndCoalescesEchoedTurn() {
        var proj = SessionProjection()
        proj.apply(makeEvent(
            seq: 1, type: "model_turn",
            dataJson: #"{"text":"Shipped it."}"#))
        proj.apply(makeEvent(
            seq: 2, type: "session_idle",
            dataJson: #"{"report":"Shipped it.\n\n## Verification\n\n- tests green"}"#))

        XCTAssertEqual(proj.durableRows.count, 1, "echoed model bubble should fold into final report")
        guard case .finalReport(let text)? = proj.durableRows.last?.kind else {
            return XCTFail("session_idle.report should project as a dedicated final report")
        }
        XCTAssertTrue(text.contains("Shipped it."))
        XCTAssertTrue(text.contains("## Verification"))
        XCTAssertTrue(text.contains("- tests green"))
        XCTAssertEqual(proj.phase, .idle)
    }

    func testDifferingSessionIdleReportPreservesModelTurn() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "model_turn", dataJson: #"{"text":"Wrapping up."}"#))
        proj.apply(makeEvent(seq: 2, type: "session_idle", dataJson: #"{"report":"Completed task 42."}"#))

        XCTAssertEqual(proj.durableRows.count, 2)
        guard case .modelMessage = proj.durableRows[0].kind,
              case .finalReport(let report) = proj.durableRows[1].kind else {
            return XCTFail("differing turn and finish report should both remain")
        }
        XCTAssertEqual(report, "Completed task 42.")
    }

    func testEmptySessionIdleReportFallsBackToSystemFinishRow() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "session_idle"))
        guard case .system(let text)? = proj.durableRows.last?.kind else {
            return XCTFail("empty finish report should still produce a lifecycle row")
        }
        XCTAssertEqual(text, "Session finished")
    }

    // MARK: - Phase folding

    func testPhaseDefaultsToRunning() {
        let proj = SessionProjection()
        XCTAssertEqual(proj.phase, .running)
    }

    func testPhaseTransitions() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "interrupted"))
        XCTAssertEqual(proj.phase, .paused)

        proj.apply(makeEvent(seq: 2, type: "resumed"))
        XCTAssertEqual(proj.phase, .running)

        proj.apply(makeEvent(seq: 3, type: "session_idle"))
        XCTAssertEqual(proj.phase, .idle)

        // Fresh activity clears the idle banner.
        proj.apply(makeEvent(seq: 4, type: "user_input", dataJson: #"{"text":"go"}"#))
        XCTAssertEqual(proj.phase, .running)

        proj.apply(makeEvent(seq: 5, type: "session_error", dataJson: #"{"msg":"boom"}"#))
        XCTAssertEqual(proj.phase, .error("boom"))

        proj.apply(makeEvent(seq: 6, type: "session_stopped"))
        XCTAssertEqual(proj.phase, .stopped)
    }

    func testSessionErrorReadsMsgWithFallbacks() {
        // Production shape: the daemon emits the message under "msg".
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "session_error", dataJson: #"{"msg":"kaboom"}"#))
        XCTAssertEqual(proj.phase, .error("kaboom"))
        guard case .system(let text)? = proj.durableRows.last?.kind else {
            return XCTFail("session_error should render a system row")
        }
        XCTAssertEqual(text, "Session error: kaboom")

        // Fallback to the legacy "error" key.
        var errProj = SessionProjection()
        errProj.apply(makeEvent(seq: 1, type: "session_error", dataJson: #"{"error":"legacy"}"#))
        XCTAssertEqual(errProj.phase, .error("legacy"))

        // Fallback to "text".
        var textProj = SessionProjection()
        textProj.apply(makeEvent(seq: 1, type: "session_error", dataJson: #"{"text":"tail"}"#))
        XCTAssertEqual(textProj.phase, .error("tail"))
    }

    func testInterruptedRendersSystemRow() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "interrupted"))
        guard case .system(let text)? = proj.durableRows.last?.kind else {
            return XCTFail("interrupted should render a system row")
        }
        XCTAssertEqual(text, "Interrupted")
    }

    // MARK: - Commit rows (task 0189)

    func testCommitMadeExposesShaForDrillIn() {
        var proj = SessionProjection()
        proj.apply(makeEvent(
            seq: 1, type: "commit_made",
            dataJson: #"{"sha":"abc123def","message":"do the thing"}"#))
        guard case .commit(let text, let sha)? = proj.durableRows.last?.kind else {
            return XCTFail("commit_made should render a commit row")
        }
        XCTAssertEqual(sha, "abc123def")
        XCTAssertEqual(text, "Committed abc123def: do the thing")
    }

    func testCommitMadeWithoutShaStillRendersRow() {
        var proj = SessionProjection()
        proj.apply(makeEvent(
            seq: 1, type: "commit_made",
            dataJson: #"{"message":"no sha here"}"#))
        guard case .commit(_, let sha)? = proj.durableRows.last?.kind else {
            return XCTFail("commit_made should render a commit row even without a sha")
        }
        XCTAssertEqual(sha, "")
    }

    // MARK: - Interaction level tracking (task 0187)

    func testInteractionLevelSeededFromSessionStarted() {
        var proj = SessionProjection()
        XCTAssertNil(proj.interactionLevel)
        proj.apply(makeEvent(
            seq: 1, type: "session_started",
            dataJson: #"{"mode":"work","interaction_level":"judgement"}"#))
        XCTAssertEqual(proj.interactionLevel, "judgement")
    }

    func testInteractionLevelUpdatedByChangeEvent() {
        var proj = SessionProjection()
        proj.apply(makeEvent(
            seq: 1, type: "session_started",
            dataJson: #"{"interaction_level":"judgement"}"#))
        proj.apply(makeEvent(
            seq: 2, type: "interaction_level_changed",
            dataJson: #"{"from":"judgement","to":"autonomous"}"#))
        XCTAssertEqual(proj.interactionLevel, "autonomous")
        // …and renders a readable system row.
        guard case .system(let text)? = proj.durableRows.last?.kind else {
            return XCTFail("interaction_level_changed should render a system row")
        }
        XCTAssertEqual(text, "Interaction level → autonomous")
    }

    func testRoleConfigChangedRendersSystemRow() {
        var proj = SessionProjection()
        proj.apply(makeEvent(
            seq: 1, type: "role_config_changed",
            dataJson: #"{"coordinator":"claude","implementer":"gpt","reviewers":["glm","gpt"]}"#))
        guard case .system(let text)? = proj.durableRows.last?.kind else {
            return XCTFail("role_config_changed should render a system row")
        }
        XCTAssertEqual(text, "Roles: coordinator claude · implementer gpt · reviewers glm, gpt")
    }

    func testThinkingLevelChangedRendersSystemRow() {
        var proj = SessionProjection()
        proj.apply(makeEvent(
            seq: 1, type: "thinking_level_changed",
            dataJson: #"{"role":"all","from":"medium","to":"high"}"#))
        guard case .system(let text)? = proj.durableRows.last?.kind else {
            return XCTFail("thinking_level_changed should render a system row")
        }
        XCTAssertEqual(text, "Thinking (all roles) → high")
    }
}
