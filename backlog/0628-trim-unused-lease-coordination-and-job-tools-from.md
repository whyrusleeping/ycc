---
id: "0628"
title: Trim unused lease, coordination, and job tools from pm and watch modes
status: todo
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
Accepted (owner decision, 2026-11-26, chat session reviewing coordinator tool count; lower priority). Not started.

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

## Work log
