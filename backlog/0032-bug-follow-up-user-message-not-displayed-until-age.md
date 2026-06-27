---
id: "0032"
title: 'Bug: follow-up user message not displayed until agent''s next response (user_input echo emitted on dequeue, not on send)'
status: done
priority: 2
created: "2026-06-26"
updated: "2026-06-27"
depends_on: []
spec_refs:
    - 7. Event model
    - 18. Client UI (TUI)
---

## Description
## Bug

In the TUI, sending a follow-up message after the coordinator has returned (or while it
is still working) does not display the user's message until the agent's next response
arrives. The user message appears to "stick" until the first agent output comes back.

## Root cause

The `user_input` echo event is emitted from **inside the session run loop**, only when the
loop dequeues the prod from `inputCh`:

```
// internal/session/session.go (run loop)
select {
case text := <-s.inputCh:
    s.emitter.EmitAs("user", event.UserInput, ...)  // <- emitted here, on dequeue
    s.currentLoop().Post(text)
...
```

`Session.SendInput` (session.go:107) merely pushes the text onto the buffered `inputCh`
and returns. The `user_input` event is therefore not recorded/broadcast until the run loop
returns to its `select` — which only happens after the current/next agent turn completes.
The TUI does no optimistic local echo (`sendInput` in `internal/tui/tui.go` just calls the
`SendInput` RPC and waits for the streamed event), so the message is invisible until the
agent responds.

## Fix

Emit the `user_input` event at the moment the prod is **accepted** in `Session.SendInput`
(the successful `case s.inputCh <- text:` branch), and remove the emit from the run-loop
`select` so the event is recorded exactly once. This decouples the echo from loop
scheduling, giving immediate display in both the idle and busy cases. The loop keeps doing
`Post(text)` when it dequeues.

Notes:
- Only emit on a successful enqueue (not when the buffer is full / SendInput errors).
- The initial prompt echo (`run()` ~line 323) and question-answer path (`inter.Answer` →
  `question_answered`) are separate and unchanged.

## Acceptance criteria
- A follow-up message is displayed in the TUI immediately after sending, regardless of
  whether the agent is idle or still working — it no longer waits for the next agent
  response.
- `user_input` is recorded exactly once per prod (no duplicate echo).
- A session test asserts that calling `SendInput` while the loop is busy emits a
  `user_input` event promptly (before the agent's next turn completes).
- `go build ./...` and `go test ./...` pass.

## Work log


## Acceptance criteria

## Work log
- 2026-06-27 plan: Move the `user_input` echo so it's emitted when the prod is accepted, not when dequeued:  1. In `Session.SendInput` (internal/session/session.go), in the successful `case s.inputCh <- text:` branch, e
…[truncated]
- 2026-06-27 implementer report: Fixed the bug where a follow-up `user_input` echo was only emitted when the run loop dequeued the prod (after the current/next agent turn), making the message invisible in the TUI until the agent resp
…[truncated]
- 2026-06-27 review (claude): accept — The fix correctly emits the user_input echo at the moment the prod is accepted (the successful `case s.inputCh <- text:` branch) and removes the emit from the run-loop select, so the event is recorded
…[truncated]
- 2026-06-27 decision: accept — commit 1dbd68b: Fix: emit user_input echo on enqueue so follow-up messages display immediately  Move the user_input echo from the session run loop's dequeue point into SendInput's successful enqueue branch, so a foll
…[truncated]
