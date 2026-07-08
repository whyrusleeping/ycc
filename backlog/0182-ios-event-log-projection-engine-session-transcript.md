---
id: "0182"
title: 'iOS: event-log projection engine + session transcript view (Subscribe fold, replay-from-seq, turn_delta tail)'
status: todo
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

## Work log
