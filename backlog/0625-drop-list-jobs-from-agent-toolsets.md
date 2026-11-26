---
id: "0625"
title: Drop list_jobs from agent toolsets
status: done
priority: 3
created: "2026-11-26"
updated: "2026-11-26"
depends_on: []
spec_refs:
    - spec.md#7.3 Subagents and asynchronous jobs
    - docs/design/async-jobs.md
---

## Description
## Status
Done 2026-11-26 (accepted by the owner, 2026-11-26, chat session reviewing coordinator tool count).

## Why
`list_jobs` is registered in `tools.JobTools` for every editing coordinator and mutating subagent, but usage across 482 recent coordinator sessions (12-day cross-project audit, 2026-11-26) was 7 calls total (~1.5% of sessions). Every background `Bash`, `spawn_agent`, and `spawn_reviewers` call already returns its `job_id`, completions push automatically, `wait` with no `job_ids` waits on all live jobs and reports still-running IDs on timeout, and `list_sessions` lists running jobs per session. Dropping `list_jobs` removes one tool definition from every editing coordinator and mutating subagent without losing job visibility.

## Scope
- Remove `list_jobs` (`listJobsTool`) from `tools.JobTools` in `internal/tools/jobs.go` and remove its direct tool test in `internal/tools/jobs_test.go`.
- Keep `jobs.Registry.List` intact for daemon/session inspection (`list_sessions`, reopen/restart summaries, UI/RPC projections, tests).
- Update prompts, runtime notes, and docs that name `list_jobs` as a current tool: `internal/session/restart_state.go` (reopen/restart context note), `internal/jobs/jobs.go` comments, `docs/design/async-jobs.md`, and `spec.md` §7.3.
- Add or verify a reopen/replay test ensuring an existing session whose event log contains `list_jobs` tool calls still reopens and replays cleanly (following `TestReopenReplaysToolTheModeNoLongerOffers` from 0538, coordinator tool consolidation).

## Acceptance criteria
- No agent mode or subagent role offers `list_jobs`.
- No current prompt, runtime notice, spec section, or design doc names `list_jobs` as an available tool (historical reports/backlog logs untouched).
- Existing sessions with `list_jobs` calls in their event log reopen and replay cleanly (test).
- `go test ./internal/...` passes.

## Outcome

`tools.JobTools` now returns only `job_output`, `wait` and `kill_job`, so no coordinator mode or subagent role offers `list_jobs`. `jobs.Registry.List` stays for `list_sessions`, watch summaries and RPC views. The restart context note now says that reports of jobs still running arrive automatically and that `wait` with no ids waits on them. Comments in `internal/jobs/jobs.go`, `docs/design/async-jobs.md` and spec §7.3 no longer name the tool. Tests: `TestModeCoordinationAndJobTools` checks that pm, watch, chat and work don't offer it, and `TestReopenReplaysToolsModesNoLongerOffer` (renamed) replays a `list_jobs` call in watch, chat and work logs. Both fail against the old `jobs.go`. `go test ./internal/...` passed on HEAD plus these changes in a private worktree, since the shared tree held 0627's in-progress edits. Landed with a hand-scoped commit taking only this task's spec.md hunks, because spec.md also held 0627's uncommitted hunks.

Commit: 0625: drop list_jobs from agent toolsets
