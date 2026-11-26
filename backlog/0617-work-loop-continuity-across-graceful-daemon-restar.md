---
id: "0617"
title: Work-loop continuity across graceful daemon restart
status: todo
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

## Acceptance criteria

## Work log
