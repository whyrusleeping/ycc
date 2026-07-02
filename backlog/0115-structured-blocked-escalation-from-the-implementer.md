---
id: "0115"
title: Structured "blocked" escalation from the implementer to the coordinator
status: done
priority: 4
created: "2026-07-01"
updated: "2026-07-02"
depends_on: []
spec_refs:
    - 10. The `work` orchestration (in detail)
    - 11. Interaction levels
---

## Description
## Description
The implementer has no structured way to say "I'm blocked on a decision" — it can only weave it into its finish report prose, and the coordinator may or may not notice and escalate. Give the implementer a structured escape: either a `blocked(reason)` variant/field on `finish_implementation`, or a documented report convention the coordinator prompt explicitly checks for. The coordinator should then ask_user (level permitting) or mark the task blocked with the reason, rather than pushing the implementer to guess.

## Acceptance criteria
- [ ] Implementer can signal blocked-with-reason distinctly from a normal finish
- [ ] Coordinator prompt instructs how to handle it (ask_user / update_task blocked, per interaction level)
- [ ] The reason lands in the task work log
- [ ] Revise loop unaffected for normal finishes

## Acceptance criteria

## Plan

Give the implementer a structured, first-class "blocked" escape distinct from a normal finish, and teach the coordinator how to react.

1. internal/tools (worker.go, tools.go):
   - Add `Blocked bool` field to the `Control` struct (out-of-band signal carried via ToolResult.Structured), documented.
   - Add a new control tool `report_blocked(reason)` (function `ReportBlocked()`), added to the `Worker()` tool set alongside `finish`. Description: call INSTEAD of finish when you cannot responsibly proceed without a decision that isn't yours to make (unresolved design choice, conflicting requirements, hard-to-reverse call); state the specific decision needed and why. Requires non-empty `reason` (errResult when missing). Returns Control{Stop: true, Blocked: true, Report: reason}.
   - Reviewers/coordinator do NOT get this tool (reviewer uses submit_review; coordinator has update_task blocked).

2. internal/engine/loop.go:
   - Add `Blocked bool` to `Result` (documented). When the loop stops on a control tool, propagate `ctrl.Blocked` into the Result.

3. internal/orchestrator/orchestrator.go:
   - In `implementerOutcome`, handle `res.Blocked` FIRST (before the no-progress guard, which must not fire for a legitimate blocked report): append a work-log line `<label>: BLOCKED — <reason>` (oneLine), and return an OK (not error) tool result that is unmistakably distinct: a header like "IMPLEMENTER BLOCKED (not finished)", the reason, the staged diff (partial work may exist), and short guidance: do not push it to guess — decide yourself if it's an ordinary judgement call, ask_user as the interaction level permits, and relay the answer via send_to_implementer (it keeps its context); if no answer is available, update_task 'blocked' with the reason.
   - In spawnImplementer/sendToImplementer, include `"blocked": true` in the SubagentFinished event payload when res.Blocked (small observability nicety).

4. internal/orchestrator/prompts.go:
   - implementerSystem: add a short paragraph telling the implementer to call report_blocked(reason) instead of guessing when it hits a decision that isn't its to make — not for ordinary implementation judgement calls it can reasonably resolve itself.
   - coordinatorSystem: add an "IMPLEMENTER BLOCKED" paragraph: when spawn_implementer/send_to_implementer returns a BLOCKED outcome, don't push the implementer to guess; resolve ordinary judgement calls yourself and send_to_implementer with the answer; when the user is genuinely needed, ask_user per interaction level and relay the answer; when no human answer is available (autonomous), update_task 'blocked' with the reason (it's already in the work log) and move on/finish.

5. spec.md:
   - §8 Tools, worker tools list: add `report_blocked(reason)` — the structured blocked escalation control tool.
   - §10 The `work` orchestration: brief note that step 4 can end in a structured BLOCKED outcome and how the coordinator handles it (resolve / ask_user / mark task blocked), reason recorded in the work log.

6. Tests:
   - internal/tools/worker_test.go: dispatch `report_blocked` → Control has Stop && Blocked and Report==reason; missing reason → error result.
   - internal/engine: loop test that a control tool with Blocked=true yields Result.Blocked=true (or extend an existing control test).
   - internal/orchestrator: test (patterned on the existing fake-turner harness in orchestrator_test.go / revise_test.go) that a blocked implementer run produces a tool result containing "BLOCKED" + the reason, writes the work-log line, and does NOT trip the no-progress error; and that normal finishes are unchanged (existing tests keep passing).

Verify with `go build ./... && go test ./...`.

### Starting points
- internal/tools/worker.go — Finish() control tool; Worker() tool set
- internal/tools/tools.go — Control struct + ControlOf
- internal/engine/loop.go ~line 499-517 — control-stop handling returning Result; Result struct ~line 193
- internal/orchestrator/orchestrator.go — implementerOutcome (no-progress guard), spawnImplementer, sendToImplementer
- internal/orchestrator/prompts.go — implementerSystem, coordinatorSystem consts
- internal/tools/reviewer.go submitReview — example of a structured control tool
- spec.md §8 (worker tools list, ~line 326) and §10 (work orchestration, ~line 432)
- existing test harnesses: internal/tools/worker_test.go dispatch(), internal/orchestrator/orchestrator_test.go + revise_test.go fake turner

## Work log
- 2026-07-02 plan: Give the implementer a structured, first-class "blocked" escape distinct from a normal finish, and teach the coordinator how to react.  1. internal/tools (worker.go, tools.go):    - Add `Blocked bool`
…[truncated]
- 2026-07-02 context hints: 8 recorded with plan
- 2026-07-02 context hints: internal/tools/worker.go — Finish() control tool at bottom; Worker() tool set; internal/tools/tools.go — Control struct + ControlOf helper; internal/engine/loop.go — Result struct ~line 193; ctr
…[truncated]
- 2026-07-02 implementer report: Implemented Task 0115: a structured "blocked" escalation from the implementer to the coordinator, distinct from a normal finish.  Changes: - internal/tools/tools.go: added documented `Blocked bool` fi
…[truncated]
- 2026-07-02 review tier: single-opus — reviewers: Claude
- 2026-07-02 review (Claude): accept — The change implements a structured "blocked" escalation exactly as planned. A new `report_blocked(reason)` control tool is added to the worker tool set only (Blocked flag on Control), propagated throu
…[truncated]
- 2026-07-02 decision: accept — commit: orchestrator: structured blocked escalation from implementer via report_blocked (task 0115)
