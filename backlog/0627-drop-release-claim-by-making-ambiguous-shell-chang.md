---
id: "0627"
title: Drop release_claim by making ambiguous shell changes opt-in and tightening background-job overlap
status: todo
priority: 3
created: "2026-11-26"
updated: "2026-11-26"
depends_on: []
spec_refs:
    - spec.md#7.3 Subagents and asynchronous jobs
---

## Description
## Status
Accepted (owner decision, 2026-11-26, chat session reviewing coordinator tool count — Fixes 1 and 2). Not started.

## Why
`release_claim` was called 101 times across 49 sessions in a 12-day cross-project audit (2026-11-26; 95 in `work`, 6 in `chat`). Sampled calls show nearly every use was an innocent session cleaning up false-positive claims created by `internal/tools/shellclaims.go` (from 0594, ambiguous shell attribution, and 0609, short-shell background-job carve-out) when a read-only or unrelated shell command (`git status`, `ssh`, remote Mac build) overlapped another session's writes. Because ambiguous claims are currently included in `changes.Paths` and block other sessions' commits (`internal/orchestrator/finalization.go`), agents had to manually call `release_claim` before reviewing or committing. Flipping ambiguous shell changes to opt-in and removing the false-definite short-shell carve-out eliminates the need for `release_claim`.

## Scope
- **Fix 1 — Make ambiguous shell changes opt-in instead of claimed:**
  - When a `Bash` command finishes and dirty paths changed while another session's shell ran concurrently, do not add those paths to any session's `changes.Paths`, review diff, or `commit` changeset, and do not let them block another session's `commit`.
  - Keep the informational `Bash` result notice listing the unattributed paths changed during concurrent shells, directing the model to pass any paths that are its own in `commit(paths=[...])`.
  - Update `workspacelease`, `internal/tools/shellclaims.go`, `internal/orchestrator/finalization.go`, `internal/orchestrator/orchestrator.go`, and `list_sessions` / `list_backlog` / client views (0603, separate ambiguous claim display) where ambiguous claims were previously tracked as session claims.
- **Fix 2 — Prevent false definite claims across concurrent background jobs:**
  - Remove the `shortShellLimit` carve-out (`internal/tools/shellclaims.go`, `internal/workspacelease`) that treated another session's running background job as non-concurrent with foreground commands under 1 minute, so a short foreground shell (`git status`, `ssh`, etc.) cannot receive a definite claim on files written by another session's background job. Any path changed while another session has a running shell (foreground or background) stays unattributed (opt-in via `commit(paths=[...])`) unless this session already definitely claims it.
- **Remove `release_claim`:**
  - Remove `release_claim` (`releaseClaim` in `internal/orchestrator/coordination.go`) from `chat` and `work` modes, along with references to it in `finalization.go`, `shellclaims.go`, `orchestrator.go`, and `spec.md` §7.3.
  - Keep `event.ClaimReleased` event-type decoding and reopen/replay support for historical logs containing `release_claim` calls.

## Acceptance criteria
- No mode offers `release_claim`; prompts, error messages, and `spec.md` do not direct agents to call `release_claim`.
- When two sessions' shells overlap in a shared worktree and a path changes, neither session gets a claim on it: it is absent from both sessions' `changes.Paths` / review diffs / default `commit` changesets, neither session's `commit` is blocked by the other on that path, `Bash` reports it as unattributed, and either session can commit it by naming it in `commit(paths=[...])` (tests).
- When session A runs a short foreground `Bash` while session B has a running background `Bash` that writes a file, session A does not receive a definite claim on B's file (test).
- Direct `Edit`/`Write` claims and lone-shell claims (no concurrent shell of another session) continue to attribute definitely as before (existing tests).
- Historical sessions containing `release_claim` calls and `claim_released` events still reopen and replay cleanly (test).
- `go test ./internal/...` passes.

## Work log
