---
id: "0198"
title: Make event-log persistence failures fatal and observable
status: done
priority: 1
created: "2026-07-15"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - Session & event log
    - Agent engine#API failure handling (classification, retry, session_error)
---

## Description
`event.Log.Record` currently increments the sequence and appends/broadcasts an event in memory even when JSON marshaling, file append, or `fsync` fails. Live clients can therefore observe state that is absent after restart, violating the event log's source-of-truth invariant.

Redesign the recorder/error path so a durable append failure cannot be treated as success. A session must stop or enter an explicit fatal/error state rather than continue with unreplayable in-memory history.

## Acceptance criteria
- [ ] JSON encoding, file write, and sync failures are surfaced to the owning session rather than only logged.
- [ ] An event is not exposed as durably recorded when its append did not succeed.
- [ ] Sequence handling remains monotonic and restart-safe after any failure.
- [ ] After a persistence failure the session cannot continue mutating from history that is absent on disk.
- [ ] Subscribers receive a clear terminal failure when feasible without pretending that failure itself was durably persisted to the broken log.
- [ ] Tests inject write and sync failures and verify no in-memory/disk divergence is reported as success.
- [ ] Normal append, replay, subscribe, and transient-broadcast behavior remains unchanged.
- [ ] `go test ./...` passes.

## Plan

Goal: a durable append failure in event.Log.Record must never look like success — the log fails terminally, subscribers learn, and the owning session stops instead of continuing with unreplayable in-memory history.

Design:

1. internal/event/log.go — fail-fast log
   - Abstract the file handle behind a tiny interface (Write/Sync/Close) so tests can inject write/sync failures; OpenLog keeps using *os.File.
   - Record: check errors from json.Marshal, Write AND Sync (Sync's error is currently dropped). On ANY of those failing:
     * do NOT append the event to l.events and roll back the seq increment, so the in-memory view never diverges from disk and seq stays monotonic/restart-safe (nothing further will ever be appended, so no reuse);
     * transition the log to a terminal failed state: record the error (new `failed error` field), mark it closed (so subsequent Record/Broadcast are no-ops like today's closed path), close the file (tolerating double-close via Close guard);
     * before/while closing, enqueue a transient (Seq=0, Transient=true) session_error event onto every live subscriber's transient queue — data like { msg: "event log persistence failed: …", kind: "event_log" } — then cond.Broadcast so pumps deliver it and then terminate on closed. This gives subscribers a clear terminal failure without pretending the failure itself was persisted.
     * invoke an owner-registered failure callback (see below) exactly once, WITHOUT holding l.mu (goroutine or after unlock) to avoid deadlock, since the callback fires from inside an Emit on the session's own goroutine.
   - Add `Err() error` returning the terminal persistence failure, and `OnFailure(func(error))` (or an OpenLog option / setter) for the owning session to register a handler. Record on an already-failed log returns an unstamped event (Seq=0), same shape as the closed path — callers can distinguish "not durably recorded" by Seq==0.
   - Close(): if already failed/closed, return nil (or the stored error) without double-closing.
   - Broadcast/Subscribe/Snapshot/LastSeq/replay behavior unchanged for the healthy path.

2. internal/session — surface to the owning session
   - In Manager.newSession (internal/session/session.go ~1608), after constructing the Session, register log.OnFailure: it should set s.setStatus(event.StatusError) and s.cancel() so the run loop exits at the next ctx check and the session stops mutating (SendInput etc. already dead-end on a cancelled/stopped session). Do NOT emit a session_error through the broken log (Record is a no-op anyway); the transient broadcast in step 1 covers live clients.
   - Check other OpenLog call sites (session.go ~1361 Start, ~1842 reopen, workstream_merge.go:60) — the two session paths go through newSession so the handler is registered there; the workstream merge log is short-lived, best-effort is fine (no handler needed, but confirm nothing breaks).

3. Tests (internal/event/log_test.go or new file)
   - Inject a failing file (write error, and separately sync error): verify Record returns Seq==0, the event is NOT in Snapshot(), LastSeq unchanged, Err() reports the failure, subsequent Record is a no-op, and a live subscriber receives a transient session_error followed by channel close.
   - Verify a subscriber that subscribed before the failure got all previously persisted events, and that reopening the path (with a real file) resumes seq from the last durable event (restart safety).
   - Session-side: a small test that the OnFailure wiring sets StatusError and cancels the session (can drive via the injected failing file if reachable, else unit-test the handler function).
   - Healthy-path regression: existing log tests must pass unchanged.

4. Verify: go build ./... && go test ./internal/event/... ./internal/session/... then go test ./... (note: internal/session, internal/setup, internal/tools have known flaky tests — compare against HEAD before blaming new failures).

Out of scope: tolerating a partially-written trailing JSONL line on restart (readEvents corrupt-line hard error stays as-is); wiring persistence failures into every other Recorder implementation (StdoutRecorder/FuncRecorder unchanged).

### Starting points
- internal/event/log.go — Log.Record currently ignores marshal/Sync errors and only log.Printf's Write errors; closed-log path returns unstamped Event (Seq=0)
- internal/event/event.go — Recorder interface (Record returns Event, no error) is used widely; do NOT change its signature
- internal/session/session.go:1608 Manager.newSession — where the session owns the log; s.cancel / s.setStatus(event.StatusError) available; setStatus does not emit
- internal/session/session.go:998-1094 Session.run — run loop exits on s.ctx.Err()
- subscriber transient queue + pump in log.go Subscribe — reuse for the terminal transient session_error delivery
- memory: `go test ./...` has known flaky tests (internal/session, internal/setup, internal/tools background-bash); verify against HEAD before blaming new work

## Work log
- 2026-08-07 plan: Goal: a durable append failure in event.Log.Record must never look like success — the log fails terminally, subscribers learn, and the owning session stops instead of continuing with unreplayable in
…[truncated]
- 2026-08-07 context hints: 6 recorded with plan
- 2026-08-07 context hints: internal/event/log.go — Record currently ignores marshal/Sync errors and only log.Printf's Write errors; closed-log path returns unstamped Event (Seq=0); subscriber transient queue + pump already ex
…[truncated]
- 2026-08-07 preload: 1 file(s), ~11 KiB seeded into implementer context
- 2026-08-07 implementer report: Implemented fatal, observable event-log persistence failures.  Changes: - Added an injectable `logFile` durability interface and made `Log.Record` validate JSON encoding, full writes, and `Sync` befor
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): revise — The log itself now fails terminally, with good write/sync/encoding checks, restart-aware sequence behavior, subscriber notification, and passing tests. However, cancellation is not yet enforced at the
…[truncated]
- 2026-08-07 revision: Addressed both reviewer gaps.  Changes: - Added `event.Emitter.Err()`, nil-safe and compatible with any recorder implementing `Err() error`, without changing the `Recorder` interface. - Added engine d
…[truncated]
- 2026-08-07 review (sol): revise — The revision fixes the previously identified idle-input handoff race and correctly aborts around model-turn/tool-call/tool-result persistence boundaries, with useful new tests and a passing full suite
…[truncated]
- 2026-08-07 revision: Closed the remaining checkpoint/session continuation paths.  Changes: - `Session.CheckpointMessages` now checks persistence after every durable checkpoint-path emit:   - `UserInputDelivered` via an er
…[truncated]
- 2026-08-07 review (sol): revise — The checkpoint and session-run continuation gaps are now addressed: failed checkpoint events no longer enter history or reach a backend, session terminal emits are followed by barriers, the added test
…[truncated]
- 2026-08-07 revision: Addressed the final settings persistence ordering issue.  Changes: - `Session.SetRoleConfig` now:   - emits `RoleConfigChanged`;   - immediately checks `s.logFailure()` and returns the terminal error
…[truncated]
- 2026-08-07 review (sol): accept — The revised change now satisfies the task: append/encoding/sync failures terminally fail the log without exposing the candidate event, notify subscribers transiently, preserve restart-safe sequence be
…[truncated]
- 2026-08-07 decision: accept — commit: Make event-log persistence failures fatal and observable (task 0198)  Log.Record now validates encode/write/sync before exposing an event: any failure terminally fails the log (no seq advance, no in-m
…[truncated]
