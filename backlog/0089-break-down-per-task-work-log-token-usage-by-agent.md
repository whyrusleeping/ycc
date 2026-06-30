---
id: "0089"
title: Break down per-task work-log token usage by agent role
status: todo
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on:
    - "0038"
spec_refs:
    - Token usage & cost accounting
---

## Description
## Description
The usage/cost line that `Session.summarizeUsage` (internal/session/session.go) appends to
a task's work log currently reports a single aggregate token count per task, e.g.:

`usage: 7,645 tok (in 30, out 7,615, cache_r 273,570, cache_w 19,750) · cost n/a`

We want this broken down by agent role so it's clear how much each agent type consumed —
e.g. coordinator used X, implementer used Y, and each reviewer (reviewer:claude,
reviewer:gpt, …) used Z respectively. The data already exists: `model_turn` events carry
the `actor` (which encodes role: `coordinator`, `implementer`, `reviewer:<name>` — see 0026)
and `usage.Aggregate` supports GroupBy, so this is primarily a reporting/formatting change
to the work-log summary.

## Context
- Built on 0029 (usage aggregation) and 0038 (per-task work-log summary for every task).
- `usage.ReduceEvents` / `usage.Aggregate` already produce role-attributable rows; the
  selection/formatting in `summarizeUsage` needs to emit a per-role breakdown.

## Acceptance criteria
- [ ] The work-log usage line for a task shows a per-agent-role token breakdown
      (coordinator / implementer / reviewer:<name> each), in addition to (or replacing)
      the single aggregate total.
- [ ] Reviewers are listed individually by name (e.g. reviewer:claude, reviewer:gpt).
- [ ] Roles that consumed zero tokens are omitted (or clearly shown as 0) — pick and keep
      it consistent.
- [ ] Cost-per-role is included where pricing is available, falling back gracefully when
      unpriced.
- [ ] Tests cover a multi-role session producing a breakdown line.

## Acceptance criteria

## Work log
