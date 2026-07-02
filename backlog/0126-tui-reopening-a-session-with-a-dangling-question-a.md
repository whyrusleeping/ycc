---
id: "0126"
title: 'TUI: reopening a session with a dangling question_asked re-arms a dead picker'
status: done
priority: 2
created: "2026-07-02"
updated: "2026-07-02"
depends_on: []
spec_refs: []
---

## Description
## Bug

Reopening a session whose persisted log ends with an unanswered `question_asked` (e.g. the session was stopped/quit while blocked on `ask_user`) put the TUI straight back into the dead-input state: the replayed `question_asked` re-armed the picker and blurred the textarea, but the question is no longer answerable — on reopen the daemon repairs the dangling `ask_user` tool call with a synthetic tool result (`internal/engine/replay.go`), so `AnswerQuestion` would fail with FailedPrecondition. No `question_answered` ever arrives, and the `session_reopened` marker was ignored by `appendEvent`.

## Fix

`appendEvent` now handles `session_reopened`: if a question/picker/wizard is pending at that point in the replay it is stale — clear `pending`/`pendingSeq`/`pickerOpts`/`picking`, `clearWizard()`, reset the latched "waiting for your answer" status to "running", and re-focus the textarea (skipped while transcript search owns input). If the reopened model still needs the answer it re-asks live with a fresh `question_asked`, which re-arms the picker normally.

## Acceptance criteria

- [x] Replaying `question_asked` (options) followed by `session_reopened` leaves no picker, empty pending, focused textarea.
- [x] Same for a multi-question wizard batch.
- [x] Regression tests: `TestReopenClearsStaleQuestion`, `TestReopenClearsStaleWizard`.

Related: 0125 (picker-answer re-focus).

## Acceptance criteria

## Work log
