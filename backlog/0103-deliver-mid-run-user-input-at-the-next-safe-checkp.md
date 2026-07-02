---
id: "0103"
title: Deliver mid-run user input at the next safe checkpoint (steer-by-default)
status: done
priority: 2
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 18.7 Interrupt & steer (pause / correct / resume)
    - 18.1 Session input — multiline
---

## Description
## Description
Typing into a running session and pressing Enter calls `SendInput`, which lands in `inputCh` and is only read **after the current `Run` completes** (session.go run loop) — yet the `user_input` echo event is emitted immediately, so the transcript shows the message as if the agent saw it. A user steering with "no, wrong file" is silently ignored for the rest of the run. The `Checkpoint` steer machinery already exists but requires an explicit `ctrl+i` first.

Make mid-run input steer-by-default: when the loop is mid-`Run`, queue the text as a correction and deliver it at the **next safe checkpoint** (between turns / after a tool result), without requiring the pause/resume ceremony. The checkpoint hook is already in the hot loop; this is "corrections drain at checkpoint even when not paused."

## Acceptance criteria
- [ ] Text sent while a run is in flight reaches the model at the next checkpoint (appended as a user message before the next turn), not only after the run ends
- [ ] Ordering preserved for multiple sends; behavior while actually paused (§18.7) unchanged
- [ ] The TUI renders a not-yet-delivered prod distinctly (e.g. "queued") until the delivery point, so the echo never lies about delivery
- [ ] Idle-session prods (current behavior) unchanged; events recorded so replay stays lossless
- [ ] Spec §18.7 updated (its "Why it's needed" paragraph describes the old buffering)

## Acceptance criteria

## Plan

Goal: text sent while a run is in flight is delivered at the next safe checkpoint (steer-by-default), with honest "queued → delivered" rendering and lossless replay. No engine loop changes needed — checkpoints already Post whatever Session.Checkpoint returns; all logic lives in the session, events, replay, and TUI layers.

1) Session state (internal/session/session.go)
- Change `corrections []string` to a slice of `{text string, seq int}` (seq = the queued user_input echo's event seq) so delivery can reference the queued echo.
- Add a `running bool` flag guarded by steerMu. Set it true immediately before `s.currentLoop().Run(s.ctx)` and false right after Run returns (both under steerMu). This closes the race between SendInput's state check and the run loop finishing.

2) SendInput routing
- Pending question → unchanged (inter.Answer).
- paused || pauseReq → unchanged delivery timing (drain only on explicit Resume), but the echo now carries `"queued": true` and the correction records the echo seq.
- else if running (under steerMu) → queue as a correction: emit `EmitAs("user", event.UserInput, {"text": text, "queued": true})`, record {text, seq}. Do NOT push to inputCh.
- else (idle) → inputCh path exactly as today (plain user_input echo).
- Emit events outside steerMu where practical; preserve FIFO order of multiple sends.

3) Checkpoint (Session.Checkpoint)
- Fast path when !pauseReq: if corrections are pending, drain them (under steerMu) and return their texts in order — no pause, no interrupted/resumed events. For each drained correction emit a new `user_input_delivered` event with `{"seq": <queued echo seq>, "text": <text>}` (emit after unlocking). If nothing pending, return (nil, nil) as today.
- Paused path unchanged (drain only on Resume), but also emit `user_input_delivered` for each drained correction at resume time.
- Delivery event must be emitted at the point the text actually enters the conversation (checkpoint return → engine Posts it before the next turn), keeping the event-log order consistent with the real conversation order.

4) run() idle race
- After Run returns (and after the SessionIdle/SessionError emit as today), drain any corrections that slipped in as the run was ending: under steerMu (running already false) take them; if non-empty, Post each into currentLoop, emit user_input_delivered for each, and `continue` the loop instead of blocking on inputCh. Mode-switch `continue` path needs no special handling (the new Run's first checkpoint drains).

5) Events (internal/event/event.go, reduce.go)
- Add `UserInputDelivered Type = "user_input_delivered"` with a doc comment (marks the checkpoint at which a queued mid-run user_input actually entered the conversation; references the queued echo by seq).
- Reduce: treat like UserInput (clears a latched error status); no other projection change.

6) Replay (internal/engine/replay.go)
- `user_input` with data `queued: true` → skip (do not append to history at echo position).
- `user_input_delivered` → append `{Role:"user", Content: text}` at that position and reset assistantIdx/truncation state exactly like UserInput does. This reproduces the live Post position so replayed history matches what the model saw.
- Queued-but-never-delivered input (session stopped mid-run) is deliberately absent from replayed history — matches what the model saw; note in a comment.

7) TUI (internal/tui/tui.go)
- Maintain a set of delivered seqs (from user_input_delivered events' `seq` field) — populate in appendEvent; reset where m.evs is reset (reopen/transcript load).
- Render `user_input` rows with queued:true and seq not yet delivered with a distinct dim "(queued)" suffix (detailLine or row render path); once the matching delivered event arrives, render normally (rebuild() re-renders rows, so the upgrade is automatic).
- Do not render `user_input_delivered` as its own transcript row (skip it like other non-row events) so the message never appears twice.

8) Spec §18.7 (spec.md ~line 995)
- Rewrite the "Why it's needed" paragraph: mid-run input no longer sits in inputCh until the run ends; it is queued as a correction and delivered at the next safe checkpoint by default. Interrupt/pause remains for "stop and hold" steering; behavior while paused unchanged. Also mention the queued/delivered event pair and the honest echo. Add `user_input_delivered` to the event table (~line 180) if that table enumerates event types.

9) Tests
- session (steer_test.go or new file): mid-run send → Checkpoint (unpaused) returns the text and emits user_input(queued:true) + user_input_delivered in order; multiple sends preserve order; paused behavior unchanged (drain only on Resume, now also with delivered events); idle send still goes via inputCh with a plain echo (update input_echo_test.go expectations only if needed — idle path emits no `queued` flag so it should pass unchanged).
- run-loop race: queue a correction while running flag is being cleared → delivered via the post-idle drain (a scripted Turner session test if feasible, else a focused unit test on the drain helper).
- replay: log with queued echo + later delivered event reconstructs exactly one user message at the delivered position; queued-without-delivered is omitted.
- go build ./... && go test ./... green.

### Starting points
- internal/session/session.go: SendInput (~line 144), Checkpoint (~264), run() (~626), steer fields near steerMu/corrections
- internal/engine/loop.go: steerCheckpoint (~72) — already Posts returned msgs at top-of-turn (~333) and after each tool result (~522); no engine change needed
- internal/engine/replay.go: ReplayHistory UserInput case (~103)
- internal/event/event.go (Type consts), internal/event/reduce.go (UserInput case)
- internal/tui/tui.go: appendEvent (~3800-3936), detailLine user_input case (~5928), typeGlyph (~5845), m.evs reset points (transcriptMsg ~1287, session open ~1335)
- internal/session/steer_test.go + input_echo_test.go: existing patterns (captureRecorder, newSteerSession)
- spec.md: §18.7 at line 995 ('Why it's needed' paragraph at 1003), event table at ~line 180

## Work log
- 2026-07-01 plan: Goal: text sent while a run is in flight is delivered at the next safe checkpoint (steer-by-default), with honest "queued → delivered" rendering and lossless replay. No engine loop changes needed �
…[truncated]
- 2026-07-01 context hints: 7 recorded with plan
- 2026-07-01 context hints: internal/session/session.go: SendInput (~144), Checkpoint (~264), run() (~626-700), steer fields near steerMu/corrections declarations (~line 63 inputCh; find steerMu nearby); internal/engine/loop.go:
…[truncated]
- 2026-07-01 implementer report: Implemented steer-by-default mid-run input delivery (task 0103). Text typed while a run is in flight is now queued as a correction and delivered at the next safe checkpoint, with honest queued→deliv
…[truncated]
- 2026-07-01 review tier: single-opus — reviewers: Claude
- 2026-07-01 review (Claude): accept — The change implements steer-by-default mid-run input delivery cleanly and completely across all layers (session, events, replay, TUI, spec). Mid-run SendInput now queues corrections with a queued:true
…[truncated]
- 2026-07-01 decision: accept — commit: session: deliver mid-run user input at the next safe checkpoint (steer-by-default) with queued/delivered events, lossless replay, and honest TUI echo (0103)
- 2026-07-01 usage: 56,749 tok (in 242, out 56,507, cache_r 7,567,602, cache_w 265,182) · cost n/a (unpriced)
  implementer: 33,132 tok (in 144, out 32,988, cache_r 5,754,998, cache_w 103,863) · cost n/a (unpriced)
  coordinator: 18,213 tok (in 70, out 18,143, cache_r 1,491,889, cache_w 127,003) · cost n/a (unpriced)
  reviewer:Claude: 5,404 tok (in 28, out 5,376, cache_r 320,715, cache_w 34,316) · cost n/a (unpriced)
