---
id: "0320"
title: 'Session analytics: truthful token classes, context growth, and round telemetry'
status: proposed
priority: 2
created: "2026-08-12"
updated: "2026-08-12"
depends_on: []
spec_refs:
    - §5 Event log
    - §7.3 Subagents and asynchronous jobs
    - §13 Models, credentials, and review tiers
---

## Description
Build first-class, read-only analysis over the event logs so harness changes can be evaluated from evidence rather than anecdote. Correct the provider-neutral usage contract so Anthropic cache reads/writes are not omitted from the displayed/aggregated effective total, while retaining the raw provider total if useful. Surface per-session and per-agent elapsed time, fresh input/output/cache classes, context high-water, tool/result volume, tool errors, implementation/review rounds, context mode, terminal outcome, and task/commit attribution; provide a CLI export or stable RPC projection suitable for comparing cohorts before and after harness changes.

Acceptance criteria:
- Anthropic and OpenAI usage fields have an explicitly documented, internally consistent contract; aggregate totals do not undercount Anthropic cache classes.
- A command or API can summarize a selected session cohort by project/date/mode/task/model and report medians/tails, not just raw event counts.
- Per-agent round/context-mode/context-high-water and tool-result-volume data are queryable without reparsing JSONL ad hoc.
- Existing logs remain readable, with any legacy ambiguity called out rather than silently reinterpreted.
- Focused tests cover OpenAI subset-cache accounting, Anthropic separate-cache accounting, and cohort aggregation.

## Acceptance criteria

## Work log
