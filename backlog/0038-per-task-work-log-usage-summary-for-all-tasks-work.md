---
id: "0038"
title: Per-task work-log usage summary for all tasks worked in a session (not just current focus)
status: done
priority: 3
created: "2026-06-27"
updated: "2026-06-29"
depends_on:
    - "0029"
spec_refs:
    - Token usage & cost accounting
    - Backlog — structured items, markdown-rendered
---

## Description
## Description
Follow-on from 0029. `Session.summarizeUsage` (internal/session/session.go) appends a
usage/cost work-log line only for the session's *currently-focused* task at each idle.
A session that focuses task A, then switches to task B and goes idle, never records a
work-log usage line for task A. The `usageSummarized` map already anticipates multiple
tasks, but only `event.Reduce(events).FocusTask` is inspected.

## Context
- `usage.ReduceEvents` / `usage.Aggregate(GroupBy: task)` already produce per-task rows,
  so the data is available; only the selection logic needs to broaden.
- Note the existing per-task line captures cumulative session usage at first idle and is
  never updated; consider whether to refresh/replace the line as more usage accrues, or
  keep the one-shot-per-task behavior (the recomputed `ycc cost` view stays accurate
  regardless).

## Acceptance criteria
- [ ] On work-mode idle, append a usage/cost work-log line for every task that accrued
      usage in the session and hasn't been summarized yet (not just the current focus).
- [ ] Remains idempotent per task per session (no duplicate lines for an unchanged task).
- [ ] Decide and document the cumulative-vs-snapshot behavior for a task worked across
      multiple idle cycles.
- [ ] Tests cover a multi-task session producing a line per task.

## Acceptance criteria

## Work log
- 2026-06-29 plan: Broaden `Session.summarizeUsage` (internal/session/session.go) to record a usage/cost work-log line for every task that accrued usage in the session, not just the currently-focused task.  Changes: 1. 
…[truncated]
- 2026-06-29 implementer report: Broadened `Session.summarizeUsage` (internal/session/session.go) to record a usage/cost work-log line for every task that accrued usage in the session, not just the currently-focused task.  Changes: -
…[truncated]
- 2026-06-29 review tier: simple (coordinator self-review)
- 2026-06-29 decision: accept — commit 19d636e: session: summarize usage to work log for every task worked in a session [0038]
