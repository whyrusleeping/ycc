---
id: "0179"
title: 'Daemon-side work loop: move the work (loop) driver from clients into the daemon'
status: done
priority: 2
created: "2026-07-08"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - 9. Modes (the home menu)
    - "20.6"
    - docs/design/ios-client.md#9. Daemon-side work loop (decision, prerequisite for loop parity)
---

## Description
Move the `work (loop)` unattended backlog drain from the client (TUI driver) into the daemon, per `docs/design/ios-client.md` §9 (user-accepted decision). Today the loop is a client concern (spec §9): the TUI starts the next `work` session when one finishes, enforces per-loop budget caps (§20.6), applies the no-progress guard, and accumulates the end-of-batch digest. A phone client cannot host that driver (iOS suspends backgrounded apps), and a loop that dies with its client is fragile even for the TUI.

**Scope note (split, this task = daemon-side core).** The TUI migration (deleting
the client driver, pointing the UI at the new RPCs, preserving UX) is split out to
**task 0195** (depends on this). This task delivers the daemon-side loop engine +
RPCs + tests + spec/docs; the TUI keeps its existing client driver until 0195 lands
(nothing breaks — the daemon loop is simply unused by the TUI until then).

- Design the RPC surface (part of this task): `StartWorkLoop(project)` and `StopWorkLoop(project)` (graceful — current session finishes, next not picked), plus `GetWorkLoop(project)` returning loop status + digest so a reconnecting client can observe state and Subscribe to the current session. Keep spec §14's "no separate facade" posture — plain Connect RPCs. (Real-time loop-lifecycle streaming is optional and may be deferred to 0195; GetWorkLoop polling + Subscribe(current session) satisfies "observe".)
- Move loop mechanics daemon-side: next-ready-task selection via fresh `work` sessions (autonomous), the no-progress guard (stop if a finished session left its expected task unchanged — backlog fingerprint), per-loop budget caps (§20.6 — currently "client-driven"), and the completion digest (per-task completed/blocked/in_review/created with commits/verdicts/tokens/cost, exposed via GetWorkLoop AND pushed via the existing notifier `digest` kind, no client `Notify` call needed).
- Update spec §9 (loop is no longer "a client concern"), §20.6 (loop cap enforcement moves daemon-side), and docs/remote-api.md (new RPCs).

## Acceptance criteria
- A loop started via the RPC continues across client disconnects; a reconnecting client can observe its state via `GetWorkLoop` and gracefully stop it via `StopWorkLoop`.
- No-progress guard, loop budget caps, and the completion digest run daemon-side, with tests.
- The completion digest is pushed via the daemon notifier (`digest` kind) — no client `Notify` call needed.
- Spec §9/§20.6 + remote-api docs updated.
- Existing TUI client-side loop continues to build/pass unchanged (its removal is task 0195).

## Plan

Deliver the DAEMON-SIDE core of the work loop (engine + RPCs + guard + caps + digest + spec/docs). Do NOT touch the TUI's client-side loop driver — that migration is task 0195. The daemon loop is simply unused by the TUI until 0195.

### 1. Proto (proto/ycc/v1/ycc.proto)
Add messages:
- `WorkLoopDigestTask { id, title, status, sha, verdict_tally, tokens(int64), cost(double), price_status, reason }` — mirrors TUI digestTask.
- `WorkLoopSession { session_id, focus, tokens(int64), cost(double), price_status }`.
- `WorkLoopInfo { loop_id, project, state (running|stopping|finished), current_session_id, outcome, started_at (RFC3339), sessions_run(int32), repeated WorkLoopSession sessions, repeated WorkLoopDigestTask completed/blocked/in_review/created, total_tokens(int64), total_cost(double), cost_status }`.
- `StartWorkLoopRequest{project}` / `StartWorkLoopResponse{WorkLoopInfo loop}`; `StopWorkLoopRequest{project}` / `StopWorkLoopResponse{WorkLoopInfo loop}`; `GetWorkLoopRequest{project}` / `GetWorkLoopResponse{WorkLoopInfo loop}` (loop null when none running).
Add 3 RPCs to SessionService: StartWorkLoop, StopWorkLoop, GetWorkLoop (unary). Document them in comments matching the file's style; note real-time streaming is deferred to 0195 (GetWorkLoop polling + Subscribe(current_session_id) satisfies observe).
Regenerate Go stubs with `buf generate` (local plugins). Attempt Swift regen (`buf generate --template buf.gen.swift.yaml` — needs network/remote BSR plugins); if it fails offline, leave the committed Swift generated code as-is (existing Swift still compiles; the new RPCs land in Swift under iOS task 0190) and say so in the report.

### 2. Daemon loop engine (new internal/session/workloop.go)
- `WorkLoop` struct: loopID, project label, resolved workspace, mu, state, currentSessionID, startedAt, loopStarted bool, prevFP string, run accumulator (baseline map[id]*docs.Task snapshot at start, []sessRec, cumTokens, cumCost, costStatus), caps (loopCost/loopTokens from `m.Budget()`), a graceful-stop flag, and a `runSession` function field (see below).
- Manager gets `workLoops map[string]*WorkLoop` (keyed by resolved abs workspace) guarded by a mutex, plus:
  - `StartWorkLoop(project) (*WorkLoop, error)`: resolve workspace (reuse the same resolution as Start/Backlog); error (already-running) if a loop exists for that workspace in state running/stopping; construct the WorkLoop, register it, `go wl.run()`, return a snapshot.
  - `StopWorkLoop(project) (*WorkLoop, error)`: look up by workspace; set graceful-stop; return snapshot (no error if none — return nil loop).
  - `GetWorkLoop(project) (*WorkLoop snapshot or nil, error)`.
- `run()` control loop (keep the CONTROL logic in small pure/injectable pieces so it is unit-testable without a live LLM):
  1. Load backlog via `m.Backlog(project)` → `store.List()`. Set baseline (id→task) + initial fingerprint. Load caps from `m.Budget()`.
  2. Loop iteration:
     a. If graceful-stop requested → finish("loop stopped: requested").
     b. Re-read backlog; `next := topReadyTask(tasks)` (port of the TUI helper: highest-priority ready task with status todo/in_progress; use `docs.BlockingDeps`/`docs.StatusByID` for readiness). If none → finish("loop complete: no ready tasks remain").
     c. No-progress guard: if `loopStarted && fp == prevFP` → finish("loop stopped: session made no progress").
     d. Session-breach guard: if the previous session's own budget was breached → finish("loop stopped: session budget reached").
     e. Per-loop cap guard: if `loopTokenCap>0 && cumTokens>=cap` or `loopCostCap>0 && cumCost>=cap` → finish("loop stopped: budget reached (...)").
     f. Set `loopStarted=true`, `prevFP=fp`. Run one work session via `runSession(...)`.
  3. `finish(outcome)`: build digest, set state=finished + outcome, push notifier `digest` line (`m.Notify(notify.KindDigest, projectLabel, "", "work loop finished: N completed, M blocked, K in review")`).
- `runSession` default implementation (the injectable seam): `m.Start(Config{Project, Mode:"work", InteractionLevel:"autonomous"})`; record currentSessionID; WAIT for the session to finish — poll `sess.Status()` until StatusIdle or StatusError (autonomous work goes Idle only after `finish`; it then blocks on input, so Idle==done), honoring the loop context and graceful-stop (graceful stop still lets the CURRENT session complete). Then snapshot the session from `sess.Log().Snapshot()` (focus/commit_made/review_submitted/tokens — port `snapshotLoopSession`), price it via `usage.ReduceEvents(id, events)` + `usage.Aggregate(entries, m.reg, {})` to get cost/priceStatus/tokens, accumulate into the run (cumTokens/cumCost/costStatus via a mergeCostStatus), capture whether `sess.BudgetBreached()`, then `m.reclaim(id)`. Return the sessRec + breach flag + error.
- Add `Session.BudgetBreached() bool` guarded accessor (reads s.budgetBreached under s.mu).

### 3. Digest (port from internal/tui/tui.go, daemon-side)
Port `buildLoopDigest`, `tallyVerdicts`, `mergeCostStatus`, `applyUsage` (fold pricing into per-session accumulation instead of a separate GetUsage call), and `blockedReasonFromBody` (fill blocked reasons from `store.Get(id).Body`). Build the digest against the baseline snapshot and the final backlog list; classify completed/blocked/in_review/created exactly as the TUI does. Expose the whole thing through `WorkLoopInfo` in snapshots.

### 4. Server RPC handlers (internal/server/server.go)
Add `StartWorkLoop`, `StopWorkLoop`, `GetWorkLoop` handlers that call the manager and marshal the WorkLoop snapshot → `WorkLoopInfo` (a `workLoopToProto` helper). Map unknown-project to CodeInvalidArgument, already-running to CodeFailedPrecondition (or AlreadyExists), other errors to CodeInternal. GetWorkLoop/StopWorkLoop with no active loop return an empty response (nil loop), not an error.

### 5. Tests
- Pure control logic: task selection (topReadyTask port), fingerprint no-progress guard, per-loop token/cost cap stop, session-breach stop, empty-backlog completion, digest roll-up + classification + pricing + blocked-reason (mirror the existing TUI tests: TestLoopDecision*, TestLoopDigestRollup, TestLoopDigestPricingAndReopen) — using the injectable `runSession` seam with a fake that returns canned sessRecs and mutates a fake backlog.
- Server handler tests: Start returns a running loop; a second Start for the same workspace fails; Stop flips to stopping/finished; Get reflects state; GetWorkLoop with no loop returns nil.
- Confirm the notifier digest push fires (assert via a fake notifier / the existing notify test seam).

### 6. Docs
- spec.md §9: the loop is no longer "a client concern" — the daemon drives it (StartWorkLoop/StopWorkLoop/GetWorkLoop); clients start/observe/stop it and it survives client disconnect. Keep the tab/shift+tab UX description (still the client's menu affordance).
- spec.md §20.6: loop-cap enforcement moves daemon-side (was "client-driven").
- docs/remote-api.md: document the three new RPCs + WorkLoopInfo shape, consistent with the existing entries.

### Verify
`buf generate` clean; `gofmt`; `go build ./...`; `go test ./...` (note the three pre-existing failures in memory — verify they are unchanged vs HEAD, don't attribute them to this work). Ensure existing TUI still builds (the fakeClient embeds the interface so new methods are additive; Server embeds Unimplemented handler).

### Starting points
- internal/tui/tui.go:964-1428 — client loop driver to port daemon-side (loopNext, applyLoopDecision, buildLoopDigest, applyUsage, tallyVerdicts, blockedReasonFromBody, snapshotLoopSession, topReadyTask, backlogFingerprint)
- internal/session/session.go:937-987 — Manager struct + Notify; 1023 Start; 1698 reclaim; 1748 Budget; 1819 Backlog
- internal/session/session.go:96-97 budgetBreached field — add BudgetBreached() accessor
- internal/server/server.go:518 ListBacklog + docs.BlockingDeps/StatusByID for readiness; 804 usageRowToProto pattern
- internal/usage: ReduceEvents(sessionID, events) + Aggregate(entries, reg, Options{}) for per-session pricing
- internal/notify/notify.go: KindDigest; Manager.Notify(kind,project,sessionID,line)
- proto/ycc/v1/ycc.proto:459-483 GetBudget/Notify messages as style reference; service block ends ~660
- buf generate (Go local plugins); buf generate --template buf.gen.swift.yaml (Swift, needs network)

## Work log
- 2026-07-15 plan: Deliver the DAEMON-SIDE core of the work loop (engine + RPCs + guard + caps + digest + spec/docs). Do NOT touch the TUI's client-side loop driver — that migration is task 0195. The daemon loop is si
…[truncated]
- 2026-07-15 context hints: 8 recorded with plan
- 2026-07-15 context hints: internal/tui/tui.go:964-1428 — client loop driver + digest to port: loopNext, applyLoopDecision, buildLoopDigest, tallyVerdicts, mergeCostStatus, applyUsage, blockedReasonFromBody, snapshotLoopSessi
…[truncated]
- 2026-07-15 implementer report: Implemented task 0179 — the daemon-side work-loop core. The TUI client driver was left untouched (that's task 0195); the daemon loop is additive and currently unused by the TUI.  ## What changed  **
…[truncated]
- 2026-07-15 review tier: high-powered — reviewers: claude
- 2026-07-15 review (claude): accept — The change delivers the daemon-side work-loop core exactly as scoped by task 0179. It adds a new internal/session/workloop.go loop engine (start/stop/get, control loop, no-progress guard, per-loop tok
…[truncated]
- 2026-07-15 revision: Addressed the review finding: `StartWorkLoop` now correctly handles `project == ""` (default-workspace case).  ## Fix The loop previously stored only the derived display label (`wl.project = projectLa
…[truncated]
- 2026-07-15 review (claude): accept — The daemon-side work-loop core remains complete and correct, and the revision fixes a real correctness bug surfaced in re-review: StartWorkLoop with an empty (default-workspace) project previously pas
…[truncated]
- 2026-07-15 decision: accept — commit: feat(daemon): daemon-side work loop core (StartWorkLoop/StopWorkLoop/GetWorkLoop)  Move the work-loop driver into the daemon (spec §9/§20.6): a new internal/session/workloop.go engine drives fresh a
…[truncated]
