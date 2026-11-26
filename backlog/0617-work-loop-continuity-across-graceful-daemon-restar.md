---
id: "0617"
title: Work-loop continuity across graceful daemon restart
status: done
priority: 2
created: "2026-11-26"
updated: "2026-11-26"
depends_on:
    - "0613"
    - "0616"
    - "0622"
spec_refs:
    - docs/design/daemon-restart.md#6. Work-loop continuity across graceful restart (Slice 5)
    - spec.md#9.1 Unattended work loop
---

## Description

Slice 5 of restart-safe daemon sessions (`docs/design/daemon-restart.md` §6), building on 0613 (graceful daemon shutdown and phased startup) and 0616 (persisted wait, wake-eligible idle continuation), whose restart-manifest and lease parts were split into 0621 (passive lease ownership) and 0622 (restart manifest recovery). Extends planned graceful daemon restarts to suspend and seamlessly resume active/waiting unattended work loops with both working and waiting slots, while keeping crashed or unmatched loops visibly interrupted.

### Acceptance criteria
- Under `ShutdownForRestart`, active and waiting work loops stop launching new sessions, settle any session that ended right as shutdown began, and release both foreground working slots (including slots blocked in `wait`) and detached waiting slots without calling `reclaimIfCurrent`, preventing replacement work in the same slot or on the same focused task.
- Graceful restart persists a complete generation-bound scheduler checkpoint (`state: "suspended_restart"`, stamped with `restart_id`) alongside `.ycc/workloop.json`, retaining `LoopID`, `Parallel`, the original backlog `Baseline` (never reseeded from the current backlog on resume), `Sessions`, per-lane backoff/stall counters, provider-outage pacing state, every working and waiting slot's `session_id`, `focus`, `attempt`, progress `before`-hashes, slot kind, resource envelope, and accumulated usage/cost, without emitting a final interrupted outcome or digest push.
- On persistent daemon startup Phase 3, a loop resumes only when both `restart-state.json` and the project's checkpoint carry the same matching `restart_id`. `restoreWorkLoopLocked` converts any stale or unmatched `"suspended_restart"` checkpoint (as well as crashed `running`/`waiting`/`stopping` snapshots) into visibly interrupted state and never silently resumes a crashed loop.
- Re-adopted loop sessions reopen with their original `OriginWorkLoop` unattended configuration (`unattended = true`, `ShellWaitCap`, loop budget enforcement, task holds, and slot wake gates) installed before Phase 4 releases execution, accounting for every working, waiting, and just-ended slot exactly once.
- `spec.md` §9.1 is updated to distinguish planned graceful restarts (suspended and resumed via generation-bound checkpoint) from unplanned crashes (still restored as interrupted and never silently resumed).

## Outcome

A persistent daemon now suspends active, waiting and stopping work loops on graceful shutdown instead of ending them. They start nothing more, their followers let go of their sessions without reclaiming them, and no job wake or finish happens during the drain unless the finish was decided before shutdown. After the sessions are suspended, workloop.json gets a suspended_restart checkpoint carrying the restart_id: baseline, sessions with commits and verdicts, pacing, stop state, and working and waiting slots with focus, before-hashes and last_seq. Sessions that ended are settled from their closed logs. The manifest lists the loop under suspended_loops. RecoverRestart resumes a loop only when its checkpoint carries the manifest's restart_id, consuming the checkpoint first. It reopens each slot session unattended, with the restart marker, wake gate and slot holds set before release. Unmatched, crashed or unconsumable checkpoints restore as interrupted. Spec §3.1 and §9.1 and the design doc are updated. Covered by unit tests; no real restart was run.

Commit: session: suspend work loops across a graceful restart and resume them from a restart_id-bound checkpoint (0617)
