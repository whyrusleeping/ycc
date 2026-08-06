---
id: "0247"
title: 'iOS: answered question card stayed "Waiting for an answer"'
status: done
priority: 2
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs: []
---

## Description
Field report: answering an `ask_user` question on iOS closed the sheet (the optimistic gate close from task 0246 works) but the question card in the transcript kept reading "Waiting for an answer".

Cause: `SessionProjection.applyQuestionAnswered` looked the row up **through `pendingQuestion`**. Task 0246 added `clearPendingQuestion()`, which nils the gate as soon as the daemon accepts the answer — so when the authoritative `question_answered` event finally arrived there was no gate left to consult, the row was never resolved, and the card stayed orange until the transcript was re-folded from scratch (reopening the session).

## Acceptance criteria

- [x] The transcript card resolves as soon as the daemon accepts the answer, showing what this client answered (option text for an option pick, the typed text, `a; b` for a batch).
- [x] The later `question_answered` event still folds the daemon's authoritative text into the same row after an optimistic close (regression test).
- [x] An empty/unparseable `question_answered` payload does not wipe the optimistic text.
- [x] A failed answer leaves both the gate open and the card unanswered.
- [x] A re-asked question resolves its own row, not the previous one.
- [ ] Verified on device (no Swift toolchain on the Linux workspace — `cd clients/ios/YccKit && swift test`).

## Work log
- `SessionProjection`: added `openQuestionRowID`, retained past an optimistic close, and a shared `foldAnswer(_:)` used by both the event path and the new `resolvePendingQuestion(answer:)` (replaces `clearPendingQuestion()`). An empty answer resolves the row without overwriting existing text.
- `SessionViewModel`: `clearAnsweredGate(answer:)`; `localAnswer(optionIndex:text:)` / `localBatchAnswer(_:)` resolve option indices against the pending gate exactly as the daemon does (`interaction.AnswerOption`/`AnswerAll`), joining a batch with `; ` like `answerText`.
- `QuestionRowView`: a non-nil but empty answer now renders "Answered" instead of falling back to the waiting state.
- Tests: 4 in `SessionProjectionTests`, 4 in `SessionViewModelTests`.
- 2026-08-07: Closed done in a backlog audit — implemented and committed; on-device use is the verification the Linux box could not provide.
