---
id: "0251"
title: 'workstream_ready: derive and emit workstream completion state'
status: done
priority: 2
created: "2026-08-06"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - Parallel workstreams (git worktrees)
    - docs/design/workstream-integration.md#3. Lifecycle, extended
---

## Description

Nothing currently signals that a workstream has finished its work — its session just ends —
so no automation can attach to completion. Introduce readiness as an explicit, observable
state.

**Ready** = the workstream's session reached a terminal state AND the branch has commits
since `BaseCommit` AND the session did not end in error/blocked; when the workstream targets
a backlog task, that task must be `in_review` or `done`. A workstream that ends errored or
blocked goes to `needs_attention` instead and is never auto-integrated.

- Emit `workstream_ready` / `workstream_needs_attention` on the workstream's session stream
  via the existing `emitWorkstreamEvent`, with the reason recorded.
- Add the corresponding registry statuses and surface them in `WorkstreamInfo.status` (and
  the reducer/projection) so TUI and iOS rows can distinguish "still working", "ready", and
  "needs attention".
- Evaluate readiness where the session reaches its terminal state (and on
  `ReconcileWorkstreams`, so a daemon restart re-derives it).

## Acceptance criteria

- [ ] A workstream whose session ends with commits transitions to `ready` and emits the event.
- [ ] A workstream whose session errors/blocks, or that produced no commits, transitions to
      `needs_attention` (with reason) and never to `ready`.
- [ ] Statuses round-trip through the registry, `ListWorkstreams`, and the reducer.
- [ ] Readiness is re-derived after a daemon restart.

## Plan

Derive and emit workstream completion state (ready / needs_attention) per docs/design/workstream-integration.md §3, §7.

1) Events (internal/event/event.go, reduce.go)
- Add `WorkstreamReady Type = "workstream_ready"` and `WorkstreamNeedsAttention Type = "workstream_needs_attention"` beside the existing workstream event types.
- Reducer: fold them into `Projection.WorkstreamState` ("ready" / "needs_attention"); add `WorkstreamAttentionReason string` set from the needs_attention event's `reason` and cleared on ready/merged/discarded. Extend reduce tests.

2) Registry (internal/workstream/registry.go)
- New statuses `StatusReady Status = "ready"` and `StatusNeedsAttention Status = "needs_attention"`; both NON-terminal (Terminal() unchanged). Add a helper like `func (s Status) InFlight() bool` = active|ready|needs_attention (worktree live, mergeable).
- Add `StatusReason string json:"status_reason,omitempty"` on Workstream (why it needs attention; cleared on other transitions).
- Add `BySession(sessionID string) (Workstream, bool)` lookup.
- Add an atomic CAS `Transition(id string, to Status, reason string, from ...Status) (bool, error)`: under the mutex, only applies when current status ∈ from; persists; returns whether it changed. This is what evaluation uses so it can never overwrite merged/discarded/stale (race with the merge flow).
- Registry tests for round-trip persistence of the new statuses + reason and for Transition CAS semantics.

3) Readiness derivation + watcher (internal/session — new file e.g. workstream_ready.go)
- `deriveWorkstreamReadiness(ws, sessStatus event.Status, commitCount int, taskStatus)` pure-ish helper: 
  - session status error → needs_attention, reason "session ended in error";
  - commit count since BaseCommit == 0 → needs_attention, "no commits since base";
  - ws.TaskID set and the task (read from the WORKTREE's docs store, docs.NewStore(ws.WorktreePath)) is not in_review/done → needs_attention with a reason naming the task + its status (a blocked task reads naturally here); unreadable task → needs_attention with reason;
  - else → ready.
- `evaluateWorkstreamReadiness(wsID)` on the Manager: re-fetch ws; skip unless Status.InFlight(); compute commit count via primaryRepo/commitCount; derive; `Transition` CAS from the in-flight statuses; ONLY when the status actually changed, emit `workstream_ready` {workstream, branch, commits, task} or `workstream_needs_attention` {workstream, branch, reason} via emitWorkstreamEvent.
- Watcher: `startWorkstreamWatcher(ws, log)` modeled on startNotifyWatcher (log.Subscribe from LastSeq; pump goroutine, so emitting back into the same log is safe): 
  - on session_idle / session_error / session_stopped → evaluateWorkstreamReadiness;
  - on user_input / user_input_delivered / resumed / model_turn → CAS ready|needs_attention→active (clear reason, no event) so a resumed stream reads "working" again.
- Attach the watcher in SpawnWorkstream (after registry Add) and in Reopen when the reopened session id maps to an in-flight workstream (registry BySession).

4) Merge/discard race hardening (internal/session/workstream_merge.go)
- MergeWorkstream: on a real successful merge, SetStatus(merged) BEFORE Stop()ing the session (event emit stays before Stop since the log closes); DiscardWorkstream likewise sets discarded before Stop. Then the watcher's stop-triggered evaluation sees a terminal status and no-ops (Transition CAS is the backstop).
- Relax the `Status != StatusActive` guards in PreviewWorkstreamMerge/MergeWorkstream to `!Status.InFlight()`; DiscardWorkstream allows InFlight() or stale.

5) Reconcile on restart (internal/session/session.go ReconcileWorkstreams)
- Include ready/needs_attention (all InFlight) workstreams in the stale sweep, not just active.
- For each surviving in-flight workstream whose session is NOT live: reduce its durable log at <worktree>/.ycc/sessions/<id>/events.jsonl (readEventsTolerant + event.Reduce); if the reduced status is idle/error/stopped, run the same evaluation (CAS + emit-on-change) so readiness is re-derived after a daemon restart.

6) Surfaces
- Server: no proto change needed — WorkstreamInfo.Status is a string and ready/needs_attention are non-terminal so ListWorkstreams enrichment (commit count, session status) already applies.
- TUI (internal/tui/workstreams.go): wsRowStatus shows "ready" (plain or subtle) and a loud "⚠ needs attention" (like conflict); the merge-gate check `GetStatus() != "active"` must accept ready/needs_attention too.

7) Tests (internal/session/workstream_test.go / new)
- Session-ends-with-commits → registry flips to ready + workstream_ready in the log.
- Session ends with no commits → needs_attention with reason; errored session → needs_attention; never ready.
- Task-targeting workstream: task left in_progress/blocked → needs_attention; task in_review → ready.
- Reconcile test: registry says active, durable log reduced to idle, commits exist → after ReconcileWorkstreams status is ready (restart re-derivation).
- go build ./... && go test ./internal/... (known flaky: internal/session, internal/setup, internal/tools background-bash — compare against HEAD).

Out of scope (follow-on): iOS WorkstreamStatus enum renders the new statuses (falls back to .unknown today).

### Starting points
- internal/workstream/registry.go — Status consts, Terminal(), SetStatus; add CAS Transition + BySession
- internal/session/workstream_merge.go — emitWorkstreamEvent, commitCount, WorkstreamSessionStatus, MergeWorkstream/DiscardWorkstream status guards
- internal/session/session.go:1704 startNotifyWatcher — the watcher pattern to copy; SpawnWorkstream ~1407; ReconcileWorkstreams ~1521; Reopen ~1797
- internal/session/history.go readEventsTolerant — tolerant durable-log read for reconcile
- internal/event/event.go workstream event types; internal/event/reduce.go WorkstreamState folding
- internal/tui/workstreams.go wsRowStatus (~105) and merge gate (~200)
- docs/design/workstream-integration.md §3 — readiness definition
- internal/docs/docs.go Store.Get / Status constants (StatusInReview, StatusDone) — read task from the worktree's store, not the primary tree

## Work log
- 2026-08-07 plan: Derive and emit workstream completion state (ready / needs_attention) per docs/design/workstream-integration.md §3, §7.  1) Events (internal/event/event.go, reduce.go) - Add `WorkstreamReady Type = 
…[truncated]
- 2026-08-07 context hints: 8 recorded with plan
- 2026-08-07 context hints: internal/workstream/registry.go — Status consts, Terminal(), SetStatus; add CAS Transition + BySession; internal/session/workstream_merge.go — emitWorkstreamEvent, commitCount, WorkstreamSessionSt
…[truncated]
- 2026-08-07 implementer report: Implemented task 0251 workstream completion readiness.  Changes: - Added `workstream_ready` / `workstream_needs_attention` events and reducer projection support, including attention reasons and cleari
…[truncated]
- 2026-08-07 revision: Addressed both review findings.  Changes: - Updated `workstreamError` to map all lifecycle precondition phrasings—legacy `is not active`, new `is not in flight`, and `cannot be discarded`—to Conne
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): revise — The core readiness state, persistence, reducer, restart reconciliation, and TUI surface are implemented well, and the project builds. However, a resumed workstream can remain incorrectly marked ready/
…[truncated]
- 2026-08-07 revision: Addressed both major reviewer findings.  Changes: - Retry-resume now emits `event.Resumed` when the parked run loop actually consumes `retryCh`, immediately before retrying the failed turn. This makes
…[truncated]
- 2026-08-07 review (sol): accept — The revision addresses both prior findings. Retry resumes now emit `resumed` when the parked run loop actually consumes the retry, causing readiness state to return to active, with a dedicated test co
…[truncated]
- 2026-08-07 decision: accept — commit: workstream_ready: derive and emit workstream completion state (task 0251)  Workstreams now transition to ready / needs_attention when their session reaches a terminal state: new registry statuses (wit
…[truncated]
