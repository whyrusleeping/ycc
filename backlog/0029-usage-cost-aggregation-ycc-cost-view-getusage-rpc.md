---
id: "0029"
title: Usage/cost aggregation + ycc cost view, GetUsage RPC, work-log summary
status: todo
priority: 2
created: "2026-06-26"
updated: "2026-06-26"
depends_on:
    - "0026"
    - "0027"
    - "0028"
spec_refs:
    - Token usage & cost accounting
    - RPC protocol (Connect)
    - Backlog — structured items, markdown-rendered
---

## Description
Aggregate captured usage across sessions and surface the "detailed cost breakdown by
backlog task over time" (spec §20.3, §20.5). Joins per-turn usage (0026) + task focus
(0027) + pricing (0028).

## Context
- Sessions persist at `<workspace>/.ycc/sessions/<id>/events.jsonl`; raw events are the
  source of truth, so the breakdown is recomputed (never a separate ledger).
- The event projection should already total usage by model and focused task per session.

## Acceptance criteria
- [ ] New `internal/usage` aggregator: scan a workspace's `.ycc/sessions/*/events.jsonl`,
      reduce each, and produce a breakdown grouped by task × model × time (per-day
      buckets), plus per-session and project totals, with token counts and (when priced)
      dollar costs; unpriced models show tokens only.
- [ ] `ycc cost` CLI command renders the breakdown (by task by default; flags for grouping
      by model/session/day and a date range). Readable table output.
- [ ] `GetUsage` RPC added to `SessionService` (spec §12) returning the structured
      breakdown so non-CLI clients (TUI/phone) can render it.
- [ ] On `work` completion, append a one-line usage/cost summary to the task's work log
      (§6.2) so per-task cost accrues in the backlog across sessions.
- [ ] Tests: aggregation over fixture event logs (multiple sessions/tasks/models),
      grouping, and cost vs. unpriced rendering.

## Acceptance criteria

## Work log
