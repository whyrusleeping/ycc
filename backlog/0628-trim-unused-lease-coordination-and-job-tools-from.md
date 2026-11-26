---
id: "0628"
title: Trim unused lease, coordination, and job tools from pm and watch modes
status: done
priority: 4
created: "2026-11-26"
updated: "2026-11-26"
depends_on: []
spec_refs:
    - spec.md#7.3 Subagents and asynchronous jobs
    - spec.md#9. Modes
---

## Description
## Status
Done 2026-11-26 (accepted by the owner, 2026-11-26, chat session reviewing coordinator tool count; lower priority).

## Why
`BuildMode` (`internal/orchestrator/modes.go`) currently adds session coordination, exclusive leases, and all job tools (via `tools.Editing`) to `pm` and `watch` modes even though neither uses them: across a 12-day cross-project audit (2026-11-26), `pm` (15 sessions) had 0 calls to all 6 coordination/lease tools and all 4 job tools, and `watch` (90 sessions) had 0 calls to `acquire_lease` and `release_lease`. Scoping these tools out of modes that do not use them shrinks their per-turn schema overhead.

## Scope
- **`pm` mode:** do not register exclusive lease tools (`acquire_lease`, `release_lease`), notice-board write tools (`set_intent`/`post_notice`/`clear_notice` or unified `notice`), or background job tools (`job_output`, `wait`, `kill_job`, and `list_jobs` if still present; `Bash` in `pm` runs foreground-only without `ws.Jobs`). Decide during implementation whether `pm` keeps read-only `list_sessions` (e.g., to see live sessions before grooming) or omits it too; document the choice in `spec.md`.
- **`watch` mode:** do not register `acquire_lease` or `release_lease` (watches keep `list_sessions`, notice-board reporting, and `spawn_agent`/job tools).
- Update `internal/orchestrator/modes.go`, `internal/orchestrator/modes_test.go`, and `spec.md` §7.3 / §9.
- Ensure existing `pm` or `watch` sessions whose history contains any now-omitted tool still reopen and replay cleanly.

## Acceptance criteria
- `pm` mode does not offer lease tools, notice-board write tools, or background job tools (`modes_test.go`).
- `watch` mode does not offer `acquire_lease` or `release_lease` (`modes_test.go`).
- `spec.md` §7.3 and §9 match the registered toolsets.
- `go test ./internal/...` passes.

## Outcome

pm sessions no longer get `acquire_lease`/`release_lease`, `notice` (or the board guidance paragraph) or the background job tools. Their workspace has no job registry or supervisor, so `Bash` runs foreground only, without `run_in_background`. pm keeps read-only `list_sessions` to see live sessions before grooming, and still receives notice-board digests at checkpoints. Watch runs no longer get the lease tools and keep `list_sessions`, `notice`, generic subagents and job tools. Spec §7.3 (job controls, `list_sessions`, notice board, exclusive leases), §9 (pm) and §9.2 (watch) document this. Tests: `TestModeCoordinationAndJobTools` checks pm, watch, chat and work toolsets with board, leases and jobs all available, and checks that a pm background Bash call is refused. `TestReopenReplaysToolsPmAndWatchNoLongerOffer` reopens pm and watch logs that call the omitted tools, replays them, and confirms the reopened session does not offer those tools. `go test ./internal/...` passed on HEAD plus these changes, built in a private worktree. The sol reviewer accepted in round 1. Landed with a hand-scoped commit because modes.go and spec.md also held 0623's uncommitted hunks.

Commit: 0628: trim lease, notice and job tools from pm and lease tools from watch