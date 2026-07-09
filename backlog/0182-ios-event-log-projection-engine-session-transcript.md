---
id: "0182"
title: 'iOS: event-log projection engine + session transcript view (Subscribe fold, replay-from-seq, turn_delta tail)'
status: done
priority: 2
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0180"
spec_refs:
    - "5.2"
    - 18. Client UI (TUI)
    - docs/design/ios-client.md#5. App architecture
---

## Description
The event-log projection engine and read-only session transcript view per `docs/design/ios-client.md` §5/§6 phase 1 step 3 — the heart of the app ("the UI is a projection of the log", spec §5.2/§18, docs/remote-api.md event model).

## Description
- `YccKit.SessionProjection`: a pure reducer folding `Event`s into ordered render rows — user_input/model_turn → chat bubbles; thinking → collapsed expandable row; tool_call/tool_result paired by id → collapsed one-liner (name + status) expandable to args/output; question_asked/question_answered → pending-question state; session lifecycle / commit_made / decision_made etc. → compact system rows; unknown event types → generic system row (forward-compat).
- `dataJson` is an embedded JSON string parsed a second time; `seq` is int64-as-string; tolerate seq-less transient events.
- Transient `turn_delta` handling: snapshot (full accumulated text) rendered as one replaceable live-tail row; cleared by `{"text":"","done":true}` or the durable `model_turn`; never persisted into state, never advances the cursor.
- Replay-from-seq: track highest persisted seq; `YccClient.subscribe(sessionId:fromSeq:)` as an AsyncStream; on stream drop or app foregrounding, re-Subscribe from the last persisted seq (no gap, no duplication).
- Session view (SwiftUI): live sessions via Subscribe, persisted via GetSessionTranscript (same reducer, no tail); auto-follow scroll pinned to newest while at bottom, with a "jump to latest" pill after the user scrolls up.

## Acceptance criteria
- `swift test` fixtures fold a real captured events.jsonl transcript (incl. interleaved transient turn_delta and a mid-stream reconnect) into identical state whether folded in one pass or via disconnect + replay-from-seq.
- Live tail row replaced (not appended) on successive deltas and cleared on done/model_turn.
- Persisted sessions render read-only with no stream held open.
- Simulator smoke against a local daemon shows a live session streaming.

## Plan

Build the event-log projection engine in YccKit (headless-testable) plus a read-only SwiftUI session transcript view, per docs/design/ios-client.md §5 and docs/remote-api.md "Event model".

1. YccKit: `SessionProjection` — a pure reducer over `Ycc_V1_Event`:
   - `mutating apply(_ event:)` folds events into an ordered `[TranscriptRow]` with stable identity (keyed by seq / tool-call id; transient tail gets a fixed synthetic id).
   - Row kinds: user_input → user bubble; model_turn → model bubble (actor shown); thinking → collapsed expandable row; tool_call/tool_result paired by id → one-liner (name + ok/error status), expandable to args/output; question_asked → pending-question state + row, question_answered clears it; lifecycle/commit_made/decision_made/plan_proposed/review_submitted/etc → compact system rows; unknown types → generic system row (forward compat, never crash).
   - `dataJson` is an embedded JSON string — parse per-type with JSONSerialization/Codable, tolerating missing/malformed payloads (degrade to raw text).
   - Track `lastPersistedSeq` = max seq of persisted events only. Transient events (transient:true / seq 0) never advance it.
   - turn_delta: payload is a SNAPSHOT `{"text": full-so-far}`; render as one replaceable live-tail row per actor; cleared by `{"text":"","done":true}` or by arrival of the durable model_turn. Never persisted into rows.
2. YccKit: `YccClient` additions — `getSessionTranscript(project:sessionId:)`, and `subscribe(sessionId:fromSeq:) -> AsyncThrowingStream<Ycc_V1_Event, Error>` wrapping the generated ServerOnlyAsyncStreamInterface (send request, iterate results, finish/throw on stream end/error; cancel the underlying stream on stream termination).
3. YccKit: `SessionViewModel` (@Observable, @MainActor) driving the view: modes live (Subscribe from seq 0, on stream drop or foregrounding re-Subscribe with fromSeq = lastPersistedSeq) and persisted (GetSessionTranscript once, no stream). Reconnect loop with small backoff; injectable stream source so logic is testable headlessly.
4. App: `SessionView` (SwiftUI) — ScrollView/List of rows from the projection: bubbles, DisclosureGroup-style expandable thinking/tool rows, system rows, live-tail row with subtle "streaming" affordance; auto-follow pinned to bottom while at bottom, "jump to latest" pill when scrolled up (ScrollViewReader + bottom anchor detection). Read-only this task (input bar etc. is 0183). Reachable via a NavigationStack destination taking (project, sessionId, live) — the session list (0181) will link to it; for now no permanent entry needed beyond the type existing and compiling.
5. Tests (swift test, headless):
   - Fixture built from a REAL captured transcript: convert a `.ycc/sessions/*/events.jsonl` (which stores `data` as a nested object) into wire-shape Event fixtures (`dataJson` string) — commit the fixture as a resource in Tests. Interleave transient turn_delta events and simulate a mid-stream disconnect: assert fold-in-one-pass state == fold(prefix) + replay-from-lastPersistedSeq(suffix) state (identical rows).
   - Live-tail: successive deltas replace (row count stable), `{"text":"","done":true}` clears, model_turn clears and appends durable bubble.
   - Seq-less/unknown-type tolerance; tool_call/result pairing incl. orphan result.
6. Verify: `cd clients/ios/YccKit && swift test`; `cd clients/ios && xcodegen generate && xcodebuild ... -destination 'generic/platform=iOS Simulator' build`. The interactive simulator-against-live-daemon smoke is deferred to the runbook that lands with 0183 (per design §10); note this in the work log.

### Starting points
- clients/ios/YccKit/Sources/YccKit/YccClient.swift — existing thin client wrapper; add subscribe/getSessionTranscript here
- clients/ios/YccKit/Sources/YccProto/ycc/v1/ycc.connect.swift:41 — `subscribe` returns ServerOnlyAsyncStreamInterface<Ycc_V1_SubscribeRequest, Ycc_V1_Event>; results() is an AsyncStream of StreamResult
- docs/remote-api.md §Event model — dataJson embedded string, transient/turn_delta snapshot semantics, replay-from-seq rules
- proto/ycc/v1/ycc.proto — Event{seq,ts,actor,type,data_json,transient}, GetSessionTranscriptRequest{project,session_id}
- real transcripts for fixtures: .ycc/sessions/*/events.jsonl (note: on-disk uses nested `data` object; wire uses `dataJson` string — convert)
- clients/ios/App/LandingView.swift + AppModel.swift — current app shell pattern (@Observable, YccError handling)
- build/test: `cd clients/ios/YccKit && swift test`; `cd clients/ios && xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build` (generated .xcodeproj and App/Info.plist are git-ignored)

## Work log
- 2026-07-08 plan: Build the event-log projection engine in YccKit (headless-testable) plus a read-only SwiftUI session transcript view, per docs/design/ios-client.md §5 and docs/remote-api.md "Event model".  1. YccKit
…[truncated]
- 2026-07-08 context hints: 7 recorded with plan
- 2026-07-08 context hints: clients/ios/YccKit/Sources/YccKit/YccClient.swift — existing thin client wrapper; add subscribe/getSessionTranscript here; clients/ios/YccKit/Sources/YccProto/ycc/v1/ycc.connect.swift:41,225 — `su
…[truncated]
- 2026-07-08 implementer report: Implemented Task 0182 — the iOS event-log projection engine + read-only session transcript view.  ## What I built  **YccKit (headless-testable logic):** - `SessionProjection.swift` — a pure, `Send
…[truncated]
- 2026-07-08 review tier: single-opus — reviewers: claude
- 2026-07-08 review (claude): accept — Task 0182 is well-implemented and satisfies the acceptance criteria. SessionProjection is a clean, pure, dependency-free reducer that folds Events into ordered TranscriptRows: message bubbles, expanda
…[truncated]
- 2026-07-08 revision: Applied both reviewer fixes and re-verified.  ## Changes  1. **Clear stale live tail before (re)subscribing** — Added a `mutating func clearLiveTail()` to `SessionProjection` (drops `liveTail`), and
…[truncated]
- 2026-07-08 review (claude): accept — The revision addresses both prior minor findings correctly. SessionProjection now exposes clearLiveTail(), which startLiveLoop() calls before each (re)subscribe so a stale streamed tail no longer ling
…[truncated]
- 2026-07-08 decision: accept — commit: iOS: event-log projection engine + read-only session transcript view (task 0182)
- 2026-07-08 usage: 67,635 tok (in 178, out 67,457, cache_r 6,287,898, cache_w 517,280) · $9.3260
  implementer: 53,327 tok (in 102, out 53,225, cache_r 4,660,814, cache_w 270,069) · $5.3495
  reviewer:claude: 8,447 tok (in 52, out 8,395, cache_r 1,059,779, cache_w 114,085) · $1.4531
  coordinator: 5,861 tok (in 24, out 5,837, cache_r 567,305, cache_w 133,126) · $2.5235
