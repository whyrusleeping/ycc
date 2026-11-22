---
id: "0550"
title: 'Backlog groom: decisions needed'
status: proposed
priority: 3
created: "2026-11-14"
updated: "2026-11-21"
depends_on: []
spec_refs: []
---

## Description
Unattended backlog grooms record here the questions that evidence could not settle. Each entry gives the task, the problem, the evidence, and the decision needed. An entry is dropped once it is answered.

## Flags

- **0471 (session fork and rewind from a user turn, todo, p2).**
  - Problem: the task is accepted and eligible for the work loop, but it has no acceptance criteria. Its sketch is marked unconfirmed, and two open questions have no answer. Should file restore be in v1? How should forks of sessions with live jobs, pending questions, or task focus be handled?
  - Evidence: the user moved it from proposed to todo on iOS on 2026-11-13 (client analytics `task.status` at 03:20:31Z). The text has said "to confirm before acceptance" since it was filed in session s_9fdcf317e49ae2b7.
  - Decision needed: answer the open questions and confirm or amend the sketch, so the task gets acceptance criteria. Or state that the implementing session may decide them.
- **0520 (iOS quick-reply swipe menu: non-message actions, todo, p4).**
  - Problem: the task is accepted, but its acceptance criterion says "chosen actions", and no actions have been chosen. The candidates are photo, new session, and voice.
  - Evidence: on 2026-11-10 the owner said "will think on it" (session s_0f66864a490b7ffc, event 29). The user moved the task to todo on iOS on 2026-11-13 (analytics, 03:20:16Z).
  - Decision needed: which actions go into the swipe menu.
- **0548 (TestGetBacklogGroom fix, proposed, p2).**
  - Problem: the fix landed while the task was still proposed. The task is now stale.
  - Evidence: `4d38b28` (2026-11-19, "server: TestGetBacklogGroom expects done tasks to be excluded from groom churn") changed the fixture to expect `PendingChanged == 2`. `go test ./internal/server/ -run TestGetBacklogGroom` passed on 2026-11-21.
  - Decision needed: close 0548 as done with `4d38b28` as its outcome, or delete it. A groom does not change a proposed task's status without the user.
- **0568 (surface history rewrites and stashes on a shared HEAD, todo, p2) and 0511 (protect live session state from working-tree git operations, proposed, p3).**
  - Problem: the two tasks overlap in part. One of 0511's "harden" options is to have tools refuse git operations that rewrite `.ycc` paths. 0568 adds Bash-tool detection of reset, amend, stash and plumbing commits on a shared HEAD. The two would build similar detectors separately.
  - Evidence: the 0511 Description options list, and the 0568 Description and acceptance criteria.
  - Decision needed: should the 0568 detector also cover git operations that would rewrite or roll back `.ycc` session state, with 0511 narrowed to relocating that state? Or should the two tasks stay separate?

## Work log
- 2026-11-21 groom: kept the 0471 and 0520 flags, which still apply. Rewrote the 0548 flag, because its fix landed in `4d38b28`. Added the 0568/0511 overlap flag. Removed an empty Acceptance criteria heading.
