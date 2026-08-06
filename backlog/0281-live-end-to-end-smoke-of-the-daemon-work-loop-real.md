---
id: "0281"
title: Live end-to-end smoke of the daemon work loop (real sessions) + plans/work-loop-smoke.md
status: todo
priority: 2
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - 9. Modes (the home menu)
    - "20.6"
---

## Description
The daemon-side work loop (task 0179) is well covered by unit tests, but every one of them substitutes the injectable `runSession` seam with a fake. The real path — `workLoop.realRunSession`: `Manager.Start(mode work, Unattended)` → poll `sess.Status()` until Idle/Error → snapshot + price the event log → `BudgetBreached` → `reclaim` — has never been exercised against a real model, nor has the whole flow been driven from a client (TUI or iOS).

## Scope
- Write `plans/work-loop-smoke.md` (mirroring `plans/remote-access-smoke.md`): start a daemon on a scratch workspace with 2–3 small ready backlog tasks, start a loop via `StartWorkLoop`, observe `GetWorkLoop` advancing through sessions, gracefully `StopWorkLoop` mid-drain, and verify the end-of-batch digest + the ntfy `digest` push.
- Execute the runbook against a real model and fix what it surfaces (likely candidates: the Idle==done assumption for unattended sessions, focus/commit/verdict extraction, pricing/`price_status` roll-up, reclaim timing, stop-while-between-sessions).
- Add regression tests for any defect found; if `realRunSession` proves testable with a stub model/backend, add a test that drives it without the fake seam.

## Acceptance criteria
- `plans/work-loop-smoke.md` exists and has been executed end to end at least once.
- A real (non-fake) loop drains a small backlog, reports accurate state via `GetWorkLoop` throughout, stops gracefully on request, and produces a correct digest + notifier push.
- Any defect found is fixed with a regression test; `go build ./... && go test ./...` green.

## Work log
