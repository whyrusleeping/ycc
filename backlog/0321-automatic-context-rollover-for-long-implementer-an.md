---
id: "0321"
title: Automatic context rollover for long implementer and reviewer loops
status: proposed
priority: 1
created: "2026-08-12"
updated: "2026-08-12"
depends_on: []
spec_refs:
    - §7.3 Subagents and asynchronous jobs
    - §10 Work orchestration
---

## Description
Prevent retained subagent history from growing until a provider rejects the request. Add an engine/orchestrator context budget based on the selected model's known or configured context window. Before a revision/re-review (and, if feasible, at safe tool-loop checkpoints), replace a near-limit loop with a fresh loop seeded from the full task, current tree/bounded diff, unresolved accepted findings, current approach, and required verification. Preserve model, role/focus, reasoning, access policy, round, and single-writer semantics. Emit the rollover reason and old/new context estimates.

Acceptance criteria:
- Each logical model can expose/configure a context-window budget distinct from the per-turn output cap.
- Retained implementer/reviewer continuation rolls over before exceeding a configurable safe fraction; an actual context-length error triggers one fresh recovery when safe rather than merely advising the coordinator.
- Fresh handoff contains unresolved blocker/major findings and current verification state without replaying old tool logs.
- Lifecycle events distinguish coordinator-selected fresh context, automatic pressure rollover, and error recovery.
- Tests cover preflight rollover, context-length recovery, same-slot reviewer recreation, and no duplicate mutation or review submission.

## Acceptance criteria

## Work log
