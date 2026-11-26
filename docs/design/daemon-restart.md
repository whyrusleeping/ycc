# Design: Restart-safe sessions and background jobs

> Status: accepted; implementation phased through the backlog.

## Context

Restarting `ycc daemon` to deploy new features or fixes currently disrupts every live session
across all registered projects:

- Background shell jobs (`startBackgroundBash`) pipe child stdout/stderr into an in-memory buffer
  and wait on `cmd.Wait()` in a daemon goroutine.
- `Serve()` does not handle `SIGINT` or `SIGTERM`. Stopping a persistent daemon exits abruptly
  without draining in-flight turns, fencing writes, or running `ReclaimAll()`; persisted active
  work loops are later restored as interrupted on startup rather than gracefully suspended.
- On reopen, `engine.RestoreJobs` and `engine.ReplayHistory` treat every unmatched `job_started`
  as `lost` (`"job lost: daemon restarted"`), and waiting sessions remain stopped on disk until
  manually reopened and nudged.

Watches (`docs/design/watches.md`) survive daemon restarts under `.ycc/watches/`, but watches run
independent recurring checks in fresh sessions. Long-running work that continues inside the same
conversation uses background shell jobs and `wait`, and needs to survive planned restarts cleanly
without changing the agent tool surface.

## Goals and non-goals

### Goals

1. Background shell jobs keep executing across `ycc daemon` restarts (and unplanned daemon crashes),
   with repeatable tail reads (`job_output`) and bounded head captures (`tool_output`) preserved.
2. Sessions waiting on background shell jobs — whether idle-waiting for automatic wake or blocked
   inside `wait` — resume automatically after a planned restart without adding a new agent tool.
3. Graceful daemon shutdown (`SIGINT`/`SIGTERM`) fences new work, drains active model turns
   briefly, preserves unexpired leases for surviving work, auto-reopens interrupted mid-turn
   sessions with synthetic context, and (in a dedicated final slice updating `spec.md` §9.1)
   suspends and resumes active work loops with their working and waiting slots.
4. `StopSession`, `kill_job`, idle reaping, fail-stop log recovery, and one-shot `InProcess`
   daemons retain their existing cleanup contracts.

### Non-goals

- Preserving an in-flight provider HTTP/SSE model stream mid-response across a restart (handled by
  bounded drain plus auto-reopen).
- Detaching in-process subagent loops across restarts (subagents run model turns inside the daemon;
  bounded drain lets active turns reach a boundary, and unfinished ones recover on reopen).
- Running two active daemons against the same state directory or proxying RPCs between old and new
  daemons.
- Silently auto-continuing model turns or work loops after an unplanned crash without a graceful
  restart checkpoint.

## Decision

The design separates four distinct facts that recovery must never conflate: whether a **process**
is still executing, whether its **completion** has been recorded and delivered, whether **shared
resources** (leases, worktrees, shell overlap) still need protection, and whether its **session**
is authorized to run another turn.

### 1. Graceful shutdown, admission fencing, and startup ordering

#### Restart-specific exit from dispatch and the run owner

Canceling a session context is not a restart-safe detach: `Session.run` unconditionally defers
`killJobs()`, `Registry.Wait` collects and claims partial reports on cancellation, and
`engine.Loop` emits `tool_result` before checking post-dispatch cancellation. A naive cancel-based
detach would kill surviving processes and record premature results for in-flight `wait` or
`ask_user` calls.

Graceful restart therefore uses a dedicated restart-suspend path distinct from `Stop`, idle reap,
and fail-stop:

- **Admission fence:** On the first `SIGINT`/`SIGTERM`, `Serve()` stops admitting new mutating
  RPCs (returning `Unavailable`), fences already-admitted mutations, and stops manager-owned
  background workers (watch scheduler, memory/backlog grooms, git sync, integration queue).
- **Bounded drain:** Active coordinator/subagent LLM turns and foreground tools receive a bounded
  drain window (`--shutdown-drain`, default `30s`; `0` skips drain) to reach a clean boundary
  (idle, `wait`, or `ask_user`). Sessions already idle, waiting on supervised shell jobs, or
  parked on `ask_user` do not delay drain. A second signal ends the drain wait immediately.
- **Suspend without premature results:** When the drain window expires (or immediately for already
  blocked `wait` and `ask_user` calls), the restart signal exits `wait` and `ask_user` via a
  suspend sentinel without claiming partial reports, and `engine.Loop` exits without emitting a
  `tool_result` or mutating history. The trailing `tool_call` remains unresolved in `events.jsonl`
  so recovery can restore it verbatim.
- **Detach vs kill:** The session run owner closes job registration before snapshotting runners.
  Under restart detach, it disconnects watchers from supervised bash jobs (leaving them executing,
  without emitting `job_finished` or `session_stopped`), cancels and joins in-process subagents and
  unsupervised jobs, joins all log writers, and only then closes `events.jsonl`.
- **Single teardown owner:** `Serve()` coordinates `ShutdownForRestart` so a deferred `ReclaimAll`
  cannot race or undo detach. `StopSession`, idle reap, fail-stop, and `InProcess` shutdown keep
  their existing kill-and-join contract; `kill_job` keeps its immediate-killed-report contract
  without promising synchronous execution join.

#### Exclusive startup ownership and phased startup

Today `buildHandler` reconciles workstreams (which can mark idle worktrees ready and enqueue
auto-integration), starts watches, and starts the idle reaper before the listener binds, and
`Manager.Reopen` spawns `go s.run()` before returning. Running recovery in that order would let
a failed bind happen after recovery side effects, or let worktrees integrate and downtime
completions execute before leases and slot gates exist.

Persistent daemon startup therefore runs in four ordered phases:

1. **Acquire exclusive ownership:** Acquire an exclusive daemon lockfile (`<state-dir>/daemon.lock`)
   and bind the listening socket (holding RPC admission closed behind a readiness gate) *before*
   performing any recovery side effects, writing events, or starting turns. If locking or binding
   fails, exit cleanly with zero state mutation.
2. **Validate static state:** Load config, project registry, workstream registry, and run
   worktree path/stale-tree validation only (verifying paths under `worktreesRoot` and pruning
   missing worktrees without deriving readiness or enqueueing integrations).
3. **Prepare recovered and passive state (execution held):**
   - Scan for passive resource holders (surviving/unjoined job supervisors in registered projects
     and unexpired job-bound leases in `<state-dir>/leases.json`), cancelling non-handed-off
     subagent shells and installing worktree guards, shell-overlap registrations, and passive
     leases without starting model turns.
   - Serialize session preparation per session ID so concurrent callers cannot duplicate watchers
     or log appends. Prepare sessions authorized by `restart-state.json` (covering primary
     workspaces and both workstream implementation and integration logs via verified worktree
     lookup) without starting their run goroutines yet, transferring any passive supervisor and
     lease handles into the prepared sessions.
   - When work-loop continuity (Slice 5) is enabled, restore loop slot gates before releasing runs.
4. **Release execution and background services:** Release prepared sessions, run workstream
   readiness derivation (excluding sessions with `awaiting_jobs`, live recovered runners, or
   active/unknown passive worktree guards), start watches, idle reaper, and health monitor, and
   open RPC admission on the bound listener.

### 2. Supervised background bash jobs and retained evidence

Persistent daemons launch background shell jobs through a detached supervisor process (`ycc`
re-executed with an internal helper sentinel before CLI parsing, alongside `sandbox.MaybeHelper()`).
One-shot `InProcess` daemons keep direct child processes so teardown leaves no survivors. Detached
launches stay gated off in production `Serve()` until Slice 4 lands reconciliation, passive
supervision, and crash-safe job-bound leases together.

#### Launch handshake, process identity, and execution-unknown state

- **Two-step launch handshake:**
  1. The daemon creates an owner-only (`0700`) job directory under `<session-dir>/jobs/<job-id>/`
     (validating no untrusted symlinks along the path), writes a versioned `spec.json` (`0600`,
     omitting environment variables; scrubbed `cmd.Env` is passed over process exec), and spawns
     the supervisor with readiness and start-gate pipes.
  2. The supervisor calls `Setsid()`, acquires an advisory lock on `shim.lock`, binds its control
     socket, and signals readiness on the first pipe.
  3. The daemon emits durable `job_started` (fsynced) and then writes "go" on the start-gate pipe.
     If durable emission fails, the daemon closes the start-gate pipe and the supervisor exits
     without launching the child.
  4. On "go", the supervisor starts `sh -c <cmd>` in its own process group (`Setpgid: true`),
     enforces optional `timeout_s`, and applies the existing 10-second `WaitDelay` on termination.
  This ordering ensures a command never executes before `job_started` is durable, recovery never
  races lock acquisition, and recovery never re-launches a command.
- **Reaping while attached:** While the launching daemon stays up, it calls `Wait()` on the
  supervisor child in a goroutine so completed supervisors do not remain zombies. After daemon
  exit, the OS reparents surviving supervisors.
- **Process identity and control channel:** Workspace-resident files are never trusted as
  authority to signal a numeric PID/PGID or connect to an arbitrary socket path. Control sockets
  live in a short, daemon-owned, owner-only (`0700`) runtime directory outside the workspace
  (keeping paths well under the 104/108-byte Unix `sun_path` limit even for deep worktrees),
  with socket path derived deterministically by the daemon and peer-UID verified on connect.
  `kill_job` and `StopSession` send `kill` over that socket; only the supervisor itself signals
  its own live child PGID (`-cmd.Process.Pid`) while `cmd.Wait()` has not returned, waits up to
  `WaitDelay`, writes the exit record, and exits. The daemon never signals persisted numeric
  PIDs/PGIDs and never kills the supervisor ahead of its exit write.
- **Binary replacement compatibility:** The supervisor protocol is small and version-checked at
  handshake (`version: 1`) rather than assuming an executable path still points to identical bytes.
  If launching a supervisor fails (deleted binary or version mismatch), the job start reports an
  explicit failure.
- **Supervisor failure and fail-closed `execution_unknown` state:**
  A dead supervisor (lock released without an exit record) or a missing control socket while
  `shim.lock` remains held does **not** prove the child process stopped. In either case the job
  enters terminal status `lost` with `execution_unknown: true` (if writing the atomic exit record
  fails, e.g. disk full, the supervisor still sends its exit payload over any connected control
  socket before exiting; if detached, recovery treats a released lock with no exit record as
  `execution_unknown`). `ExecutionComplete` is **never** falsely asserted for `execution_unknown`;
  resource guards remain separate from model notification. While `execution_unknown` holds (across
  GC runs and subsequent daemon restarts until an exit record is confirmed or an operator clears
  it explicitly):
  - `kill_job` reports that supervision was lost and the child could not be confirmed stopped;
  - `StopSession`, `KillAll`, and subagent cleanup surface an explicit unjoined-execution error or
    warning rather than reporting a clean execution join;
  - job-bound leases are **never** released early as `job_exited` — they stay held until their
    original `expires_at` TTL expires (releasing as `expired`) or explicit operator release;
  - destructive worktree cleanup (workstream merge, discard, or removal) is blocked
    (`needs_attention`) and the job's disk directory is protected from GC until cleared explicitly;
  - the coordinator still receives one distinct supervision-lost warning (via the restored `wait`
    result or a single `job_notified` delivery stating that supervision ended and the child may
    still be running, without implying execution stopped), so the session does not hang
    indefinitely in `awaiting_jobs` while passive resource guards stay active.
- **Secret hygiene:** `spec.json`, logs, and exit records are owner-only (`0600`) under owner-only
  directories (`0700`), matching `events.jsonl`, because command text and captured output may
  contain secrets even though `cmd.Env` is scrubbed and never written to disk.

#### Retained output, artifacts, and disk bounds

- **Tail segments (`job_output`):** The supervisor writes combined stdout/stderr into rotating
  256 KiB segments named by absolute start offset (`seg-<offset>.log`), keeping at most two
  segments (256–512 KiB retained tail, >= `maxJobBuf`). Readers open segment files directly and
  retry once on `ENOENT` if rotation unlinked the oldest segment, preserving exact absolute
  offsets and eviction gaps across live reads and reopens.
- **Head capture (`tool_output`):** The supervisor captures up to `maxCapturedCommandBytes` (4 MiB)
  into `artifact.bin` while tracking total bytes, total lines, and SHA-256 over the full stream.
  Truncated captures always record their loss metadata and are never presented as complete.
- **Re-hydration and store bounds:** Disk-backed tail and artifact evidence are re-hydrated on any
  reopen while the job directory remains on disk (not only for downtime finishes), scoped to the
  owning actor's `ArtifactStore` (never exposing one actor's capture to another and preserving the
  existing 16 MiB / 64-record store limits).
- **Disk caps and GC safety:** Each session bounds retained disk bytes for *finished, notified*
  job directories by evicting the oldest finished artifacts/segments first, without resurrecting
  evicted data. Session retention / GC never deletes a session directory while any job directory
  inside it holds a live `shim.lock`, an unharvested exit record, or an uncleared
  `execution_unknown` state.

#### Shell write attribution and overlap protection across restart

Live shell attribution (`internal/tools/shellclaims.go`) relies on in-memory `ShellRun` overlap
windows, claim generations, scope lifetimes, and watched HEADs. Replaying a single pre-restart
dirty snapshot after a restart would race concurrent detached jobs across scopes and falsely
attribute edits made after a job exited during downtime. Conversely, skipping overlap tracking
for a surviving shell would let another session's new shell run in the same worktree without
seeing an overlapping `ShellRun` and falsely claim the surviving shell's writes.

Therefore:

- **Without a restart:** Live attached jobs run `snapshotShell` / `claimShellChanges` unchanged.
- **Across a restart:**
  1. During startup Phase 3 (before admitting any new shells), every surviving or
     `execution_unknown` shell — whether owned by a reopened session or a passive resource holder
     — is registered as an active background `ShellRun` in `workspacelease.Ownership` for its
     worktree, participating in the existing overlap rules (including the short-foreground-command
     exemption in `workspacelease.EndShell`).
  2. Automatic post-restart `claimShellChanges` for the re-attached or downtime-harvested shell
     itself is skipped conservatively: its writes remain unclaimed dirty paths (surfaced in the
     change manifest under `docs/design/async-jobs.md`), and its recovered report notes that
     automatic write attribution was skipped across restart.

### 3. Idempotent job reconciliation, projections, and replay

#### Five-axis reconciliation on reopen

Reopen does not key off "unmatched `job_started`". Serialized per-session reopen reconstructs five
separate facts per job from the durable log in stable ID order:

1. **Owner and handoff:** Initial actor from `job_started`, updated by `job_handed_off`. A
   subagent's non-handed-off shell is never adopted into the coordinator: its supervisor is
   cancelled via `ctl.sock` and joined (or flagged `execution_unknown` if control was lost).
2. **Terminal evidence:** Durable `job_finished`, `job_claimed`, or `job_notified`.
3. **Execution state:** Live supervisor (`ctl.sock` / `shim.lock`), on-disk atomic exit record, or
   `execution_unknown` — checked even when a `killed` finish was already emitted before shutdown
   so execution completion and leases remain tracked.
4. **Retained evidence:** Available disk-backed segments and `artifact.bin` (for both live and
   previously finished jobs).
5. **Delivery state:** Whether `job_claimed` or `job_notified` already delivered or suppressed the
   single automatic notification.

`jobs.Registry` gains a tracked-live restoration path so surviving jobs restore as live `Running`
jobs with open `done` channels and execution tracking (rather than `NewRestored` unconditionally
converting `Running` to `Lost`).

#### Event ordering, idempotency, and durable `lost` delivery

- Only the winning serialized `Reopen` transfers passive handles, attaches watchers, or appends
  events.
- **Downtime finishes precede `SessionReopened`:** Any job whose terminal outcome is harvested
  from `exit.json` or determined `lost` at reopen has its `job_finished` emitted *before*
  `SessionReopened` (guarded by durable terminal evidence so repeated crashes never duplicate it).
  `SessionReopened` then lists only jobs still live (`surviving_jobs`) after reconciliation.
- **Delivering durable `lost` to the model:** Because `job_finished(status: "lost")` is terminal
  evidence rather than a model message in `ReplayHistory`:
  - if a lost job is a target of a restored `wait`, `awaitRestoredWait` includes its `lost` report
    in the `wait` `tool_result` and emits `job_claimed(status: "lost", reason: "wait")`;
  - otherwise, the normal single-delivery path delivers the unnotified `job_finished(status: "lost")`
    once via `job_notified` (at the next checkpoint or idle wake). If a terminal report (such as a
    killed job) was already claimed by `wait` or notified before supervision was lost, that report
    and its claim are preserved and supervision loss never emits a second completion notification.
    For `execution_unknown`, this delivery is authorized separately from `ExecutionComplete` and
    states explicitly that supervision ended while the child may still be executing, without
    releasing leases or worktree guards;
  - `ReplayHistory` receives the reconciled `surviving_jobs` set and suppresses its legacy
    tail-note fallback for both (a) jobs with durable terminal evidence (`job_finished`,
    `job_claimed`, or `job_notified`) and (b) positively reconciled live `surviving_jobs` (even
    when replay runs during Phase 3 preparation before `SessionReopened` is appended). Legacy logs
    without reconciliation retain the tail-note fallback.

#### Projections and client parity

`SessionReopened` carries explicit recovery metadata (with empty/omitted fields preserving legacy
reopen behavior):

- `restart_id`: shutdown generation ID when reopened by planned restart recovery;
- `surviving_jobs`: job IDs still running after downtime reconciliation;
- `wait_restored`: true when an in-flight `wait` call is restored;
- `awaiting_jobs`: true when the session reopens directly into wake-eligible idle state.

Downstream projections and RPCs update together:

- `internal/sessionview/activity.go` receives downtime `job_finished` events while those nodes are
  still `running`, and on `SessionReopened` keeps nodes in `surviving_jobs` as `running` (marking
  only non-surviving open nodes `lost`).
- `internal/sessionview/store.go` and web/iOS projections preserve `awaiting_jobs` when
  `SessionReopened.awaiting_jobs` is true and suppress spurious idle notifications on restart
  reopen.
- `internal/server/activity.go` sets `OutputAvailable` from `info.OutputAvailable` (live buffer or
  disk-backed segments) and updates `GetJobOutput` to check `!info.OutputAvailable` rather than
  blanket-rejecting `info.Restored`.

### 4. Persisted `wait` and wake-eligible idle continuation

No new tool is introduced.

#### Persisting `wait` via `wait_started`

Mirroring `ask_user` (`tool_call` → `question_asked` → `resume_question.go`), `wait` emits a
durable `wait_started` event once it resolves its targets and effective deadline:

- Fields: `call_id`, resolved `job_ids` (already expanded when omitted), `for` (`any`|`all`),
  effective `deadline` (RFC3339 timestamp already incorporating the 600-second default and
  work-loop `ShellWaitCap`), and `shell_only`.
- `findResumableWait(events)` matches a trailing unresolved coordinator `tool_call(name: "wait")`
  paired with `wait_started` and no later `tool_result`, `model_turn`, `user_input(queued: false)`,
  `user_input_delivered`, `job_notified`, `context_view_changed`, or `session_forked`.
- **Preserving queued user messages (`user_input(queued: true)`):** A `user_input` event with
  `queued: true` does not invalidate an unresolved `wait_started` (whether it arrived between
  `tool_call(wait)` and `wait_started`, or after `wait_started`). `findResumableWait` recovers all
  undelivered `user_input(queued: true)` corrections pending for that active run in sequence order
  (scanning from the start of the active run and excluding any already matched by
  `user_input_delivered`). Before `awaitRestoredWait` blocks, those corrections are seeded into
  the session's correction queue (closing `InputWake` immediately), so the wait returns early with
  the normal interrupted-by-user-message `tool_result` and the post-tool checkpoint delivers the
  corrections (`user_input_delivered`) in exact live order.
- **Already-claimed targets:** If `job_claimed` events were recorded for one or more targets
  before shutdown (even all of them, before `tool_result` was written), `awaitRestoredWait`
  includes those durable claimed reports in the wait result without re-emitting `job_claimed`,
  collects any remaining targets, and emits the missing `tool_result`.
- **Expiry handling:** If `now >= deadline`, restoration runs an explicit non-blocking collection
  of completed reports plus still-running IDs (never passing `0` to `waitTool` or `Registry.Wait`,
  where `0` means default or unbounded).
- **Pre-armed input routing and ID mapping:** Generalized `placeholderCallID(history, toolName,
  rawID)` preserves raw-vs-canonical IDs and batch ordering. Before the prepared session is
  published to the manager map, active-turn input routing (`s.running` / wait-active state under
  `s.steerMu`) is armed so any `SendInputMessage` arriving before or during the restored wait
  queues as a correction and closes `InputWake` rather than landing in idle `messageCh`.
- On completion, timeout, or user-input wake, `awaitRestoredWait` emits any new `job_claimed`
  events and `tool_result(name: "wait", id: call_id, restored: true)`, replaces
  `engine.DanglingToolResult` in `loop.History()`, and continues the coordinator turn.

#### Wake-eligible idle continuation

Wake eligibility is determined from durable lifecycle state plus `PendingContinuation("coordinator")`,
never from `PendingResponse()` or raw job execution alone:

- A session is wake-eligible on reopen only when its latest lifecycle state is
  `session_idle(awaiting_jobs: true)` with no intervening human boundary (pause, refusal, error or
  model-switch gate, blocked final report, fork-first-input, or pending question), **and**
  `s.deps.Jobs.PendingContinuation("coordinator")` is true (excluding already-claimed killed jobs
  that are only unwinding).
- Prepared before publication with `setIdle(true)` (`jobWakeArmed = true`), the session
  immediately reports `StatusWithJobContinuation() == (StatusIdle, true)` and enters
  `awaitNextRun(true)`. Downtime completions wake it immediately; live jobs keep it wake-eligible
  and protected from idle reaping until completion or user input arrives.
- Ordinary manual reopens that do not satisfy this durable condition keep `setIdle(false)` and
  `awaitNextRun(false)`.

### 5. Passive resource ownership, restart manifest, leases, and crash policy

#### Passive resource supervision (closed sessions and crashes)

Because surviving supervisors and leases outlive a closed conversation, passive supervision is
separate from reopening a session:

- On every persistent daemon startup (planned restart or crash), Phase 3 scans `<state-dir>/leases.json`
  and registered projects' job directories for every durably started supervised job (`spec.json`
  matching a `job_started` in the session log) that has neither a confirmed `exit.json` nor an
  explicit operator-cleared marker (covering both held `shim.lock` and dead-supervisor
  `execution_unknown` states, without writing `SessionReopened` or starting model turns):
  - Non-handed-off subagent shells are immediately sent `kill` over `ctl.sock` and joined (or
    marked `execution_unknown` if control was lost).
  - Remaining live or `execution_unknown` shells are held in a manager-level passive supervisor
    table that maintains worktree cleanup guards (blocking merge/discard/removal), GC protection,
    and active shell-overlap registrations in `workspacelease.Ownership` across GC runs and
    subsequent daemon restarts until exit is confirmed or an operator clears them explicitly.
  - When a session is later opened (by startup manifest recovery, `ReopenForInput`,
    `ReopenForAnswer`, or manual `Reopen`), its passive supervisor handles transfer atomically
    into the live `*Session`'s `jobs.Registry`.
- `leases.go` decouples lease entries from requiring an in-memory `*Session` pointer: a lease
  entry binds `(primary, session_id, job_id, expires_at)` plus an optional live `*Session` or
  passive supervisor handle.
  - Job-bound leases (`job_id != ""`) persist in `<state-dir>/leases.json` (`0600`) on acquire and
    release so they survive both planned restarts and unplanned crashes while their job executes.
  - While its holder session remains closed (e.g., parked in `ask_user`, idle with a planned-restart
    unbound lease, or un-reopened after a crash), the lease manager watches the passive job's
    execution completion (`Done` + `WaitExecution`, never `Status() == Running`) and `expires_at`.
  - If a passive lease releases (execution completed or TTL expired) while the holder session is
    closed, the lease manager removes it from memory and `leases.json` and appends a single
    `lease_released` event under the per-session serialization lock (at-most-once, never racing
    `Reopen`). If the session reopens while the lease is still held, ownership transfers to the
    live `*Session`.

#### Cross-process idempotent restart manifest (`restart-state.json`)

On graceful shutdown, after detaching sessions and closing logs, the daemon writes
`<state-dir>/restart-state.json` (`0600`, atomic rename) stamped with a unique `restart_id`:

- `waiting_sessions`: sessions with an unresolved `wait_started` (regardless of whether all
  targets were already `job_claimed` before `tool_result`) **or** `StatusWithJobContinuation() ==
  (StatusIdle, true)`, each recording `(project, session_id, kind, last_seq)`.
- `interrupted_sessions`: non-loop sessions (until Slice 5) whose coordinator turn was cut short
  at drain expiry, recording `(project, session_id, last_seq)`.
- `unbound_leases`: unexpired unbound leases held at shutdown (including those held by closed
  `ask_user` or idle sessions), restored passively with original `expires_at` and canonical
  primary workspace.
- `suspended_loops`: projects with a matching `restart_id` scheduler checkpoint (Slice 5).

Idempotency across repeated crashes during recovery:

- Startup reads `restart-state.json` in place rather than deleting it before Phase 3 completes.
- Each entry is authorized **only** if the session's durable log has not already recorded
  `session_reopened` for this `restart_id` and has not advanced past `last_seq` via later turns
  or user input. If the daemon dies after reopening and answering a session but before deleting
  `restart-state.json`, the next startup sees `seq > last_seq` (and `restart_id` already consumed)
  and never injects a second restart nudge.
- Phase 3 writes each prepared session's `session_reopened(restart_id)` before Phase 4 releases
  execution, and removes `restart-state.json` once Phase 3 preparation completes (with a bounded
  attempt guard so a corrupt entry is quarantined and logged rather than looping). Missing
  projects are logged and skipped. If writing `restart-state.json` fails on shutdown, the daemon
  logs the error and still finishes clean detach so supervisors survive.
- **Interrupted sessions** reopen automatically: if replayed history already owes a model response
  (e.g., repaired dangling non-wait tool call), `s.run()` continues; otherwise Phase 3 records one
  durable synthetic restart-context event (rendered as synthetic context, like `job_notified`,
  never as an apparent human `user_input`). Sessions parked in `ask_user` stay closed on disk until
  answered via `ReopenForAnswer` (with any leases held passively meanwhile).

#### Crash policy (no graceful manifest)

An unplanned crash (`SIGKILL`, panic, OOM, power loss) produces no `restart-state.json`:

- Startup runs passive resource supervision (killing orphaned non-handed-off subagent shells,
  guarding active worktrees, registering shell overlaps, and holding unexpired job-bound leases
  from `leases.json`), but does **not** automatically start model turns or resume work loops.
- Unbound leases (not tied to a surviving external process) are not restored after an unplanned
  crash.
- Manual `Reopen` (or `ReopenForInput` / `ReopenForAnswer`) transfers passive supervisors and
  harvests exit records seamlessly.

### 6. Work-loop continuity across graceful restart (Slice 5)

`spec.md` §9.1 currently specifies: *"Daemon restart restores an active/waiting loop as interrupted
and never silently resumes it."* Slices 1–4 preserve that rule: work-loop sessions (`OriginWorkLoop`)
are excluded from automatic restart-manifest reopening until Slice 5 lands, so active loops stay
visibly interrupted rather than reopening uncoordinated sessions.

Slice 5 explicitly revises `spec.md` §9.1 for **planned graceful restarts only** (crashed or
unmatched checkpoints still restore as interrupted and never silently resume):

- **All slots covered (working and waiting):** Both foreground working slots (including slots
  blocked in `wait`) and detached waiting slots support restart-release without calling
  `reclaimIfCurrent`, preventing replacement work from starting on the same slot or focused task.
  Sessions that finished just as shutdown began are settled into the checkpoint before exit.
- **Generation-bound scheduler checkpoint:** Graceful restart writes a complete scheduler
  checkpoint (`state: "suspended_restart"`, stamped with `restart_id`) retaining `LoopID`,
  `Parallel`, original backlog `Baseline` (never reseeded from today's backlog on resume),
  `Sessions`, per-lane backoff/stall counters, provider-outage pacing state, every working and
  waiting slot's `session_id`, `focus`, `attempt`, progress `before`-hashes, slot kind, resource
  envelope, and accumulated usage/cost.
- **Unmatched checkpoint safety:** Startup resumes a loop only when `restart-state.json` and the
  project's checkpoint carry the same matching `restart_id`. `restoreWorkLoopLocked` converts any
  stale or unmatched `"suspended_restart"` checkpoint (for example, a crash between writing the
  loop checkpoint and writing `restart-state.json`) into visibly interrupted state.
- **Unattended semantics preserved:** Re-adopted loop sessions reopen with their original
  `OriginWorkLoop` unattended configuration (`unattended = true`, `ShellWaitCap`, loop budget and
  slot wake gates) rather than reverting to attended interactive sessions.
- **Exactly-once accounting:** Startup Phase 3 re-adopts every working and waiting slot into the
  reconstructed `workLoop` with its task holds, budgets, and slot gates armed before Phase 4
  releases execution. No spurious interrupted outcome or digest push is emitted on planned restart.

## Rejected alternatives

- **Separate `wait_until` / probe tool.** Unnecessary once supervised bash jobs and persisted
  `wait` survive restarts. If daemon-scheduled polling without a sleeping shell is wanted later,
  optional probe parameters on `wait` can cover it without adding a second waiting tool.
- **Socket handoff / blue-green draining daemon (`tableflip` / `SO_REUSEPORT`).** Draining sessions
  in the old daemon become invisible to web and iOS clients connected to the new daemon unless an
  internal RPC proxy is built, and concurrent daemons contend on `.ycc/watches/`,
  `.ycc/workloop.json`, `.git/ycc/claims.json`, and `session-view.sqlite`.
- **Two persistent daemons sharing a state directory.** Unsafe because both schedule watches and
  mutate shared registries; isolating state directories splits clients and session history.
- **Auto-reopen only (without a job supervisor).** Recovers remote polling loops at the cost of
  extra turns, but still kills local long-running commands and discards their output.

## Rollout and task breakdown

Each slice is independently verifiable, keeps unsupported later-slice recovery paths disabled until
their contract lands, and updates `spec.md` (§3.1, §5.1, §7.3, §9.1, §11), `docs/design/async-jobs.md`,
and client projections as its behavior lands:

1. **Graceful daemon shutdown, admission fencing, and phased startup.**
   Acquire exclusive daemon lock + bound listener before recovery side effects, trap
   `SIGINT`/`SIGTERM` in `Serve()` with `--shutdown-drain`, fence RPCs and background workers,
   implement restart-suspend exit in `engine.Loop` / `wait` / `ask_user` without premature
   `tool_result` emission, eliminate deferred `ReclaimAll` races, and split startup into
   ownership → static validation → prepared recovery → execution/scheduling release (with
   workstream readiness respecting `awaiting_jobs`).
2. **Supervised background bash jobs and disk-backed retained evidence** *(depends on 1; gated off
   in production `Serve()` until Slice 4 lands)*.
   Add `internal/jobshim` with the two-step launch handshake, derived owner-only runtime-dir
   control socket + peer-UID check + `shim.lock`, zombie reaping while attached, fail-closed
   `execution_unknown` handling, rotating 256 KiB tail segments + 4 MiB head capture
   (`artifact.bin`), per-session disk bounds, and GC protection for live locks, unharvested exits,
   and uncleared `execution_unknown` states.
3. **Passive supervision, idempotent job reconciliation, and projection parity** *(depends on 2;
   supervised bash remains opt-in until Slice 4 adds crash-safe job-bound leases)*.
   Serialize `Reopen` per session ID, add startup passive supervisor discovery for all unconfirmed
   supervised jobs (cleaning up non-handed-off subagent shells, guarding worktrees, and
   registering shell overlaps under existing `workspacelease.Ownership` rules), implement
   five-axis reconciliation with tracked-live `jobs.Registry` restoration, emit downtime/lost
   `job_finished` before `SessionReopened`, exclude reconciled survivors from `ReplayHistory`
   legacy loss synthesis, adopt conservative post-restart shell attribution, and update
   `SessionReopened` (`surviving_jobs`, `awaiting_jobs`), `sessionview`,
   `internal/server/activity.go` (including `GetJobOutput`), and web/iOS projections.
   Implemented in two tasks: reopen reconciliation (0615) and startup passive supervision of closed
   sessions (0619). 0615 also leaves coordinator-owned supervised jobs running across a graceful
   restart. It also reopens a session whose durable state was `session_idle(awaiting_jobs)` into
   wake-eligible idle (the Slice 4 rule below) so that `awaiting_jobs` is recorded truthfully.
   0619 adds the startup scan and the manager's passive table (`tools.PassiveJobs`). The scan holds
   jobs without writing to any log. Reopen adopts the held handles. The table and the on-disk state
   guard workstream merge, integration, discard and readiness. An operator who has confirmed by
   hand that a command stopped writes a `cleared` file into its job directory
   (`<session-dir>/jobs/<job-id>/cleared`), which settles a job that has no exit record. Merge,
   integration and discard now kill and join a live
   session's jobs before advancing the base or recording the discard (`jobs.Registry.Quiesce`), and
   stop the session only afterwards. Both features run only when a job launcher is set, until 0616
   enables supervised launches in `Serve()`.
4. **Persisted `wait`, wake-eligible idle continuation, idempotent restart manifest, and passive
   leases** *(depends on 1, 3; enables supervised bash launches by default in persistent `Serve()`,
   and excludes `OriginWorkLoop` sessions until Slice 5)*.
   Emit `wait_started`, implement `resume_wait.go` (resolved targets, effective deadline with
   non-blocking expiry handling, full-run queued `user_input(queued: true)` preservation,
   already-claimed targets, generalized `placeholderCallID`, pre-armed `InputWake` input routing),
   restore durable wake-eligible idle sessions via `setIdle(true)` +
   `PendingContinuation("coordinator")` + `awaitNextRun(true)`, add generation-bound idempotent
   `restart-state.json` recovery with synthetic restart context, and decouple `leases.go` for
   passive lease ownership plus persistent job-bound `leases.json` with fail-closed
   `execution_unknown` and `WaitExecution` gating.
   Implemented in three tasks: persisted `wait` and the wake-eligible idle check (0616), passive
   lease ownership and `leases.json` (0621), and the restart manifest with supervised launches on
   by default (0622).
5. **Work-loop continuity across graceful restart** *(depends on 1, 4)*.
   Revise `spec.md` §9.1 for planned graceful restarts, add working- and waiting-slot restart
   release, persist and restore the generation-bound full scheduler checkpoint
   (`"suspended_restart"`), convert stale/unmatched checkpoints to interrupted in
   `restoreWorkLoopLocked`, preserve unattended session semantics on re-adoption, and keep crashed
   loops visibly interrupted.
