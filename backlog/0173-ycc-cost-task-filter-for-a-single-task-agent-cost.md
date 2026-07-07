---
id: "0173"
title: 'ycc cost: --task filter for a single-task agent/cost view'
status: todo
priority: 4
created: "2026-07-07"
updated: "2026-07-07"
depends_on: []
spec_refs: []
---

## Description
`ycc cost --by task,agent` already produces the per-task per-agent breakdown, but answering "what did task 0093 cost, by agent?" requires scanning the full table. Add a `--task <id>` filter to the cost command (and GetUsage RPC) that restricts entries to one task before aggregation, so `ycc cost --task 0093 --by agent` prints just that task's agent breakdown plus its total.

Acceptance criteria:
- `ycc cost --task 0093 --by agent` shows only rows attributed to task 0093, with a TOTAL row for that task.
- Filter composes with `--by`, `--since`, `--until`.
- Unknown/empty task id behaves sensibly (empty table, no error).
- GetUsage RPC gains the corresponding field; daemon applies the filter server-side.

## Acceptance criteria

## Work log
