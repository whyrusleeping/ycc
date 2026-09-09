---
id: "0365"
title: Make session budget checks incremental instead of rescanning all events
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §5 Event log
    - §9.1 Unattended work loop
---

## Description
Remove growing per-checkpoint overhead from session budget enforcement, which previously copied and reduced the complete event history.

## Acceptance criteria
- Ordinary budget checks consume only new durable events, independent of prior transcript length.
- Coordinator and subagent usage count exactly once with existing disjoint token classes, pricing, and legacy decoding semantics.
- Reopen reconstructs equivalent totals and budget warning/breach state.
- Preserve fsync visibility and budget wrap-up behavior.
- Verify reference equivalence, repeated checkpoints, reopen, threshold crossing, and large-history performance.

## Outcome
Added an event-index tail cursor and compact per-model budget totals, repriced with the current registry. Reopen seeds totals once from its existing replay snapshot. Standard independent review accepted. Full Go suite and targeted event/session race tests passed; focused tests also passed on isolated HEAD plus this task's changes. A 20,000-event benchmark measured unchanged checkpoints at about 1.1 µs and 472 B versus 4.1 ms and 321,560 B for reference full reduction.

Commit: Make session budget checks incremental
