---
id: "0619"
title: Startup passive supervision of surviving jobs in closed sessions
status: todo
priority: 2
created: "2026-11-26"
updated: "2026-11-26"
depends_on:
    - "0615"
spec_refs:
    - docs/design/daemon-restart.md#5. Passive resource ownership, restart manifest, leases, and crash policy
---

## Description
Split from 0615 (slice 3 of restart-safe daemon sessions, `docs/design/daemon-restart.md` §1 Phase 3 and §5 "Passive resource supervision"). 0615 reconciles supervised jobs when a session is reopened; this task covers sessions that stay closed after a daemon restart or crash, whose supervised jobs keep running with nothing watching them.

### Acceptance criteria
- Persistent daemon startup Phase 3 scans registered projects (including verified worktree implementation and integration session logs) for every durably started supervised job (`spec.json` matching `job_started`) that has neither a confirmed `exit.json` nor an explicit operator-cleared marker, without writing `SessionReopened` or starting model turns:
  - Non-handed-off subagent shells are cancelled via their control socket and joined (or flagged `execution_unknown`).
  - Surviving and `execution_unknown` shells are held in a manager-level passive supervisor table that blocks destructive workstream merge/discard/removal (`needs_attention`), protects job directories from GC across subsequent restarts until confirmed exited or explicitly cleared, and registers active background `ShellRun` overlaps under existing `workspacelease.Ownership` rules (preserving `EndShell`'s short-foreground exemption).
- A later `Reopen` of the session (manual, `ReopenForInput`, `ReopenForAnswer`) takes over its passive handles atomically through 0615's reconciliation rather than probing them again, and the table drops entries once their exit is confirmed.
- Workstream merge, integration and discard confirm, before any irreversible step, that no background job can still run in the worktree: before advancing the base ref, before recording `workstream_discarded`/`StatusDiscarded`, and before archiving session logs into the primary. A stop that cannot confirm this leaves the stream in flight as `needs_attention`, keeping its worktree logs authoritative. Workstream events must keep reaching the live session's log.
  - 0615 left a gap here. It has pre-checks (`workstreamExecutionUnsettled`) and fails closed after the stop: it keeps the tree and logs and moves the stream to `needs_attention`, from `Merged` or `Discarded`. A supervision loss that first appears during the stop is therefore found only after the base advanced, or after the discarded record was written.
  - A crash in that window leaves a terminal entry.
  - Reordering the stop before those steps broke the event-ordering tests in `workstream_merge_test.go` and `workstream_integrate_test.go`.

## Acceptance criteria

## Work log
