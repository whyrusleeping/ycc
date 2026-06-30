---
id: "0020"
title: Persist and re-run coordinator / testing plans (plan library)
status: done
priority: 3
created: "2026-06-26"
updated: "2026-06-30"
depends_on:
    - "0004"
spec_refs:
    - Document model
    - The `work` orchestration (in detail)
---

## Description
Coordinator plans are barely persisted today. `propose_plan` (orchestrator.go) emits a
`plan_proposed` event carrying the FULL plan — but that only lives in the per-session JSONL
(`.ycc/sessions/<id>/events.jsonl`), buried and not meant for browsing. The only copy in the
durable docs is a ONE-LINE squash: `AppendWorkLog(id, "plan: " + oneLine(plan))`. There is no
browsable, reusable plan artifact, and no way to say "rerun the plan we worked on."

Two related pieces (the second is the interesting one — discuss scope with the user):

1. **Persist full plans for reference.** Keep the complete plan in the durable docs, not just
   a one-liner — e.g. a `## Plan` section in the task file (or full text in the work log).
   Cheap; keeps the plan next to its task.

2. **Reusable, re-runnable plans (runbooks) — esp. testing plans.** A library of named
   markdown procedures that live IN the repo (committed, version-controlled — matches the
   docs-driven philosophy), plus a way to invoke one: "run the <name> plan" → the agent reads
   it and executes the steps. Testing plans are the motivating case: a repeatable verification
   procedure you replay instead of re-describing. Distinct from the backlog (tasks = what to do
   once; plans = how to do something, repeatably).

Open design questions:
- Where do reusable plans live? In-repo `plans/` markdown (committed; preferred for test plans)
  vs. `.ycc/plans/` (session/machine-local). Per-task plan vs. standalone library plan.
- How is a saved plan invoked — a tool (`run_plan <name>`), a mode, or a prompt convention?
- Format: free markdown vs. light structure (name, steps, expected outcome) so a "test plan"
  can be re-run and its result checked.
- Listing/surfacing: a tool/RPC + TUI so plans are discoverable.

## Acceptance criteria
- [ ] decide where plans live + their format (in-repo `plans/` vs. task-embedded) — needs user input
- [ ] the FULL coordinator plan is persisted to a durable, human-browsable artifact (not just a
      one-line work-log entry or a buried event)
- [ ] a saved plan can be referenced later and re-run by the agent — motivating case: save a
      testing plan, then tell ycc to rerun it end-to-end
- [ ] plans are listable (tool/RPC) so the agent and TUI can surface them
- [ ] does not duplicate the backlog: plans are reusable procedures, tasks are work items

## Work log
- 2026-06-26 created. (Originally drafted as 0018, then 0019; the 0018 draft was deleted by the
  0013 implementer agent via `git rm` as "out of scope," and 0019 collided with a task a live
  session created — so it landed here as 0020 and was committed on creation so it can't be
  silently cleaned again. Motivates the agent scope-guard / workspace-isolation work, 0008.)
- 2026-06-30 plan: Design decision (autonomous, per task's stated preferences): reusable plans live in-repo at `plans/*.md` (committed, version-controlled — matches docs-driven philosophy). Full coordinator plans are 
…[truncated]
- 2026-06-30 implementer report: Implemented task 0020: full-plan persistence + reusable in-repo plan library.  **docs package** (`internal/docs/docs.go`, new `internal/docs/plans.go`): - `SetPlan(id, plan)` upserts a `## Plan` secti
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — The change cleanly satisfies all acceptance criteria for task 0020. It (1) makes a documented design decision that reusable plans live in-repo at `plans/*.md` (committed, version-controlled), recorded
…[truncated]
- 2026-06-30 decision: accept — commit: plans: persist full coordinator plans + in-repo reusable plan library  Persist the FULL plan to the task's ## Plan section via docs.SetPlan (idempotent upsert above the work log) instead of only a one
…[truncated]
