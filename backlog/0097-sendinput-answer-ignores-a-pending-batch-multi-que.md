---
id: "0097"
title: SendInput/Answer ignores a pending batch (multi-question) ask_user
status: todo
priority: 4
created: "2026-07-01"
updated: "2026-07-01"
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

## Work log
