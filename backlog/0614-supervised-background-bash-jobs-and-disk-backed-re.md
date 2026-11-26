---
id: "0614"
title: Supervised background bash jobs and disk-backed retained evidence
status: todo
priority: 2
created: "2026-11-26"
updated: "2026-11-26"
depends_on:
    - "0613"
spec_refs:
    - docs/design/daemon-restart.md#2. Supervised background bash jobs and retained evidence
    - docs/design/async-jobs.md
---

## Description
Slice 2 of restart-safe daemon sessions (`docs/design/daemon-restart.md` §2), building on 0613 (graceful daemon shutdown and phased startup). Introduces an out-of-process detached bash job supervisor (`internal/jobshim`) and disk-backed output/artifact storage so background shell commands can survive daemon restarts and crashes without losing repeatable evidence. Detached launches remain opt-in/gated off in production `Serve()` until Slice 4 adds crash-safe job-bound leases alongside Slice 3 reconciliation.

### Acceptance criteria
- `ycc` dispatches the internal supervisor helper before CLI parsing (alongside `sandbox.MaybeHelper()`), version-checks its protocol at handshake, and launches supervised background bash jobs in their own session (`Setsid`) with child commands in their own process group (`Setpgid`), enforcing optional `timeout_s` and the 10-second `WaitDelay`. One-shot `InProcess` daemons continue spawning child processes directly.
- Launch uses a two-step handshake: the supervisor acquires `shim.lock` and binds its control socket before signalling readiness; the daemon fsyncs durable `job_started` before signalling the supervisor to exec the child, and aborts cleanly without launching the child if `job_started` emission fails. While attached, the daemon reaps completed supervisor children so zombies do not accumulate.
- Control sockets live in a short, daemon-owned, owner-only (`0700`) runtime directory outside the workspace (avoiding the 104/108-byte `sun_path` limit) with daemon-derived paths and peer-UID checks on connect. `kill_job` and `StopSession` send `kill` only over that control channel; the daemon never signals numeric PIDs/PGIDs read from workspace files and never kills the supervisor ahead of its exit write.
- A dead supervisor (lock released with no exit record) or vanished control socket while `shim.lock` remains held enters fail-closed `execution_unknown` state without asserting `ExecutionComplete`; `kill_job`, `StopSession`, `KillAll`, and subagent cleanup report the unjoined-execution state honestly rather than claiming a clean execution join.
- Combined output is written under owner-only `<session-dir>/jobs/<job-id>/` (`0700` dir, `0600` files, no environment variables persisted) into rotating 256 KiB tail segments (`seg-<abs-offset>.log`, at most 2 retained) and a 4 MiB head capture (`artifact.bin` with full-stream byte/line counts, SHA-256, and truncation loss metadata), plus an atomic `exit.json` on completion. Readers handle concurrent segment rotation without losing absolute offsets or eviction gaps.
- Each session bounds total retained disk bytes across finished, notified job directories by evicting the oldest finished evidence first without resurrecting evicted data; session GC never deletes a session directory while any job holds a live `shim.lock`, an unharvested exit record, or an uncleared `execution_unknown` state.
- Detached launches stay gated off in production `Serve()` in this slice and are exercised through opt-in/unit/integration tests.

## Acceptance criteria

## Work log
