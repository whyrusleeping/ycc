---
id: "0183"
title: 'iOS: session interactions — input bar, answer sheets, interrupt/resume/stop + smoke runbook'
status: todo
priority: 2
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0182"
spec_refs:
    - "18.3"
    - "18.7"
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Interactive controls on the session view per `docs/design/ios-client.md` §6 phase 1 step 4 — completes the "observe, answer, control" milestone.

## Description
- Sticky bottom input bar → `SendInput` (prod running/idle sessions; steer-by-default queues it).
- Question sheet on `question_asked`: suggested options as buttons + free-text field; single → `AnswerQuestion`, batched ask_user → `AnswerQuestions` (positional answers[i]); `optionIndex >= 0` selects an option, `-1` sends text; sheet dismissed by `question_answered` (incl. when answered from another client). Handle `failed_precondition` ("no pending question") gracefully.
- Toolbar/overflow actions: `Interrupt` (graceful pause-to-steer), `Resume`, `StopSession` (confirmation dialog — hard terminate).
- Reflect session state in chrome (paused banner after `interrupted`, idle/error banners).
- Add `plans/ios-client-smoke.md`: runbook mirroring plans/remote-access-smoke.md — daemon on a tailnet addr + token, connect, watch live, answer an ask_user, kill network mid-stream and verify replay-from-seq reconnect, interrupt/steer/resume, stop.

## Acceptance criteria
- Answering options and free text both work against a live daemon; batch questions answered positionally.
- Interrupt/Resume/Stop round-trip and the feed reflects the resulting events.
- Error toasts for not_found/failed_precondition; no crash on races (question answered elsewhere).
- Smoke runbook committed; YccKit interaction view-model logic under `swift test`.

## Work log
