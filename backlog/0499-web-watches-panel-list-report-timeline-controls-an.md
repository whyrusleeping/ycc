---
id: "0499"
title: 'Web: watches panel (list, report timeline, controls) and hide watch runs from recent sessions'
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
Client surface for watches (docs/design/watches.md, spec §9.2). RPCs: ListWatches, GetWatch (with reports), CreateWatch, UpdateWatch, DeleteWatch, SetWatchPaused, RunWatchNow.

## Acceptance criteria
- A project's watches are listed with latest status (ok/warn/alert/failed), next due time, paused/expired state, and a parked-question indicator.
- A watch's detail shows its definition, notes, standing decisions, and report timeline (newest first, including skipped/probe/failed entries with linked run sessions).
- Pause/resume, run now, edit, and delete work from the panel; create is available with the same fields as the RPC.
- Sessions with origin `watch` are kept out of the main recent-session list and reachable from their watch.
- Rebuild and commit internal/web/dist.

## Work log
