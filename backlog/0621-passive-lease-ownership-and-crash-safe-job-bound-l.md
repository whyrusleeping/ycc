---
id: "0621"
title: Passive lease ownership and crash-safe job-bound leases (leases.json)
status: todo
priority: 2
created: "2026-11-26"
updated: "2026-11-26"
depends_on:
    - "0619"
spec_refs:
    - docs/design/daemon-restart.md#5. Passive resource ownership, restart manifest, leases, and crash policy
    - spec.md#7.3 Session coordination and leases
---

## Description
Split from 0616 (Slice 4 of restart-safe daemon sessions, `docs/design/daemon-restart.md` §5) on 2026-11-26 by the work-loop session that took 0616; this is the authoring agent's split, not a user decision. 0616 was three separately verifiable outcomes (persisted `wait`, passive leases, restart manifest); the 0615/0619 slices each took seven review rounds, so the lease layer gets its own task and review. It delivers the parent's "`leases.go` decouples lease entries from requiring a live `*Session`" criterion unchanged.

Status: todo. Depends on 0619 (startup passive supervision of closed sessions' jobs), which is done.

### Acceptance criteria
- `leases.go` decouples lease entries from requiring a live `*Session`: an entry binds `(primary, session_id, job_id, expires_at)` plus an optional live `*Session` or passive supervisor handle.
- Job-bound leases persist in `<state-dir>/leases.json` (`0600`, written on acquire and release) across both planned restarts and unplanned crashes, gated on execution completion (`Done` + `WaitExecution`, never `Status() == Running`) and fail-closed on `execution_unknown` (holding until `expires_at` or explicit operator release rather than releasing as `job_exited`).
- Passive leases held by closed sessions (including `ask_user`, idle planned-restart holders, and crash-surviving job-bound leases) emit at-most-once `lease_released` under the per-session serialization lock on expiry/exit, and transfer ownership cleanly when the session reopens.
- Unbound leases are not restored after an unplanned crash (restoring them after a planned restart is the restart-manifest task's `unbound_leases`).
- `spec.md` §7.3 and `docs/design/async-jobs.md` describe the implemented lease behavior.

## Acceptance criteria

## Work log
