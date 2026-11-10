---
id: "0499"
title: 'Web: watches panel (list, report timeline, controls) and hide watch runs from recent sessions'
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

Client surface for watches (docs/design/watches.md, spec §9.2). RPCs: ListWatches, GetWatch (with reports), CreateWatch, UpdateWatch, DeleteWatch, SetWatchPaused, RunWatchNow.

## Acceptance criteria

- A project's watches are listed with latest status (ok/warn/alert/failed), next due time, paused/expired state, and a parked-question indicator.
- A watch's detail shows its definition, notes, standing decisions, and report timeline (newest first, including skipped/probe/failed entries with linked run sessions).
- Pause/resume, run now, edit, and delete work from the panel; create is available with the same fields as the RPC.
- Sessions with origin `watch` are kept out of the main recent-session list and reachable from their watch.
- Rebuild and commit internal/web/dist.

## Outcome

Web Watches surface at /p/<project>/watches[/<name>] (sidebar eye icon, unscoped /watches asks for a project). List: latest status (ok/warn/alert/failed/skipped), next due/running/paused/expired, waiting-question link, sorted attention-first. Detail: definition, notes, standing decisions, newest-first report timeline with probe/skipped/failed entries, actions+undo and run-session links, "Show older". Controls: run now, pause/resume (expired -> Extend…), edit, confirmed delete; create dialog with every WatchDefinition field. Origin `watch` sessions stay out of the inbox unless waiting/flagged. Unit + editor regression tests (435 pass), typecheck, Playwright flow (create/pause/resume/run/edit/delete) and light/dark screenshots against the sidebar-v2 harness; dist rebuilt. Review: sol accepted round 2 (fixed polled-watch edit loss).

Commit: Web: Watches panel (list, detail with report timeline, run now/pause/resume/edit/delete, create); watch runs kept out of the inbox (0499)
