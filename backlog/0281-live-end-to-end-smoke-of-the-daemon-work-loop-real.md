---
id: "0281"
title: Live end-to-end smoke of the daemon work loop (real sessions) + plans/work-loop-smoke.md
status: blocked
priority: 2
created: "2026-08-06"
updated: "2026-09-17"
depends_on: []
spec_refs:
    - 9. Modes (the home menu)
    - "20.6"
---

## Description
The daemon-side work loop (task 0179) has unit coverage, including tests that exercise `realRunSession` through a narrower `startSession` seam. The remaining verification gap is a documented client-driven, real-model end-to-end run covering session lifecycle, event-log accounting, budgets, reclaim, graceful stop and digest delivery; do not duplicate runner tests already present. Use a scratch workspace, explicit bounded spend/runtime, and an authorized notification destination. If credentials, budget or notification setup are unavailable, report the missing gate rather than substituting fake evidence.

## Scope
- Write `plans/work-loop-smoke.md` (mirroring `plans/remote-access-smoke.md`): start a daemon on a scratch workspace with 2–3 small ready backlog tasks, start a loop via `StartWorkLoop`, observe `GetWorkLoop` advancing through sessions, gracefully `StopWorkLoop` mid-drain, and verify the end-of-batch digest + the ntfy `digest` push.
- Execute the runbook against a real model and fix what it surfaces (likely candidates: the Idle==done assumption for unattended sessions, focus/commit/verdict extraction, pricing/`price_status` roll-up, reclaim timing, stop-while-between-sessions).
- Add regression tests for any defect found; if `realRunSession` proves testable with a stub model/backend, add a test that drives it without the fake seam.

## Acceptance criteria
- `plans/work-loop-smoke.md` exists and has been executed end to end at least once.
- A real (non-fake) loop drains a small backlog, reports accurate state via `GetWorkLoop` throughout, stops gracefully on request, and produces a correct digest + notifier push.
- Any defect found is fixed with a regression test; `go build ./... && go test ./...` green.

## Work log
- 2026-09-17 preflight: global `~/.config/ycc/ycc.toml` has an empty `[notify]` section, project `.ycc/config.toml` has no notification destination, and no notification environment setting is present. The required authorized ntfy digest destination is unavailable; no live loop was started. Unblock by configuring/authorizing a notification destination for the bounded scratch smoke. This administrative update was deferred until after task 0284 finalization because the changeset ownership guard rejected modifying this already-dirty, unrelated task during that task's implementation.
