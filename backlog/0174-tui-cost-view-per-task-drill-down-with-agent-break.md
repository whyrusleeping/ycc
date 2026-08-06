---
id: "0174"
title: 'TUI cost view: per-task drill-down with agent breakdown'
status: done
priority: 4
created: "2026-07-07"
updated: "2026-08-06"
depends_on:
    - "0173"
spec_refs:
    - "20.5"
---

## Description
The TUI cost modal (spec §20.5, task 0039) currently supports a single group-by dimension cycled with "g" (task|model|session|day|agent). It cannot show the per-task per-agent breakdown ("task 0093: coordinator $12, implementer $19, reviewer $5") that the CLI provides via `ycc cost --by task,agent`, and there is no per-task drill-down.

Add a drill-down: with the cost view grouped by task, pressing enter on a task row opens that task's breakdown grouped by agent (using the GetUsage task filter from task 0173). Esc goes back up to the task table rather than closing the modal outright.

Acceptance criteria:
- In the cost view grouped by task, enter on a row shows only that task's usage, grouped by agent, with a TOTAL row; title/header makes the focused task obvious.
- Within the drill-down, "g" still cycles dimensions (e.g. agent → model) while staying filtered to the task.
- Esc from the drill-down returns to the task-level table (cursor preserved); esc again closes the modal.
- Works for the "(unattributed)" row (empty task id) too, or that row is clearly non-drillable.
- Snapshot/TUI tests cover the drill-down rendering.

## Acceptance criteria

## Plan

Add a per-task drill-down to the TUI cost modal (spec §20.5), reusing the server-side GetUsage `task` filter added in task 0173.

1. Model state (internal/tui/tui.go, cost-view block near line 404):
   - Add `costTask string` — non-empty means the view is drilled into one backlog task.
   - Add `costTaskCursor int` — the task-table cursor to restore when leaving the drill-down.
   (Keep field comments in the house style: explain the "why", reference §20.5 / task 0174.)

2. `fetchUsage` (~line 1908): pass `Task: m.costTask` on the GetUsageRequest so the daemon filters server-side; everything else unchanged.

3. `updateCost` (~line 4846):
   - `enter`: only meaningful when not already drilled and the current group-by dimension is `task`. Take the cursor row; if its `Task` is empty ("(unattributed)") do nothing (that row is non-drillable — see the hint in step 4). Otherwise save `costTaskCursor = costCursor`, set `costTask = row.Task`, `costGroupBy = []string{"agent"}`, `costCursor = 0`, `costRows/costTotal = nil`, `costMsg = "loading…"`, and return `m.fetchUsage`.
   - `esc`/`q`: if `costTask != ""`, pop back up instead of closing — clear `costTask`, restore `costGroupBy = []string{"task"}` and `costCursor = costTaskCursor`, clear rows, `costMsg = "loading…"`, re-fetch. Otherwise close the modal as today.
   - `g`: while drilled, cycle a task-scoped order that excludes `task` (a new `var costDrillGroupOrder = []string{"agent", "model", "session", "day"}`), staying filtered (fetchUsage still sends `Task`). Unchanged top-level behaviour otherwise. Reset cursor to 0 as today.

4. `costView` (~line 4996) — make the focus obvious and the affordances discoverable:
   - When `costTask != ""`, append `· task <id>` to the modal title (after the workspace segment).
   - Hint: while drilled, use `esc back` instead of `esc close`; at the task level append an `enter breakdown`-style affordance, and when the cursor row is the `(unattributed)` row say it is not drillable (e.g. `enter n/a for (unattributed)`), so the non-drillable case is clear without transient state.
   - Keep the existing scroll/window/TOTAL/partial logic intact (the drill-down must still window correctly and keep its TOTAL row).

5. Help (internal/tui/help.go, "cost" section ~line 135): add `enter` → "task breakdown by agent" and change `esc / q` to "close · back from the task drill-down"; keep the grouping line accurate.

6. Tests (internal/tui/tui_test.go):
   - Extend `fakeClient.GetUsage` (~line 1319) to record `lastUsageTask` and, when the request carries a task, return only rows matching it (plus a suitable total) — canned drill-down rows with distinct agents (coordinator/implementer/reviewer) so the breakdown is meaningful.
   - New test: from the task-grouped cost view, `enter` on the "0001" row sends the filter (assert `f.lastUsageTask == "0001"` and `m.costGroupBy == ["agent"]`), renders agent rows + TOTAL + the task id in the title; `g` keeps the filter (lastUsageTask still "0001") while moving to the next dimension; `esc` returns to the task table with the cursor preserved and no task filter; a second `esc` closes the modal.
   - Cover the "(unattributed)" (empty task) row: `enter` on it does not drill (still `costTask == ""`, no filtered fetch) and the view says so.
   - Add a snapshot-style rendering assertion for the drill-down: either a table-render check in tui_test.go or, better for the "snapshot/TUI tests" criterion, a `TestSnapshotCostDrilldown` in internal/tui/snapshot_test.go that builds a model with `cost: true`, `costTask: "0093"`, agent rows and renders via `snapshot.RenderANSI` with the same in-memory assertions the existing snapshot tests use (and the optional YCC_TUI_SNAPSHOT_DIR write, file name `cost_drilldown.png`).

7. Docs: no CLI change; if docs/tui.md (or equivalent) enumerates cost-view keys, add enter/esc-back there. Spec §20.5 needs no change (it does not enumerate keys) — leave it unless it becomes inaccurate.

Verify with `go build ./... && go test ./internal/tui/...` then `go test ./...` (known flakes: internal/session TestReconcileWorkstreams, internal/setup TestConfigPath, internal/tools TestBackgroundBashWaitReturnsExitAndOutput — check against HEAD before blaming this change).

## Work log
- 2026-08-06 plan: Add a per-task drill-down to the TUI cost modal (spec §20.5), reusing the server-side GetUsage `task` filter added in task 0173.  1. Model state (internal/tui/tui.go, cost-view block near line 404): 
…[truncated]
- 2026-08-06 implementer report: Implemented task 0174’s TUI cost drill-down.  Changes: - Added task drill-down/filter state and preserved parent task-table cursor. - `GetUsage` now receives the selected task filter. - Enter on an 
…[truncated]
- 2026-08-06 review tier: single-opus — reviewers: sol
- 2026-08-06 review (sol): revise — The drill-down state, filtering, rendering, help text, unattributed behavior, and tests otherwise align well with task 0174, and the full Go build/test suite passes. However, asynchronous usage respon
…[truncated]
- 2026-08-06 revision: Addressed the stale/out-of-order GetUsage response race.  Changes: - Added `costGen` request generation state, documented as the task 0174 stale-response guard. - Added matching generation metadata to
…[truncated]
- 2026-08-06 review (sol): accept — The revision resolves the prior out-of-order response defect by generation-tagging usage requests, rejecting stale responses, and adding a focused regression test for drill-down followed by back navig
…[truncated]
- 2026-08-06 decision: accept — commit: tui: per-task cost drill-down with agent breakdown (§20.5)
