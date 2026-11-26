---
id: "0616"
title: Persisted wait, wake-eligible idle continuation, restart manifest, and passive leases
status: todo
priority: 2
created: "2026-11-26"
updated: "2026-11-26"
depends_on:
    - "0613"
    - "0615"
spec_refs:
    - docs/design/daemon-restart.md#4. Persisted wait and wake-eligible idle continuation
    - docs/design/daemon-restart.md#5. Passive resource ownership, restart manifest, leases, and crash policy
    - spec.md#7.3 Session coordination and leases
---

## Description
Slice 4 of restart-safe daemon sessions (`docs/design/daemon-restart.md` §4–§5), building on 0613 (graceful daemon shutdown and phased startup) and 0615 (passive job supervision and idempotent reopen reconciliation). Persists in-flight `wait` calls without adding a new agent tool, restores wake-eligible idle sessions, adds generation-bound idempotent restart-manifest recovery with passive lease ownership and crash-safe job-bound leases, and enables supervised bash launches by default in persistent `Serve()`. Excludes `OriginWorkLoop` sessions from automatic restart recovery until Slice 5 installs work-loop scheduler checkpoints.

### Acceptance criteria
- `wait` records a durable `wait_started` event carrying `call_id`, resolved `job_ids` (already expanded when omitted), `for`, effective RFC3339 `deadline` (incorporating the 600s default and `ShellWaitCap`), and `shell_only`.
- `resume_wait.go` restores a trailing unresolved coordinator `tool_call(wait)` + `wait_started`:
  - Recovers all undelivered `user_input(queued: true)` corrections pending for that active run in sequence order (including messages queued between `tool_call(wait)` and `wait_started`), seeds them into the correction queue so `InputWake` fires immediately, and delivers them after `tool_result` in live order.
  - Preserves already-recorded `job_claimed` reports without re-emitting `job_claimed`, collects remaining targets, and handles `now >= deadline` via an explicit non-blocking collection (never passing `0` to `waitTool` or `Registry.Wait`).
  - Generalizes `placeholderCallID` for raw-vs-canonical IDs and batch ordering, pre-arms active-turn input routing (`SendInputMessage` -> correction path -> `InputWake`) before publishing the session to the manager map, emits `SessionReopened(wait_restored: true)`, writes `tool_result(name: "wait", restored: true)`, swaps it into history, and continues the coordinator turn.
- Sessions whose last durable lifecycle boundary is `session_idle(awaiting_jobs: true)` (with no intervening human boundary such as pause, refusal, error/model-switch gate, blocked report, fork-first-input, or pending question) and where `PendingContinuation("coordinator")` is true are prepared with `setIdle(true)` before publication and enter `awaitNextRun(true)`; ordinary manual reopens retain `setIdle(false)` and `awaitNextRun(false)`.
- `leases.go` decouples lease entries from requiring a live `*Session`:
  - Job-bound leases persist in `<state-dir>/leases.json` (`0600`) across both planned restarts and unplanned crashes, gated on execution completion (`Done` + `WaitExecution`, never `Status() == Running`) and fail-closed on `execution_unknown` (holding until `expires_at` or explicit operator release rather than releasing as `job_exited`).
  - Passive leases held by closed sessions (including `ask_user`, idle planned-restart holders, and crash-surviving job-bound leases) emit at-most-once `lease_released` under the per-session serialization lock on expiry/exit and transfer ownership cleanly when the session reopens.
- Graceful shutdown writes generation-bound `<state-dir>/restart-state.json` (`0600`, `restart_id`, per-session `last_seq`, `waiting_sessions` for any unresolved `wait_started` or `StatusWithJobContinuation() == (StatusIdle, true)`, non-loop `interrupted_sessions`, and unexpired `unbound_leases`). Startup consumes it idempotently across repeated crashes using `(restart_id, last_seq)` authorization, injects durable synthetic restart context (never apparent human `user_input`) only when an interrupted session does not already owe a model response, keeps `ask_user` sessions closed on disk until answered, and never auto-starts model turns after an unplanned crash without a manifest.
- Supervised bash launches are enabled by default for persistent `ycc daemon` (`Options.Persist == true`), while `OriginWorkLoop` sessions remain excluded from automatic restart reopening (`spec.md` §9.1 preserved until Slice 5).
- `spec.md` §3.1, §5.1, §7.3, §11 and `docs/design/async-jobs.md` are updated to match the implemented behavior.

## Acceptance criteria

## Work log
