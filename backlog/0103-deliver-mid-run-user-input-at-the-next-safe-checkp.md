---
id: "0103"
title: Deliver mid-run user input at the next safe checkpoint (steer-by-default)
status: todo
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

## Work log
