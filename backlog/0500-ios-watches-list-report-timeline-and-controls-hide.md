---
id: "0500"
title: 'iOS: watches list, report timeline and controls; hide watch runs from Recent'
status: done
priority: 2
created: "2026-11-02"
updated: "2026-11-10"
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

## Outcome

iOS Watches screens reached from a project's overflow menu: list with latest status, next run/paused/expired and parked-question indicator; detail with waiting runs (answer opens the run), controls, definition, notes, standing decisions, report timeline with run-session drill-ins and older-report paging. SessionListModel hides origin=watch rows from lists and drawer badges unless waiting or flagged. swift test 572/0 on the Mac, app builds, simulator walk-through against a scratch daemon (incl. answering a parked run). Create/edit left to web/agents.

Commit: iOS: Watches list and detail (status, schedule, parked questions, report timeline, pause/resume/run now/delete); watch runs kept out of session lists unless asking or flagged (0500)
