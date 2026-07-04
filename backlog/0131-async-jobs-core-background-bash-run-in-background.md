---
id: "0131"
title: Async jobs core + background bash (run_in_background, job_output, wait, kill_job)
status: done
priority: 2
created: "2026-07-04"
updated: "2026-07-04"
depends_on: []
spec_refs:
    - 7.3 Subagents
    - 'docs/design/async-jobs.md#3. Design: one job abstraction for shell commands and subagents'
---

## Description
Implement the unified job abstraction (docs/design/async-jobs.md) with its first job kind: background shell commands.

Scope:
- New `internal/jobs` package: session-scoped registry of Job{ID "job_<n>", Kind, Label, Status running|done|failed|killed, output buffer + read cursor, Result, done chan}. Kill-all on session end (cancel job contexts when the coordinator loop exits).
- `Bash` tool gains `run_in_background: true` → returns job_id immediately; process output captured into the job buffer.
- New coordinator tools (background Bash also available to the implementer): `job_output(job_id)` (non-blocking, output since last read + status; never consumes the final report), `wait(job_ids?, for: any|all, timeout_s?)` (blocks, returns final report(s): exit code + output tail), `kill_job(job_id)`.
- Exactly-once final-report delivery: consumed by `wait` OR by checkpoint injection (drain finished-job notifications at the loop's existing Steer.Checkpoint sites, appended as user-role messages), whichever fires first — one shared consumed flag, per the Claude Code deadlock lesson (§2 of the design doc).
- Events: `job_started`/`job_finished` (id, kind, label, status, output tail). Checkpoint-injected notifications recorded as user-actor events so reopen replays the identical history (same rule as steer corrections). Replay synthesizes "(job lost: daemon restarted)" for a started-but-never-finished job.
- Prompt guidance on Bash: background is for builds/tests/watchers; never poll — reports arrive automatically or via `wait`.
- TUI: minimal live job status line (id, label, running/exit).

Acceptance criteria:
- A backgrounded `sleep 2 && echo done` returns a job_id immediately; `wait` on it returns exit 0 + output; a finished job the model never waited on gets its report injected before the next turn (visible in the event log as a recorded user-actor event).
- `job_output` mid-run returns partial output and running status; repeated calls return only new output.
- `kill_job` terminates the process; status "killed".
- Session end leaves no orphan processes.
- Reopen of a session with a mid-flight job replays a valid history (synthesized lost-job note).
- Unit tests for the registry (concurrent finish vs wait, exactly-once consumption) and an engine-level test for checkpoint injection.

## Acceptance criteria

## Plan

Implement the unified async-job core with background bash as the first job kind, per docs/design/async-jobs.md.

1. **`internal/jobs` package** (new): session-scoped registry.
   - `Job{ID "job_<n>", Kind ("bash"), Label, Status running|done|failed|killed, output buffer + per-reader cursor, Result, done chan, consumed flag}`.
   - Registry: monotonic ids, Start/Finish/Kill, `Wait(ids, anyAll, timeout)` returning final reports, `Output(id)` non-blocking incremental read (never consumes), `DrainFinished()` for checkpoint injection — exactly-once consumption of final reports shared between Wait and DrainFinished (one consumed flag under the registry mutex; this is the Claude Code deadlock lesson).
   - KillAll on session end: job contexts derive from a registry-owned context cancelled when the coordinator loop exits; no orphan processes.

2. **Bash tool**: add `run_in_background: true` param (worker + coordinator Bash). Backgrounded command starts the process with output tee'd into the job buffer and returns `job_id` immediately.

3. **New tools**: `job_output(job_id)`, `wait(job_ids?, for: any|all, timeout_s?)`, `kill_job(job_id)` — registered for coordinator and implementer registries.

4. **Checkpoint injection**: at the loop's existing Steer.Checkpoint sites (between turns / after each tool result), drain finished, unconsumed jobs and append a user-role notification message ("[job job_3 done] go test ./... — exit 0; tail: …"). Record it as a user-actor event (same rule as steer corrections) so reopen replays the identical history. Wire via the session's Steer implementation or a sibling hook owned by the session.

5. **Events + replay**: emit `job_started`/`job_finished` (id, kind, label, status, output tail). ReplayHistory: a `job_started` with no matching `job_finished` on reopen synthesizes a "(job lost: daemon restarted)" state so histories stay valid; injected notifications replay from their recorded user events.

6. **Prompt guidance**: Bash description notes background is for builds/tests/watchers; never poll — reports arrive automatically at checkpoints or via `wait`.

7. **TUI**: minimal rendering of job_started/job_finished (id, label, status/exit).

8. **Tests**: registry unit tests (concurrent finish vs wait; exactly-once consumption; incremental output cursor; kill), engine-level test for checkpoint injection ordering/alternation, replay test for lost-job synthesis, tool-level test that backgrounded bash returns immediately and wait returns exit+output.

### Starting points
- docs/design/async-jobs.md — the agreed design (read first)
- internal/engine/loop.go — steerCheckpoint (injection point, appends user messages between turns/after tool results); Run's tool dispatch
- internal/session/steer_test.go + internal/session/session.go — existing Steer/Checkpoint implementation to mirror for notifications
- internal/tools/worker.go — Bash tool implementation; tools.Control/ControlOf pattern in internal/tools/tools.go
- internal/orchestrator/orchestrator.go — CoordinatorTools registration, Deps (where the registry handle lives)
- internal/event/event.go — event types (add JobStarted/JobFinished beside SubagentSpawned), Emitter.EmitAs for user-actor recording
- internal/engine/replay.go — ReplayHistory actor filtering / synthesized-message precedent (truncationNudge)

## Work log
- 2026-07-04 plan: Implement the unified async-job core with background bash as the first job kind, per docs/design/async-jobs.md.  1. **`internal/jobs` package** (new): session-scoped registry.    - `Job{ID "job_<n>", 
…[truncated]
- 2026-07-04 context hints: 7 recorded with plan
- 2026-07-04 context hints: docs/design/async-jobs.md — the agreed design (read first); internal/engine/loop.go:87-100,430,624 — steerCheckpoint call sites (injection points); internal/session/session.go:293-350 Checkpoint (
…[truncated]
- 2026-07-04 implementer report: Implemented the unified async-job core with background bash as the first job kind, per docs/design/async-jobs.md.  ## New package: internal/jobs `jobs.Registry` (session-scoped) + `jobs.Job`: - ids "j
…[truncated]
- 2026-07-04 review tier: high-powered — reviewers: Claude
- 2026-07-04 review (Claude): accept — Task 0131 is satisfied. The internal/jobs registry, background bash (run_in_background), job_output/wait/kill_job tools, exactly-once report delivery (shared consumed flag, job_output never consumes),
…[truncated]
- 2026-07-04 revision: Addressed the review finding: made the background-bash guidance actor-accurate.  Changes (internal/tools/worker.go): - Added `bgAutoDelivered(ws)` helper: true only when `ws.Emitter != nil && ws.Emitt
…[truncated]
- 2026-07-04 review (Claude): accept — Task 0131 is fully satisfied. The async-jobs core (internal/jobs registry with exactly-once report delivery, background bash, job_output/wait/kill_job, checkpoint injection as recorded user-actor even
…[truncated]
- 2026-07-04 decision: accept — commit: async jobs core + background bash: registry, run_in_background, job_output/wait/kill_job, checkpoint injection, replay (task 0131)  Adds internal/jobs (session-scoped registry, exactly-once final-repo
…[truncated]
