---
id: "0500"
title: 'iOS: watches list, report timeline and controls; hide watch runs from Recent'
status: todo
priority: 2
created: "2026-11-02"
updated: "2026-11-02"
depends_on:
    - "0498"
spec_refs:
    - spec.md#9.2 Watches
    - docs/design/watches.md
---

## Description
Client surface for watches (docs/design/watches.md, spec §9.2) on iOS, using the regenerated YccProto RPCs (ListWatches, GetWatch, CreateWatch, UpdateWatch, DeleteWatch, SetWatchPaused, RunWatchNow).

## Acceptance criteria
- A project's watches are listed with latest status, next due time, paused/expired state, and a parked-question indicator.
- Watch detail shows definition, notes, standing decisions, and the report timeline with links to run sessions; answering a parked run's question works from there.
- Pause/resume, run now, and delete are available.
- Sessions with origin `watch` are kept out of Recent and reachable from their watch.

## Work log
