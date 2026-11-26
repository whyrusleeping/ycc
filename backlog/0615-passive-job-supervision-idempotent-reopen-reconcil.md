---
id: "0615"
title: Passive job supervision, idempotent reopen reconciliation, and projection parity
status: done
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

Split on 2026-11-26: startup passive supervision of closed sessions' surviving jobs (the startup Phase 3 scan, the manager-level passive supervisor table, workstream guards and GC protection for closed sessions) moved to 0619. This task keeps the reopen side: when a session is reopened its supervised jobs are reconciled, survivors re-attached (with their shell overlap registered and non-handed-off subagent shells cancelled), and graceful restart leaves coordinator-owned supervised jobs running instead of killing them.

### Acceptance criteria
- `Reopen` is serialized per session ID so only one winning caller transfers passive handles, attaches watchers, or appends events. Reopen reconciles five axes per job in stable ID order: owner/handoff, terminal evidence, execution state (including still-unwinding killed jobs), retained disk evidence (re-hydrated across repeated reopens within the owning actor's `ArtifactStore` scope and 16 MiB / 64-record limits), and delivery state.
- `jobs.Registry` supports restoring surviving jobs as live tracked `Running` jobs (with open `done` channels and execution tracking). Automatic post-restart `claimShellChanges` is skipped conservatively for re-attached and downtime-harvested shells, noting that in the recovered report while preserving live attribution when no restart occurred.
- Downtime finishes and reconciled losses emit `job_finished` before `SessionReopened`, and `SessionReopened` records `surviving_jobs` and `awaiting_jobs` (with empty/omitted fields preserving legacy behavior). `ReplayHistory` excludes both durably finalized jobs and positively reconciled live `surviving_jobs` from its legacy tail-note fallback while keeping legacy logs compatible.
- `execution_unknown` delivers at most one distinct supervision-lost warning (stating that supervision ended and the child may still be running) without asserting `ExecutionComplete` or releasing worktree/overlap guards. If `wait` or `job_notified` already claimed a terminal report (such as a killed job) before supervision was lost, that report and its claim are preserved and supervision loss does not emit a second completion notification.
- `internal/sessionview/activity.go`, `internal/sessionview/store.go`, `internal/server/activity.go` (both `OutputAvailable` and `GetJobOutput`, removing the blanket `info.Restored` rejection), and web/iOS projections agree on surviving jobs, downtime finishes, `awaiting_jobs`, and disk-backed output availability without firing spurious idle notifications on restart reopen.
- `docs/design/async-jobs.md` and `spec.md` §5.1 are updated for disk-backed job survival and `SessionReopened` metadata.

## Outcome

Reopen reconciles each supervised job across five axes. Survivors are re-attached as live tracked jobs listed in session_reopened.surviving_jobs, and their outcome is held until that marker. Downtime finishes and losses get job_finished(recovered) before the marker. Lost supervision fails closed but is still followed. Subagent shells are killed and joined, with an atomic owner_gone delivery. Captures are re-hydrated within the store bounds. Exit records are harvested only after a durable terminal record. A graceful restart detaches coordinator shells. Late-wired stops are honoured and restored pending reports wake their owner. A reopen into awaiting_jobs idle wakes on completion. Workstream merge/discard/integration defer or fail closed on unsettled jobs. sessionview, server, web and iOS projections and the docs are updated. Startup supervision of closed sessions, plus the stricter settle-before-irreversible ordering, moved to 0619. Verified with go test ./..., race runs, web vitest, and YccKit/xcodebuild on the Mac; opus and sol accepted.

Commit: jobs: reconcile supervised background jobs on reopen, keep them running across graceful restart, and project survivors (0615; splits startup passive supervision to 0619)
