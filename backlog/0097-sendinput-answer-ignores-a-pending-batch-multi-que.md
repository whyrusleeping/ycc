---
id: "0097"
title: SendInput/Answer ignores a pending batch (multi-question) ask_user
status: done
priority: 4
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs: []
---

## Description
## Description

`interaction.Answer` (and therefore `Session.SendInput`) only claims the single-question `waiting` channel, not the batch `batchWaiting` channel. When the coordinator has a **multi-question** `ask_user` (`AskMany`) pending and a client calls `SendInput` with plain text, `Answer` returns false, so the text falls through and is buffered into `inputCh`. But the loop is blocked inside `AskMany` and never drains `inputCh` (it only reads it when idle), so the answer is silently lost and the session appears to hang. The user even sees their message echoed (via the UserInput event), compounding the confusion.

The primary TUI is unaffected because it routes batch answers through the `AnswerQuestions` RPC (`wizActive` gate). This only bites non-TUI / scripted clients (e.g. the `ycc send` CLI) and any client that answers via `SendInput`.

Relevant code:
- `internal/session/interaction.go` — `Answer`, `AnswerAll`, `batchWaiting`
- `internal/session/session.go` — `SendInput`

## Acceptance criteria
- A plain-text `SendInput`/`Answer` while a batch `ask_user` is pending is either applied to the batch (e.g. as the first/only answer, or rejected with a clear error) rather than silently buffered into `inputCh` and lost.
- No regression to single-question answering or normal prods.
- A test covers SendInput arriving while a batch question is pending.

## Acceptance criteria

## Plan

Problem: `interaction.Answer` only claims the single-question `waiting` channel. When a batch (multi-question) ask_user is pending (`batchWaiting` non-nil), a plain-text `SendInput` falls through Answer, gets buffered into `inputCh` (the loop is blocked in AskMany and never drains it), and is silently lost.

Fix (apply-to-batch, not reject — friendlier for scripted/CLI clients like `ycc send`):
1. In `internal/session/interaction.go`, extend `Answer(text string) bool`:
   - Under the mutex, first check `waiting` (existing behavior, unchanged).
   - If `waiting` is nil but `batchWaiting` is non-nil, claim the batch channel + questions (nil them out, same claim-under-lock discipline as `AnswerAll`), and deliver a combined-reply answer slice: `out[0] = text`, and for i > 0 a clear marker such as "(the user replied with a single free-form message; see the answer to Q1)". This flows back through AskMany's existing "Q1/A1..." formatting so the model sees the full text once, unambiguously, and knows the other slots weren't individually answered.
   - Return true so `SendInput` / `Session.Answer` treat it as delivered (no inputCh buffering, no bogus "no pending question" error).
2. Update doc comments on `Answer` (interaction.go) and `SendInput` (session.go) to describe the batch behavior.
3. Tests in `internal/session/interaction_many_test.go` (or a new file):
   - `Answer` with a pending batch returns true; AskMany unblocks; answers[0] == text, answers[i>0] carry the see-A1 marker; batchWaiting/batchQuestions are cleared afterward (pending() false).
   - `Answer` with no pending anything still returns false; single-question path regression-checked (existing tests cover it — keep them green).
   - A session-level test that a `SendInput` while a batch is pending answers the batch rather than being buffered into inputCh (model existing session tests for setup; if constructing a full Session is disproportionate, an interaction-level test that exercises exactly the SendInput code path via inter.Answer is acceptable, but prefer the Session-level test since the acceptance criteria names SendInput).
4. Leave `AnswerOption` and `AnswerAll` as-is (AnswerOption with a batch pending already returns a hard error via the RPC — not silent — and the TUI gates batch answers through AnswerQuestions).
5. Verify: go build ./... && go vet ./... && go test ./internal/session/... (plus full test run).

### Starting points
- internal/session/interaction.go — Answer (~line 238), AnswerAll (~278), batchWaiting/batchQuestions fields
- internal/session/session.go — SendInput (~line 162)
- internal/session/interaction_many_test.go — existing batch test conventions (waitBatchPending helper)
- internal/orchestrator/orchestrator.go ~632-647 — how AskMany answers are formatted back to the model (Q1/A1 pairs)

## Work log
- 2026-07-02 plan: Problem: `interaction.Answer` only claims the single-question `waiting` channel. When a batch (multi-question) ask_user is pending (`batchWaiting` non-nil), a plain-text `SendInput` falls through Answ
…[truncated]
- 2026-07-02 context hints: 4 recorded with plan
- 2026-07-02 context hints: internal/session/interaction.go — Answer (~238), AnswerAll (~278), batchWaiting/batchQuestions fields; internal/session/session.go — SendInput (~162); internal/session/interaction_many_test.go —
…[truncated]
- 2026-07-02 implementer report: Fixed Task 0097: a plain-text SendInput/Answer while a batch (multi-question) ask_user is pending was silently buffered into inputCh and lost, because interaction.Answer only claimed the single-questi
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change correctly fixes the bug: interaction.Answer now claims a pending batch (batchWaiting) when no single question is waiting, delivering the free-form text to the first answer slot with a clear
…[truncated]
- 2026-07-02 decision: accept — commit: session: deliver free-text answers to a pending batch ask_user instead of losing them (0097)
