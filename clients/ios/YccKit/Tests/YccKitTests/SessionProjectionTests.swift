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

    private func delta(
        _ text: String, actor: String = "coordinator", done: Bool = false
    ) -> Ycc_V1_Event {
        let payload: String
        if done {
            payload = #"{"text":"","done":true}"#
        } else {
            let escaped = text.replacingOccurrences(of: "\"", with: "\\\"")
            payload = "{\"text\":\"\(escaped)\"}"
        }
        return makeEvent(
            seq: 0, type: "turn_delta", actor: actor,
            dataJson: payload, transient: true)
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

    func testUserPictureMetadataProjectsRetainedReference() {
        var projection = SessionProjection()
        projection.apply(makeEvent(
            seq: 1, type: "user_input", actor: "user",
            dataJson: #"{"text":"look at this","images":[{"attachment_id":"a_123","media_type":"image/jpeg","filename":"photo.jpg"}]}"#))
        guard let first = projection.rows.first,
              case .userMessage(let text, let pictures) = first.kind else {
            return XCTFail("expected user message")
        }
        XCTAssertEqual(text, "look at this")
        XCTAssertEqual(pictures, [.init(attachmentID: "a_123", mediaType: "image/jpeg", filename: "photo.jpg")])
        XCTAssertFalse(text.contains("base64"))
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

    func testConcurrentActorTailsUpdateIndependentlyInStableOrder() {
        var proj = SessionProjection()
        proj.apply(delta("implementing", actor: "implementer-1"))
        proj.apply(delta("reviewing", actor: "reviewer-1"))

        XCTAssertEqual(proj.liveTails.map(\.actor), ["implementer-1", "reviewer-1"])
        XCTAssertEqual(Set(proj.liveTails.map(\.id)).count, 2)
        let reviewerBefore = proj.liveTails[1]

        // Interleaving a newer implementer snapshot updates that stable row in
        // place rather than replacing or moving the reviewer's live output.
        proj.apply(delta("implementing more", actor: "implementer-1"))
        XCTAssertEqual(proj.liveTails.map(\.actor), ["implementer-1", "reviewer-1"])
        XCTAssertEqual(proj.liveTails[1], reviewerBefore)
        guard case .liveTail(let text) = proj.liveTails[0].kind else {
            return XCTFail("expected implementer live tail")
        }
        XCTAssertEqual(text, "implementing more")
        XCTAssertEqual(proj.lastPersistedSeq, 0)
    }

    func testActorCompletionClearsOnlyMatchingLiveTail() {
        var proj = SessionProjection()
        proj.apply(delta("implementing", actor: "implementer-1"))
        proj.apply(delta("reviewing", actor: "reviewer-1"))

        proj.apply(delta("", actor: "implementer-1", done: true))
        XCTAssertEqual(proj.liveTails.map(\.actor), ["reviewer-1"])

        // Durable completion is also actor-scoped in case its redundant terminal
        // transient delta was dropped under backpressure.
        proj.apply(makeEvent(
            seq: 1, type: "model_turn", actor: "reviewer-1",
            dataJson: #"{"text":"review complete"}"#))
        XCTAssertTrue(proj.liveTails.isEmpty)
        XCTAssertEqual(proj.durableRows.last?.actor, "reviewer-1")
    }

    func testDurableErrorClearsOnlyMatchingLiveTail() {
        var proj = SessionProjection()
        proj.apply(delta("implementing", actor: "implementer-1"))
        proj.apply(delta("reviewing", actor: "reviewer-1"))

        proj.apply(makeEvent(
            seq: 1, type: "session_error", actor: "reviewer-1",
            dataJson: #"{"error":"review failed"}"#))

        XCTAssertEqual(proj.liveTails.map(\.actor), ["implementer-1"])
    }

    func testSessionCompletionClearsEveryLiveTail() {
        var proj = SessionProjection()
        proj.apply(delta("implementing", actor: "implementer-1"))
        proj.apply(delta("reviewing", actor: "reviewer-1"))

        proj.apply(makeEvent(
            seq: 1, type: "session_idle", actor: "coordinator",
            dataJson: #"{"report":"finished"}"#))

        XCTAssertTrue(proj.liveTails.isEmpty)
        XCTAssertEqual(proj.lastPersistedSeq, 1)
        guard case .finalReport(let text)? = proj.durableRows.last?.kind else {
            return XCTFail("expected final report")
        }
        XCTAssertEqual(text, "finished")
    }

    func testLegacyEmptyActorPairsWithCoordinatorCompletion() {
        var proj = SessionProjection()
        proj.apply(delta("legacy coordinator output", actor: ""))
        XCTAssertEqual(proj.liveTails.first?.actor, "coordinator")
        XCTAssertEqual(proj.liveTails.first?.id, SessionProjection.liveTailID)

        proj.apply(makeEvent(
            seq: 1, type: "model_turn", actor: "coordinator",
            dataJson: #"{"text":"done"}"#))
        XCTAssertTrue(proj.liveTails.isEmpty)
    }

    func testClearLiveTailsPreservesDurableCursor() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 3, type: "user_input", dataJson: #"{"text":"hi"}"#))
        proj.apply(delta("implementing", actor: "implementer-1"))
        proj.apply(delta("reviewing", actor: "reviewer-1"))

        proj.clearLiveTails()

        XCTAssertTrue(proj.liveTails.isEmpty)
        XCTAssertEqual(proj.lastPersistedSeq, 3)
        XCTAssertEqual(proj.durableRows.count, 1)
    }

    func testDurableTurnDoesNotClearOtherActorsTail() {
        var proj = SessionProjection()
        proj.apply(delta("implementing", actor: "implementer-1"))
        proj.apply(delta("reviewing", actor: "reviewer-1"))

        proj.apply(makeEvent(
            seq: 1, type: "model_turn", actor: "implementer-1",
            dataJson: #"{"text":"implementation complete"}"#))

        XCTAssertEqual(proj.liveTails.map(\.actor), ["reviewer-1"])
        XCTAssertEqual(proj.liveTails.first?.id, "live-tail:reviewer-1")
    }

    // MARK: - Subagent plant identities

    func testSubagentPlantIdentityPrefixesEveryActorOwnedRowKind() throws {
        var proj = SessionProjection()
        let actor = "agent:agent_1"
        proj.apply(makeEvent(
            seq: 1, type: "subagent_spawned", actor: "coordinator",
            dataJson: #"{"role":"generic","agent_id":"agent_1","model":"claude"}"#))
        proj.apply(makeEvent(
            seq: 2, type: "model_turn", actor: actor,
            dataJson: #"{"text":"I found it"}"#))
        proj.apply(makeEvent(
            seq: 3, type: "thinking", actor: actor,
            dataJson: #"{"text":"checking"}"#))
        proj.apply(makeEvent(
            seq: 4, type: "tool_call", actor: actor,
            dataJson: #"{"id":"call-1","name":"Read","args":"{}"}"#))
        proj.apply(delta("still working", actor: actor))

        let emoji = try XCTUnwrap(proj.durableRows.first?.actorEmoji)
        XCTAssertFalse(emoji.isEmpty)
        XCTAssertEqual(proj.durableRows.map(\.actor), Array(repeating: actor, count: 4))
        XCTAssertEqual(proj.durableRows.map(\.actorEmoji), Array(repeating: emoji, count: 4))
        XCTAssertEqual(proj.liveTails.first?.actorEmoji, emoji)
    }

    func testReviewerLifecycleRowsReusePlantWithoutRepeatingActor() throws {
        var proj = SessionProjection()
        let actor = "reviewer:opus"
        proj.apply(makeEvent(
            seq: 1, type: "subagent_spawned", actor: "coordinator",
            dataJson: #"{"role":"reviewer","model":"opus"}"#))
        proj.apply(makeEvent(
            seq: 2, type: "model_turn", actor: actor,
            dataJson: #"{"text":"reviewing"}"#))
        proj.apply(makeEvent(
            seq: 3, type: "review_submitted", actor: "coordinator",
            dataJson: #"{"model":"opus","summary":"Looks good"}"#))
        proj.apply(makeEvent(
            seq: 4, type: "subagent_finished", actor: "coordinator",
            dataJson: #"{"role":"reviewer","model":"opus"}"#))

        let emoji = try XCTUnwrap(proj.durableRows.first?.actorEmoji)
        XCTAssertEqual(proj.durableRows.map(\.actor), Array(repeating: actor, count: 4))
        XCTAssertEqual(proj.durableRows.map(\.actorEmoji), Array(repeating: emoji, count: 4))
        guard case .system(let spawned) = proj.durableRows[0].kind,
              case .system(let review) = proj.durableRows[2].kind,
              case .system(let finished) = proj.durableRows[3].kind else {
            return XCTFail("expected lifecycle rows")
        }
        XCTAssertEqual(spawned, "Spawned")
        XCTAssertEqual(review, "Review submitted: Looks good")
        XCTAssertEqual(finished, "Finished")
        let lifecycleText = [spawned, review, finished].joined(separator: " ").lowercased()
        XCTAssertFalse(lifecycleText.contains("reviewer"))
        XCTAssertFalse(lifecycleText.contains("opus"))
    }

    func testCoordinatorUserAndSystemActorsDoNotReceivePlants() {
        var proj = SessionProjection()
        proj.apply(makeEvent(
            seq: 1, type: "user_input", actor: "user",
            dataJson: #"{"text":"hello"}"#))
        proj.apply(makeEvent(
            seq: 2, type: "model_turn", actor: "coordinator",
            dataJson: #"{"text":"working"}"#))
        proj.apply(makeEvent(
            seq: 3, type: "future_widget", actor: "daemon",
            dataJson: #"{"text":"maintenance"}"#))
        proj.apply(makeEvent(
            seq: 4, type: "future_widget", actor: "system",
            dataJson: #"{"text":"notice"}"#))

        XCTAssertEqual(proj.durableRows.map(\.actorEmoji), ["", "", "", ""])
    }

    func testPlantAssignmentSurvivesTransientReset() throws {
        var proj = SessionProjection()
        proj.apply(delta("working", actor: "implementer"))
        let assigned = try XCTUnwrap(proj.liveTails.first?.actorEmoji)

        proj.clearLiveTails()
        proj.apply(delta("working again", actor: "implementer"))

        XCTAssertEqual(proj.liveTails.first?.actorEmoji, assigned)
    }

    func testPlantAssignmentsReconstructDeterministicallyFromReplay() {
        let events = [
            makeEvent(
                seq: 1, type: "subagent_spawned", actor: "coordinator",
                dataJson: #"{"role":"implementer","model":"sol"}"#),
            makeEvent(
                seq: 2, type: "subagent_spawned", actor: "coordinator",
                dataJson: #"{"role":"reviewer","model":"opus"}"#),
            makeEvent(
                seq: 3, type: "model_turn", actor: "implementer",
                dataJson: #"{"text":"implemented"}"#),
            makeEvent(
                seq: 4, type: "model_turn", actor: "reviewer:opus",
                dataJson: #"{"text":"reviewed"}"#),
        ]
        var first = SessionProjection()
        var replayed = SessionProjection()
        first.apply(events)
        replayed.apply(events)

        XCTAssertEqual(first, replayed)
        XCTAssertEqual(first.durableRows.map(\.actorEmoji), replayed.durableRows.map(\.actorEmoji))
        XCTAssertNotEqual(first.durableRows[0].actorEmoji, first.durableRows[1].actorEmoji)
    }

    func testPlantPaletteOverflowCyclesWithoutLosingActorIdentity() {
        var proj = SessionProjection()
        let count = SessionProjection.subagentEmojiPalette.count + 2
        for index in 0..<count {
            proj.apply(makeEvent(
                seq: Int64(index + 1), type: "model_turn", actor: "agent:\(index)",
                dataJson: #"{"text":"done"}"#))
        }

        XCTAssertEqual(proj.durableRows.count, count)
        XCTAssertEqual(
            proj.durableRows[0].actorEmoji,
            proj.durableRows[SessionProjection.subagentEmojiPalette.count].actorEmoji)
        XCTAssertEqual(proj.durableRows.last?.actor, "agent:\(count - 1)")
        XCTAssertFalse(proj.durableRows.last?.actorEmoji.isEmpty ?? true)
    }

    /// The unread tracker records "seen up to here" from the daemon's own event
    /// timestamps, so every durable event must advance the watermark — including
    /// ones that render no row — while transient tail deltas must not.
    func testLastEventTimestampTracksDurableEventsOnly() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "session_started", ts: "2026-08-06T10:00:00Z"))
        XCTAssertEqual(proj.lastEventTimestamp, "2026-08-06T10:00:00Z")

        proj.apply(makeEvent(
            seq: 2, type: "model_turn", dataJson: #"{"text":"hi"}"#,
            ts: "2026-08-06T10:00:05Z"))
        XCTAssertEqual(proj.lastEventTimestamp, "2026-08-06T10:00:05Z")

        // A render-suppressed lifecycle event still counts as seen.
        proj.apply(makeEvent(seq: 3, type: "usage_totals", ts: "2026-08-06T10:00:06Z"))
        XCTAssertEqual(proj.lastEventTimestamp, "2026-08-06T10:00:06Z")

        // Transient deltas are not persisted and must not move the watermark.
        proj.apply(delta("streaming…"))
        XCTAssertEqual(proj.lastEventTimestamp, "2026-08-06T10:00:06Z")

        // A replayed (already-folded) event must not move it backwards either.
        proj.apply(makeEvent(seq: 2, type: "model_turn", ts: "2026-08-06T10:00:05Z"))
        XCTAssertEqual(proj.lastEventTimestamp, "2026-08-06T10:00:06Z")
    }

    func testLiveTailCarriesOptionalVerifiedAppendHint() {
        var proj = SessionProjection()
        proj.apply(delta("Hel"))

        var hinted = Ycc_V1_Event()
        hinted.type = "turn_delta"
        hinted.transient = true
        hinted.dataJson = #"{"text":"Hello 🌍","append":"lo 🌍","append_base_utf8":3}"#
        proj.apply(hinted)

        XCTAssertEqual(proj.liveTail?.liveAppend, "lo 🌍")
        XCTAssertEqual(proj.liveTail?.liveAppendBaseUTF8, 3)
        guard case .liveTail(let text)? = proj.liveTail?.kind else {
            return XCTFail("expected hinted live tail")
        }
        XCTAssertEqual(text, "Hello 🌍", "full snapshot remains authoritative")
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
        guard case .system? = proj.durableRows.last?.kind else {
            return XCTFail("unknown type should degrade to a system row")
        }
    }

    func testMalformedDataJsonDoesNotCrash() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "user_input", dataJson: "{not valid json"))
        proj.apply(makeEvent(seq: 2, type: "model_turn", dataJson: ""))
        // Degrades to empty text; user_input bubble still appears (empty text).
        XCTAssertEqual(proj.lastPersistedSeq, 2)
        guard case .userMessage(let text, let pictures)? = proj.durableRows.first?.kind else {
            return XCTFail("expected a user bubble")
        }
        XCTAssertEqual(text, "")
        XCTAssertTrue(pictures.isEmpty)
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

    /// The optimistic close must resolve the transcript card too — the user has
    /// answered, so the row cannot keep saying "Waiting for an answer".
    func testResolvePendingQuestionAnswersTheRowImmediately() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "question_asked",
                             dataJson: #"{"question":"Proceed?","options":["yes","no"]}"#))

        proj.resolvePendingQuestion(answer: "yes")

        XCTAssertNil(proj.pendingQuestion)
        guard case .question(_, _, let answer)? = proj.durableRows.last?.kind else {
            return XCTFail("expected a resolved question row")
        }
        XCTAssertEqual(answer, "yes")
    }

    /// Regression: closing the gate optimistically used to orphan the later
    /// `question_answered` event (it looked the row up through `pendingQuestion`,
    /// which was already nil), leaving the card unanswered forever.
    func testAnsweredEventStillResolvesRowAfterOptimisticClose() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "question_asked",
                             dataJson: #"{"question":"Proceed?","options":["yes","no"]}"#))
        proj.resolvePendingQuestion(answer: "")

        proj.apply(makeEvent(seq: 2, type: "question_answered", dataJson: #"{"answer":"yes"}"#))

        guard case .question(_, _, let answer)? = proj.durableRows.last?.kind else {
            return XCTFail("expected a resolved question row")
        }
        XCTAssertEqual(answer, "yes", "the event is authoritative for the row text")
    }

    /// An unparseable/empty answered payload must not wipe the text this client
    /// already folded in optimistically.
    func testEmptyAnsweredPayloadKeepsOptimisticAnswerText() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "question_asked",
                             dataJson: #"{"question":"Proceed?"}"#))
        proj.resolvePendingQuestion(answer: "go ahead")

        proj.apply(makeEvent(seq: 2, type: "question_answered", dataJson: "{}"))

        guard case .question(_, _, let answer)? = proj.durableRows.last?.kind else {
            return XCTFail("expected a resolved question row")
        }
        XCTAssertEqual(answer, "go ahead")
    }

    /// A re-asked question opens a fresh gate and its own row; answering it must
    /// resolve the new row, not the stale one.
    func testSecondQuestionResolvesItsOwnRow() {
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "question_asked", dataJson: #"{"question":"First?"}"#))
        proj.apply(makeEvent(seq: 2, type: "question_answered", dataJson: #"{"answer":"one"}"#))
        proj.apply(makeEvent(seq: 3, type: "question_asked", dataJson: #"{"question":"Second?"}"#))
        proj.resolvePendingQuestion(answer: "two")

        let questions = proj.durableRows.compactMap { row -> String? in
            guard case .question(_, _, let answer) = row.kind else { return nil }
            return answer
        }
        XCTAssertEqual(questions, ["one", "two"])
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
        guard case .system? = proj.durableRows.last?.kind else {
            return XCTFail("empty finish report should still produce a lifecycle row")
        }
    }

    // MARK: - Coordinator model folding

    func testCoordinatorModelFoldsFromLifecycleAndTurns() {
        var proj = SessionProjection()
        XCTAssertEqual(proj.coordinatorModel, "")

        proj.apply(makeEvent(
            seq: 1, type: "session_started", actor: "system",
            dataJson: #"{"mode":"work","workspace":"/ws","coordinator":"claude"}"#))
        XCTAssertEqual(proj.coordinatorModel, "claude")
        // The turn that actually ran is authoritative.
        proj.apply(makeEvent(
            seq: 2, type: "model_turn", actor: "coordinator",
            dataJson: #"{"text":"hi","model_name":"gpt"}"#))
        XCTAssertEqual(proj.coordinatorModel, "gpt")

        // A subagent's turn must not hijack the coordinator readout.
        proj.apply(makeEvent(
            seq: 3, type: "model_turn", actor: "implementer",
            dataJson: #"{"text":"sub","model_name":"glm"}"#))
        XCTAssertEqual(proj.coordinatorModel, "gpt")

        // A mid-session role change moves it.
        proj.apply(makeEvent(
            seq: 4, type: "role_config_changed", actor: "system",
            dataJson: #"{"coordinator":"claude","implementer":"gpt"}"#))
        XCTAssertEqual(proj.coordinatorModel, "claude")
    }

    func testCoordinatorModelIgnoresEmptyAndUnrelatedPayloads() {
        var proj = SessionProjection()
        proj.apply(makeEvent(
            seq: 1, type: "session_started", actor: "system",
            dataJson: #"{"mode":"work","coordinator":"claude"}"#))
        // Older logs omit model_name; the known name must survive.
        proj.apply(makeEvent(seq: 2, type: "model_turn", dataJson: #"{"text":"hi"}"#))
        XCTAssertEqual(proj.coordinatorModel, "claude")
        // Unparsable payloads never clear it either.
        proj.apply(makeEvent(seq: 3, type: "model_turn", dataJson: "not json"))
        XCTAssertEqual(proj.coordinatorModel, "claude")
    }

    // MARK: - Current context folding

    func testCurrentContextFoldsLatestCoordinatorEstimate() {
        var proj = SessionProjection()
        XCTAssertNil(proj.currentContextTokensEstimate)

        proj.apply(makeEvent(
            seq: 1, type: "model_turn", actor: "coordinator",
            dataJson: #"{"text":"first","context_tokens_est":12345}"#))
        XCTAssertEqual(proj.currentContextTokensEstimate, 12_345)

        // A newer completed coordinator turn replaces the prior prompt estimate.
        proj.apply(makeEvent(
            seq: 2, type: "model_turn", actor: "coordinator",
            dataJson: #"{"text":"second","context_tokens_est":23456}"#))
        XCTAssertEqual(proj.currentContextTokensEstimate, 23_456)
    }

    func testCurrentContextIgnoresSubagentsAndMissingTelemetry() {
        var proj = SessionProjection()
        proj.apply(makeEvent(
            seq: 1, type: "model_turn", actor: "",
            dataJson: #"{"text":"coordinator","context_tokens_est":12000}"#))

        proj.apply(makeEvent(
            seq: 2, type: "model_turn", actor: "implementer",
            dataJson: #"{"text":"subagent","context_tokens_est":99000}"#))
        XCTAssertEqual(proj.currentContextTokensEstimate, 12_000)

        // Old logs, malformed values, and unrelated events do not erase the last
        // useful estimate.
        proj.apply(makeEvent(seq: 3, type: "model_turn", dataJson: #"{"text":"old"}"#))
        proj.apply(makeEvent(
            seq: 4, type: "model_turn", dataJson: #"{"context_tokens_est":-1}"#))
        proj.apply(makeEvent(
            seq: 5, type: "tool_result", dataJson: #"{"context_tokens_est":45000}"#))
        XCTAssertEqual(proj.currentContextTokensEstimate, 12_000)
    }

    // MARK: - Phase folding

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
        XCTAssertEqual(proj.phase, .error("boom", retryable: true))

        proj.apply(makeEvent(seq: 6, type: "session_stopped"))
        XCTAssertEqual(proj.phase, .stopped)
    }

    func testSessionErrorReadsMsgWithFallbacks() {
        // Production shape: the daemon emits the message under "msg".
        var proj = SessionProjection()
        proj.apply(makeEvent(seq: 1, type: "session_error", dataJson: #"{"msg":"kaboom"}"#))
        XCTAssertEqual(proj.phase, .error("kaboom", retryable: true))
        // Fallback to the legacy "error" key.
        var errProj = SessionProjection()
        errProj.apply(makeEvent(seq: 1, type: "session_error", dataJson: #"{"error":"legacy"}"#))
        XCTAssertEqual(errProj.phase, .error("legacy", retryable: true))

        // Fallback to "text".
        var textProj = SessionProjection()
        textProj.apply(makeEvent(seq: 1, type: "session_error", dataJson: #"{"text":"tail"}"#))
        XCTAssertEqual(textProj.phase, .error("tail", retryable: true))

        // An explicit `retryable=false` (e.g. auth / invalid request / context
        // length) suppresses the retry affordance.
        var terminalProj = SessionProjection()
        terminalProj.apply(makeEvent(
            seq: 1, type: "session_error",
            dataJson: #"{"msg":"context window exceeded","retryable":false}"#))
        XCTAssertEqual(terminalProj.phase, .error("context window exceeded", retryable: false))
    }

    // MARK: - Commit rows (task 0189)

    func testCommitMadeExposesShaForDrillIn() {
        var proj = SessionProjection()
        proj.apply(makeEvent(
            seq: 1, type: "commit_made",
            dataJson: #"{"sha":"abc123def","message":"do the thing"}"#))
        guard case .commit(_, let sha)? = proj.durableRows.last?.kind else {
            return XCTFail("commit_made should render a commit row")
        }
        XCTAssertEqual(sha, "abc123def")
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
