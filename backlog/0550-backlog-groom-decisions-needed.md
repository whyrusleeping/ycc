---
id: "0550"
title: 'Backlog groom: decisions needed'
status: proposed
priority: 3
created: "2026-11-14"
updated: "2026-11-14"
depends_on: []
spec_refs: []
---

## Description
The unattended backlog grooms collect here the questions that evidence could not settle. Each entry gives the task, the problem, the evidence, and the decision needed. Drop an entry once it is answered.

## Flags

- **0471 (session fork and rewind from a user turn, todo, p2).**
  - Problem: the task is accepted and eligible for the work loop, but it has no acceptance criteria. Its sketch is marked unconfirmed, and its two open questions are unanswered: should file restore be in v1, and how should forks of sessions with live jobs, pending questions, or task focus be handled?
  - Evidence: the user moved it from proposed to todo on iOS on 2026-11-13 (client analytics `task.status` at 03:20:31Z). The text has said "to confirm before acceptance" since it was filed in session s_9fdcf317e49ae2b7.
  - Decision needed: answer the open questions and confirm or amend the sketch, so the task gets acceptance criteria. Otherwise, state that the implementing session may decide them.
- **0520 (iOS quick-reply swipe menu: non-message actions, todo, p4).**
  - Problem: the task is accepted, but its acceptance says "chosen actions" and no actions have been chosen. The candidates are photo, new session, and voice.
  - Evidence: on 2026-11-10 the owner said "will think on it" (session s_0f66864a490b7ffc, event 29). The user moved the task to todo on iOS on 2026-11-13 (analytics, 03:20:16Z).
  - Decision needed: which actions go into the swipe menu.
- **0548 (TestGetBacklogGroom fix, proposed, p2).**
  - Problem: `go test ./internal/server/` fails at HEAD, and recent work sessions report the failure as a known issue. The original diagnosis, a hardcoded date, was wrong.
  - Evidence: the 2026-11-14 groom found that the test passes at d8d7702 and fails at 65ff85c, which leaves done tasks out of groom churn while the fixture still expects 3. The task now describes this cause.
  - Decision needed: accept 0548 (promote it to todo) or leave it proposed.


## Acceptance criteria

## Work log
