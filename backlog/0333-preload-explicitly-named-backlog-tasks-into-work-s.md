---
id: "0333"
title: Preload explicitly named backlog tasks into work sessions
status: done
priority: 2
created: "2026-08-14"
updated: "2026-08-14"
depends_on: []
spec_refs:
    - §7 Agent engine
    - §6.2 Backlog
---

## Description
When a work-session opening prompt names a specific backlog task, avoid spending initial model turns on the predictable `list_backlog` then `get_task` sequence. Resolve the explicit task deterministically and seed synthetic backlog tool exchanges before the first coordinator turn, while preserving event-log/replay validity and falling back to the normal workflow when no unambiguous valid task is named.

## Acceptance criteria
- A work session whose opening prompt unambiguously names an existing task starts with synthetic backlog context sufficient to skip the routine list/get round trips.
- Synthetic calls/results are represented consistently in model history and durable events and survive replay.
- Ambiguous, absent, or invalid task references fall back safely without inventing context.
- Tests cover recognition, preloading, events/history, and fallback behavior.

## Work log
