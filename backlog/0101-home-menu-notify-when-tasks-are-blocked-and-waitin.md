---
id: "0101"
title: 'Home menu: notify when tasks are blocked and waiting on the user'
status: done
priority: 2
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs: []
---

## Description
## Description

When a task gets blocked (an autonomous/loop session marks it `blocked` because it needs user input — §10/§11), that fact is passive: the loop silently skips it and the reason lives only in the task's work log. A user returning to ycc has no signal from the **home menu** that the project is waiting on them.

Surface **blocked-on-you** items on the home menu so returning users immediately see "the agent needs you." The menu already fetches modes/presets; add a blocked-task indicator alongside it.

Scope:
- Home menu (`internal/tui/tui.go`, `stateMenu` / menu view, `modesMsg`) shows a count/badge of blocked tasks (e.g. "⚠ 2 tasks blocked — waiting on you"), or nothing when zero.
- Selecting/activating it jumps to those tasks (reuse the backlog browser, filtered to `blocked`), where the user can read why each is blocked and unblock it (ties into the batch digest task 0098 and backlog grooming task 0099).
- Data comes from the existing `ListBacklog` projection (each summary already carries status); no new RPC strictly required, though a `blocked`-with-reason field may be worth adding if the reason isn't already easily surfaced.

Keep it lightweight and non-nagging: a single glanceable indicator, not a modal that interrupts.

## Acceptance criteria
- The home menu shows a blocked-task indicator with a count when one or more tasks are `blocked`, and shows nothing when none are.
- Activating the indicator routes to the blocked tasks (filtered backlog view) where the block reason is visible.
- The indicator reflects current state on menu (re)entry (e.g. after a loop run leaves a task blocked).
- Build + tests green; a test covers the indicator appearing/hiding based on backlog state.

## Acceptance criteria

## Plan

Surface a "blocked — waiting on you" indicator on the home menu and let the user jump to the blocked tasks in a filtered backlog browser.

Implementation (all in internal/tui/tui.go):
1. Data: ensure the home menu knows blocked tasks. Add `m.fetchBacklog` to `Init()` so `m.backlogTasks` is populated for the initial menu, and re-fetch on menu (re)entry so the indicator reflects current state — return `m.fetchBacklog` when transitioning to `stateMenu` after a loop ends (`applyLoopDecision`, all three menu-return branches) and on explicit back-home (`ovBackHome`). (backlogMsg already stores into m.backlogTasks.)
2. Helper `blockedTaskCount()` (or similar) counting `m.backlogTasks` with Status=="blocked".
3. menuView: when count>0, render a single glanceable banner line (e.g. "⚠ N task(s) blocked — waiting on you · press w to view") styled with an attention style; render nothing when zero. Mention `w view blocked` in the footer only when count>0.
4. Add filter field `backlogBlockedOnly bool`. In `updateMenu`, add key "w": if blockedTaskCount()>0, open the backlog browser (`m.backlog=true`, reset cursor/detail, `backlogShowDone=false`, `backlogBlockedOnly=true`) and fetch. Also reset `backlogBlockedOnly=false` in the existing ctrl+b open path so the normal browser is unfiltered.
5. `visibleBacklogTasks()`: when `backlogBlockedOnly`, return only Status=="blocked" tasks (takes precedence; ignore showDone). `backlogView()` title/hint reflect the filter (e.g. title " ycc — blocked tasks ", hint notes esc close). Block reason is already visible via `enter inspect` (TaskDetail.Body includes the work log).
6. Clear `backlogBlockedOnly=false` when the browser closes (where m.backlog is set false).

Tests (internal/tui/tui_test.go): add a test that sets m.backlogTasks with/without blocked entries and asserts menuView contains/omits the indicator; and a test that pressing "w" opens the backlog browser with backlogBlockedOnly set and visibleBacklogTasks returns only blocked tasks. Run gofmt, go build ./..., go test ./internal/tui/.

### Starting points
- internal/tui/tui.go: menuView() ~line 4046, updateMenu() ~1599, Init() ~400, applyLoopDecision() ~505, ovBackHome ~2759, visibleBacklogTasks() ~2439, backlogView() ~2452, backlogMsg handler ~1291
- model fields near line 185 (backlogTasks, backlogShowDone, backlogCursor, backlogDetail)
- block reason is visible via enter-inspect detail: TaskDetail.Body includes work log (server.go GetTask ~521)

## Work log
- 2026-07-01 plan: Surface a "blocked — waiting on you" indicator on the home menu and let the user jump to the blocked tasks in a filtered backlog browser.  Implementation (all in internal/tui/tui.go): 1. Data: ensur
…[truncated]
- 2026-07-01 context hints: 3 recorded with plan
- 2026-07-01 context hints: internal/tui/tui.go; internal/tui/tui_test.go; menuView ~4046, updateMenu ~1599, Init ~400, applyLoopDecision ~505, ovBackHome ~2759, visibleBacklogTasks ~2439, backlogView ~2452, model fields ~185, b
…[truncated]
- 2026-07-01 implementer report: Implemented a "blocked — waiting on you" indicator on the home menu (task 0101). All changes in internal/tui/tui.go (+ tests).  Changes: - Added model field `backlogBlockedOnly bool` for a blocked-o
…[truncated]
- 2026-07-01 review tier: single-opus — reviewers: Claude
- 2026-07-01 review (Claude): accept — The staged changes implement task 0101 correctly and completely. menuView() renders a single glanceable warn-styled "⚠ N task(s) blocked — waiting on you · press w to view" banner (and a "w view 
…[truncated]
- 2026-07-01 decision: accept — commit: tui: home-menu blocked-task indicator + blocked-filtered backlog view (0101)
- 2026-07-01 usage: 26,479 tok (in 158, out 26,321, cache_r 2,127,334, cache_w 83,168) · cost n/a (unpriced)
  implementer: 13,148 tok (in 96, out 13,052, cache_r 1,191,889, cache_w 37,138) · cost n/a (unpriced)
  coordinator: 9,230 tok (in 30, out 9,200, cache_r 686,528, cache_w 23,908) · cost n/a (unpriced)
  reviewer:Claude: 4,101 tok (in 32, out 4,069, cache_r 248,917, cache_w 22,122) · cost n/a (unpriced)
