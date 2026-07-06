---
id: "0168"
title: 'Work loop: consider tasks added during the loop when picking future work'
status: done
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs: []
---

## Description

Tasks that get added while a work-loop session is running (e.g. split-off / follow-on tasks the coordinator files via `create_task`, or user quick-capture adds via ctrl+n / `ycc task add`) do not appear to be considered by the same work loop when it picks the next task. First verify whether this is actually the case: check how the loop driver (TUI "work (loop)" toggle) selects the next task between iterations — whether it re-reads the live backlog each cycle or works from a snapshot/queue built when the loop started.

If confirmed, fix it so newly created tasks are pushed onto the loop's queue for future iterations, with the same eligibility rules as the initial pick:

- status is `todo` (never `proposed`, `blocked`, or `in_review`)
- all dependencies are done (`[READY]`)
- it doesn't require user input (respect the blocked/needs-user semantics)

Note: within a single coordinator session the "THE BACKLOG IS LIVE" prompt guidance already tells the coordinator not to chase mid-session additions — this task is about the loop driver's between-session selection, not changing that in-session behavior.

## Acceptance criteria

- Investigation note recorded (work log or code comment) confirming or refuting the current behavior.
- A task created mid-loop with status `todo` and satisfied dependencies is picked up by a subsequent loop iteration without restarting the loop.
- Tasks that are `proposed`, `blocked`, dependency-blocked, or otherwise need user input are NOT picked up.
- Test covering the mid-loop task-addition path.

## Plan

Investigation (done, coordinator): the work-loop driver does NOT work from a snapshot/queue. Each iteration re-reads the live backlog: on streamClosedMsg while looping, tui.go calls loopNext() → ListBacklog RPC → server.ListBacklog → docs.NewStore(workspace).List() which re-reads the backlog directory from disk every call → topReadyTask picks the highest-priority Ready task with status todo/in_progress → applyLoopDecision starts the next session. So a task created mid-loop (coordinator create_task, ycc task add, ctrl+n quick capture) with status todo and satisfied deps IS already considered on the next iteration. Premise REFUTED — no behavioral fix needed. Eligibility rules already hold: topReadyTask skips proposed/blocked/in_review/done by status and dependency-blocked tasks via Ready=false; loop sessions run autonomous so needs-user tasks get marked blocked and are then skipped.

Remaining work (tests + note only, no behavior change):
1. Add a code comment on loopNext() (internal/tui/tui.go) recording the investigation result: the backlog is re-read live each iteration, so tasks added mid-loop are considered by subsequent picks (task 0168).
2. Add a test in internal/tui/tui_test.go covering the mid-loop task-addition path end to end at the driver level: start with a looping model whose fakeClient backlogList contains task A (todo, ready); apply the first loop decision (picks A, session starts); then mutate fc.backlogList to simulate mid-loop changes — A done, plus newly added tasks: B (todo, ready), C (proposed, no deps → Ready true but wrong status), D (todo, Ready=false dep-blocked), E (blocked). Deliver streamClosedMsg through Update (looping state) and execute the returned loopNext command against the fake client; assert the resulting loopDecisionMsg picks B (not C/D/E), and that applyLoopDecision continues the loop with a startSession command.
3. Extend TestTopReadyTask with a "proposed" status case (Ready=true but status proposed must not be picked).
4. Run go build ./... and go test ./internal/tui/... (or full go test ./...).

### Starting points
- internal/tui/tui.go: loopNext() ~line 947, applyLoopDecision ~1301, topReadyTask ~1387, streamClosedMsg case ~2533
- internal/tui/tui_test.go: fakeClient.ListBacklog ~1151 returns f.backlogList (mutable per-test); existing loop tests TestTopReadyTask/TestLoopDecision* ~3960-4052
- internal/server/server.go ListBacklog ~518: fresh store.List() per call; internal/docs/docs.go List() re-reads dir

## Work log
- 2026-07-06 plan: Investigation (done, coordinator): the work-loop driver does NOT work from a snapshot/queue. Each iteration re-reads the live backlog: on streamClosedMsg while looping, tui.go calls loopNext() → Lis
…[truncated]
- 2026-07-06 context hints: 3 recorded with plan
- 2026-07-06 context hints: internal/tui/tui.go:947 loopNext, 1301 applyLoopDecision, 1387 topReadyTask, 2533 streamClosedMsg case (loop branch calls m.loopNext()); internal/tui/tui_test.go:1151 fakeClient.ListBacklog returns f.
…[truncated]
- 2026-07-06 implementer report: Task 0168 complete. The coordinator's investigation was confirmed: the work-loop driver keeps NO snapshot/queue — every iteration re-reads the LIVE backlog (streamClosedMsg while looping → loopNex
…[truncated]
- 2026-07-06 review tier: simple (coordinator self-review)
- 2026-07-06 decision: accept — commit: Work loop: verify mid-loop task additions are picked up; lock in with tests (task 0168)  Investigation refuted the premise: the loop driver keeps no snapshot/queue — every iteration re-reads the liv
…[truncated]
- 2026-07-06 usage: 15,038 tok (in 78, out 14,960, cache_r 910,010, cache_w 56,040) · cost n/a (unpriced)
  coordinator: 10,062 tok (in 52, out 10,010, cache_r 713,708, cache_w 34,369) · cost n/a (unpriced)
  implementer: 4,976 tok (in 26, out 4,950, cache_r 196,302, cache_w 21,671) · cost n/a (unpriced)
