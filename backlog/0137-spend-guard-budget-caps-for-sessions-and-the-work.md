---
id: "0137"
title: 'Spend guard: budget caps for sessions and the work loop'
status: done
priority: 2
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 20. Token usage & cost accounting
    - 11. Interaction levels
---

## Description
Usage/cost tracking is rich (per-turn usage events, live Σ tokens/$ in the status bar, `ycc cost`), but nothing acts on it. The scariest UX moment in an agentic harness is walking away from `work (loop)` in autonomous mode with no ceiling on spend. A budget guard converts the existing telemetry into trust: users will run longer unattended loops when they know the harness stops at a line they drew.

## Acceptance criteria
- [ ] Optional config (e.g. `[budget]` in ycc.toml): per-session cost/token cap and a per-loop-run cap; absent = unlimited (current behaviour).
- [ ] Status bar shows a visually distinct warning when a session passes ~80% of its budget.
- [ ] On breach in an attended session: a Confirm gate ("budget reached — continue?") like `switch_to_work`'s gate; declining stops gracefully.
- [ ] On breach in autonomous / loop sessions: graceful halt at the next safe checkpoint (current task finishes or is marked blocked), recorded in the loop digest and the event log — never a silent overrun, never a mid-write kill.
- [ ] Models with no pricing configured count tokens against a token cap only (no invented dollars, matching §20.4's degrade-gracefully rule).

## Plan

Goal: convert existing usage/cost telemetry into an enforced, optional spend guard: per-session caps enforced daemon-side at safe checkpoints, and a per-loop-run cap enforced by the TUI loop driver. Absent config = unlimited (today's behaviour).

1) Config (internal/config/config.go)
- New `Budget` struct + `Budget Budget `toml:"budget,omitempty"`` on Config:
  session_cost (float64 $), session_tokens (int64), loop_cost (float64 $), loop_tokens (int64). 0/unset = unlimited.
- validate(): all fields non-negative.
- `Registry.Budget() Budget` accessor (RLock, like MaxTokens). Read live at each check so runtime config edits apply.

2) Events (internal/event/event.go)
- New types: `BudgetWarning Type = "budget_warning"` (~80% crossed) and `BudgetExceeded Type = "budget_exceeded"` (cap crossed). Data: tokens, token_cap, cost, cost_cap, pct, and for exceeded an `action` field ("continue" when the user confirmed past the cap; "halt" when the session was told to wrap up). These make breaches durable in the event log (never a silent overrun) and let the TUI/status bar project them.

3) Session-side guard (new internal/session/budget.go + small hooks in session.go)
- Session gains budgetWarned / budgetBreached flags (guarded by s.mu).
- checkBudget(ctx) is invoked from Session.Checkpoint (the engine's safe checkpoint — top of turn / after tool results). It must not disturb pause/steer semantics: run it on the fast path and after a resume, merging any returned wrap-up message into the returned msgs slice.
- Computation: caps := s.reg.Budget(); if both session caps are 0 → no-op. Otherwise reduce the session's own log: usage.ReduceEvents(s.ID, s.log.Snapshot()) + usage.Aggregate(entries, s.reg, Options{}) → total tokens (Tokens.Total) and priced cost (res.Total.Cost). Unpriced models contribute no dollars, so they count only against the token cap (§20.4 degrade-gracefully — acceptance criterion 5). pct = max over the *configured* caps of spent/cap.
- pct >= 0.8 && !warned → emit budget_warning once; set warned.
- pct >= 1.0 && !breached:
  - Attended (s.inter.Level() != "autonomous"): ok, err := s.inter.Confirm(ctx, "Session budget reached (<tokens/cost vs caps>) — continue past the budget?"). Confirmed → emit budget_exceeded{action:"continue"}, set breached, continue normally (ask at most once per session). Declined → graceful halt path below.
  - Autonomous (incl. loop sessions) or declined confirm: emit budget_exceeded{action:"halt"} and inject a wrap-up instruction as a user-role message: "Session budget reached… stop taking on new work; bring the current task to the nearest safe stopping point — finish+commit it if essentially complete, otherwise mark it in_review/blocked (update_task) with a brief work-log note — then call finish." Set breached so it fires once.
- REPLAY CORRECTNESS: the injected instruction must survive reopen. Mirror the drainJobNotes pattern: emit the instructing event AS actor "user" (e.g. the budget_exceeded halt event carries the instruction text, or emit a user-actor event alongside) and teach engine/replay.go (ReplayHistory) to reconstruct it as a user message, exactly like job_notified — otherwise a reopened session's history would break user/assistant alternation.
- Reopen: seed warned/breached from the existing log (a budget_warning / budget_exceeded already present in the replayed events sets the flag) so the guard doesn't re-fire on a reopened session that already crossed the line.

4) RPC (proto/ycc/v1/ycc.proto → buf generate → internal/server)
- New `GetBudget(GetBudgetRequest) returns (GetBudgetResponse)` with session_cost (double), session_tokens (int64), loop_cost (double), loop_tokens (int64). Handler reads Manager → reg.Budget(). Check internal/server/auth.go for any per-RPC lists (read-only allowlists etc.) and register accordingly. Regenerate with buf (buf.gen.yaml at repo root).
- Manager.Budget() convenience passthrough.

5) TUI status bar warning (internal/tui/tui.go)
- Track budget state from events in the model_turn/event handler switch: budget_warning → store pct; budget_exceeded → exceeded flag. Reset wherever per-session state (usageByModel, ~line 2256) resets.
- statusBar(): new high-priority segment — warn style ("⚠ budget NN%") once warned, err style ("⚠ budget reached") once exceeded — visually distinct from the normal Σ readout (acceptance criterion 2).
- Add transcript row rendering for budget_warning/budget_exceeded in the transcript renderer(s) (mirror how session_error / log rows render) so the breach is visible in session history too.

6) TUI work-loop cap (internal/tui/tui.go loop driver)
- On loop start (both entry paths: menu tab-enter ~line 3158 and shift+tab mid-session ~line 3360) fetch GetBudget once and stash loop caps on loopRunState.
- Accumulate loop-wide spend: snapshotLoopSession already sums tokens per session; also compute the priced cost estimate at session close from usageByModel × pricing (m.sessionUsage already does this math) and accumulate on the run state.
- applyLoopDecision: before starting the next session, if loop caps are configured and cumulative tokens/cost >= cap → finish("loop stopped: budget reached (…)") so the current task's session completed and the halt is recorded in the batch digest outcome.
- If a budget_exceeded event is observed on a loop session, stop the loop at the next decision point with a distinct outcome ("loop stopped: session budget reached") — recorded in the digest; the event log record came from the daemon side.

7) Docs
- spec.md: add "### 20.6 Spend guard (budget caps)" documenting [budget] config, the 80% warning, the attended Confirm gate, the autonomous/loop graceful wrap-up halt, the unpriced-models token-cap-only rule, and the client-driven loop cap via GetBudget. Update the RPC list in §12 and docs/remote-api.md if they enumerate RPCs.

8) Tests (go build/vet/test per plans/build-and-test.md)
- config: [budget] round-trip + negative-value rejection + Registry.Budget.
- session: checkpoint guard tests (pattern after existing steer/session tests): warning emitted once at 80%; autonomous breach injects wrap-up + emits budget_exceeded once; attended breach raises Confirm — "no" halts, "yes" continues without re-asking; unpriced model + cost-only cap never invents dollars (no breach), token cap still enforced.
- engine/replay: user-role budget instruction reconstructed as a user message.
- server: GetBudget returns configured values.
- tui: statusBar shows the warn/exceeded segment; applyLoopDecision halts at the loop cap and on an observed session breach (unit-level, like existing digest tests).

### Starting points
- internal/config/config.go — Config/GC/Registry accessors as the pattern for Budget
- internal/session/session.go — Checkpoint (steer), drainJobNotes (user-actor event pattern), summarizeUsage (log→usage reduction pattern), newSession/Reopen
- internal/session/interaction.go — Confirm (attended yes/no gate), Level()
- internal/usage/usage.go — ReduceEvents/Aggregate give total tokens + priced cost; unpriced ⇒ cost 0
- internal/event/event.go — event Type constants; JobNotified precedent
- internal/engine/replay.go — how job_notified/user_input_delivered replay as user messages
- internal/tui/tui.go — statusBar() ~7714, usageByModel accumulation ~6616, per-session reset ~2256, loop driver: applyLoopDecision ~1161, loop entry ~3158/~3360, snapshotLoopSession ~1090, sessionUsage ~9512
- proto/ycc/v1/ycc.proto + internal/server/server.go (GetUsage handler ~670 as the pattern); buf generate with buf.gen.yaml
- plans/build-and-test.md — verification runbook

## Work log
- 2026-07-06 plan: Goal: convert existing usage/cost telemetry into an enforced, optional spend guard: per-session caps enforced daemon-side at safe checkpoints, and a per-loop-run cap enforced by the TUI loop driver. A
…[truncated]
- 2026-07-06 context hints: 9 recorded with plan
- 2026-07-06 context hints: internal/config/config.go — GC struct + Registry.GC() as the exact pattern for Budget; internal/session/session.go — Checkpoint (steer fast path), drainJobNotes (user-actor event + replay pattern)
…[truncated]
- 2026-07-06 implementer report: Implemented task 0137 — Spend guard: budget caps for sessions and the work loop. All acceptance criteria met; `go build ./... && go vet ./... && go test ./...` all pass.  ## What changed  **Config (
…[truncated]
- 2026-07-06 review tier: high-powered — reviewers: Claude
- 2026-07-06 review (Claude): accept — Task 0137 (spend guard: budget caps) is implemented completely and correctly. Config adds a validated `[budget]` block with session and loop caps (0 = unlimited, absent preserves prior behaviour) plus
…[truncated]
- 2026-07-06 decision: accept — commit: Spend guard: budget caps for sessions and the work loop ([budget] config, checkpoint enforcement, budget events, GetBudget RPC, TUI warning + loop cap) (task 0137)
- 2026-07-06 usage: 75,540 tok (in 404, out 75,136, cache_r 25,708,743, cache_w 408,198) · cost n/a (unpriced)
  implementer: 56,956 tok (in 314, out 56,642, cache_r 23,523,605, cache_w 208,974) · cost n/a (unpriced)
  reviewer:Claude: 9,458 tok (in 68, out 9,390, cache_r 870,093, cache_w 50,428) · cost n/a (unpriced)
  coordinator: 9,126 tok (in 22, out 9,104, cache_r 1,315,045, cache_w 148,796) · cost n/a (unpriced)
