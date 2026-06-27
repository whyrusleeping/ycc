---
id: "0035"
title: TUI session browser (list, transcript, reopen) + shared list+detail modal
status: todo
priority: 3
created: "2026-06-27"
updated: "2026-06-27"
depends_on:
    - "0033"
    - "0034"
    - "0031"
spec_refs: []
---

## Description
Add a TUI **session browser**: a modal list+detail view (opened from the home menu /
settings overlay, like the backlog browser §18.5) that lists a project's sessions
(live + persisted, most-recent first), drills into a read-only replayed transcript, and
offers a **Reopen** action to re-enter a session. Also factor the modal list+detail
pattern into a shared component reused by the backlog browser and the future cost view.

## Context
- Data comes from `ListSessionHistory` (task 0033); Reopen calls `ResumeSession` (task 0034)
  then Subscribes to the now-live session, reusing the existing live session view.
- The TUI already has the settings overlay (spec §18.2) and the backlog browser
  (task 0031) as modal precedents in `internal/tui/`. The transcript should render with the
  same event components the live session view uses so reasoning/tool-calls look identical.
- Spec §18.6 (session browser + shared browser surface) and §20.5 (cost view shares it).

## Acceptance criteria
- [ ] A reusable list+detail modal component in `internal/tui` (generic navigable list with
      a drill-in detail pane, Esc to dismiss), and the backlog browser refactored onto it
      (no behavior change) to prove the shared surface.
- [ ] Session browser modal: lists session summary rows (id/short-title, mode, status,
      last-activity, focused task; tokens/cost when available); navigable; selecting a row
      shows a read-only transcript rendered via the shared event components.
- [ ] **Reopen** action on a selected session: calls `ResumeSession`, then enters the live
      session view (Subscribe + input) for it.
- [ ] A small "browse" menu entry (home menu / overlay) routes to backlog / sessions
      (and is ready to add cost once task 0029 lands).
- [ ] Tests where feasible (model update/navigation logic, row rendering); manual-path notes
      for the interactive bits.

## Acceptance criteria

## Work log
