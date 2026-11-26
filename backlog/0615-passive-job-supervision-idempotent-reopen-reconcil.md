---
id: "0615"
title: Passive job supervision, idempotent reopen reconciliation, and projection parity
status: todo
priority: 2
created: "2026-11-26"
updated: "2026-11-26"
depends_on:
    - "0614"
spec_refs:
    - docs/design/daemon-restart.md#3. Idempotent job reconciliation, projections, and replay
    - docs/design/async-jobs.md
    - spec.md#5.1 Storage and durability
---

## Description
Slice 3 of restart-safe daemon sessions (`docs/design/daemon-restart.md` §2–§3), building on 0614 (supervised background bash jobs and disk-backed evidence). Implements startup passive supervision, serialized five-axis job reconciliation on reopen, conservative post-restart shell attribution with overlap protection, and client/projection parity. Supervised launches remain opt-in until Slice 4 adds crash-safe job-bound leases.

### Acceptance criteria
- Persistent daemon startup Phase 3 scans registered projects (including verified worktree implementation and integration session logs) for every durably started supervised job (`spec.json` matching `job_started`) that has neither a confirmed `exit.json` nor an explicit operator-cleared marker, without writing `SessionReopened` or starting model turns:
  - Non-handed-off subagent shells are cancelled via their control socket and joined (or flagged `execution_unknown`).
  - Surviving and `execution_unknown` shells are held in a manager-level passive supervisor table that blocks destructive workstream merge/discard/removal (`needs_attention`), protects job directories from GC across subsequent restarts until confirmed exited or explicitly cleared, and registers active background `ShellRun` overlaps under existing `workspacelease.Ownership` rules (preserving `EndShell`'s short-foreground exemption).
- `Reopen` is serialized per session ID so only one winning caller transfers passive handles, attaches watchers, or appends events. Reopen reconciles five axes per job in stable ID order: owner/handoff, terminal evidence, execution state (including still-unwinding killed jobs), retained disk evidence (re-hydrated across repeated reopens within the owning actor's `ArtifactStore` scope and 16 MiB / 64-record limits), and delivery state.
- `jobs.Registry` supports restoring surviving jobs as live tracked `Running` jobs (with open `done` channels and execution tracking). Automatic post-restart `claimShellChanges` is skipped conservatively for re-attached and downtime-harvested shells, noting that in the recovered report while preserving live attribution when no restart occurred.
- Downtime finishes and reconciled losses emit `job_finished` before `SessionReopened`, and `SessionReopened` records `surviving_jobs` and `awaiting_jobs` (with empty/omitted fields preserving legacy behavior). `ReplayHistory` excludes both durably finalized jobs and positively reconciled live `surviving_jobs` from its legacy tail-note fallback while keeping legacy logs compatible.
- `execution_unknown` delivers at most one distinct supervision-lost warning (stating that supervision ended and the child may still be running) without asserting `ExecutionComplete` or releasing worktree/overlap guards. If `wait` or `job_notified` already claimed a terminal report (such as a killed job) before supervision was lost, that report and its claim are preserved and supervision loss does not emit a second completion notification.
- `internal/sessionview/activity.go`, `internal/sessionview/store.go`, `internal/server/activity.go` (both `OutputAvailable` and `GetJobOutput`, removing the blanket `info.Restored` rejection), and web/iOS projections agree on surviving jobs, downtime finishes, `awaiting_jobs`, and disk-backed output availability without firing spurious idle notifications on restart reopen.
- `docs/design/async-jobs.md` and `spec.md` §5.1 are updated for disk-backed job survival and `SessionReopened` metadata.

## Acceptance criteria

## Work log
