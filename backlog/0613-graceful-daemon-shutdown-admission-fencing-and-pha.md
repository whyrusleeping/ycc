---
id: "0613"
title: Graceful daemon shutdown, admission fencing, and phased startup
status: done
priority: 2
created: "2026-11-26"
updated: "2026-11-26"
depends_on: []
spec_refs:
    - docs/design/daemon-restart.md#1. Graceful shutdown, admission fencing, and startup ordering
    - spec.md#3.1 Daemon lifecycle and projects
---

## Description

Slice 1 of restart-safe daemon sessions (`docs/design/daemon-restart.md` §1). Adds graceful `SIGINT`/`SIGTERM` shutdown with admission fencing and bounded drain to `ycc daemon`, separates restart suspension from ordinary session cancellation so in-flight `wait` and `ask_user` calls stay cleanly unresolved, and splits persistent daemon startup into ordered phases before any recovery side effects or background scheduling run.

### Acceptance criteria
- Persistent `ycc daemon` acquires an exclusive state-directory lock (`daemon.lock`) and binds its listening socket (holding RPC admission closed behind a readiness gate) before running startup recovery, mutating state, or starting background workers; a lock or bind failure exits cleanly with zero state mutation.
- On the first `SIGINT`/`SIGTERM`, `Serve()` stops admitting new mutating RPCs (`Unavailable`), fences already-admitted mutations, stops manager-owned background workers (watches, grooms, git sync, integration queue), and waits up to `--shutdown-drain` (default 30s; `0` skips drain) for active model turns, foreground tools, and subagents to reach a clean boundary. Sessions already idle, waiting on jobs, or parked on `ask_user` do not delay drain. A second signal ends the drain wait immediately.
- Restart suspension exits in-flight `wait` and `ask_user` calls via a restart-specific suspend path without claiming partial job reports and without `engine.Loop` emitting a premature `tool_result` or mutating history, leaving the trailing `tool_call` unresolved in `events.jsonl`.
- Under restart teardown, the session run owner closes job registration before snapshotting runners, detaches (rather than killing) supervised jobs when present, cancels and joins in-process subagents and unsupervised jobs without writing `session_stopped`, joins all log writers before closing `events.jsonl`, and does not race with a deferred `ReclaimAll`.
- Ordinary `StopSession`, `kill_job`, idle reaping, fail-stop log recovery, and one-shot `InProcess` daemon shutdown preserve their existing kill-and-join cleanup behavior.
- Persistent daemon startup runs in four explicit phases: exclusive ownership + bound listener -> static worktree path/stale-tree validation -> prepared recovery state (with per-session serialized preparation and run goroutines held) -> release execution, workstream readiness derivation (excluding sessions with `awaiting_jobs` or live recovered runners), watches, reaper, health monitor, and RPC admission.
- `spec.md` §3.1 is updated to document graceful shutdown, `--shutdown-drain`, and phased startup.

## Outcome

Persistent ycc daemon now takes <state>/daemon.lock and binds before any state write (lock/bind failure mutates nothing), gates RPCs Unavailable until startup phases finish (validate worktrees -> held execution -> release, readiness derivation skipping live/awaiting_jobs sessions, watches, reaper, health). SIGINT/SIGTERM: fence all new RPCs, cancel workers (joined after suspension), drain up to --shutdown-drain (default 30s, one deadline from the signal; 2nd signal ends it, 3rd exits), then ShutdownForRestart suspends every session: restart cause makes wait/ask_user (and subagent waits) exit with no claim and no tool_result, run owner joined, jobs killed, watcher joined, log closed last, no session_stopped. Ordinary Stop/reap/InProcess unchanged. spec.md §3.1 and docs/cli.md updated; tests in session/daemon/server/jobs shutdown_test files.

Commit: daemon: graceful SIGINT/SIGTERM shutdown with admission fencing, restart suspension, and phased startup (0613; files proposed flaky-test task 0618)
