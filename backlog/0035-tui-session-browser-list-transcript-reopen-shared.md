---
id: "0035"
title: TUI session browser (list, transcript, reopen) + shared list+detail modal
status: done
priority: 3
created: "2026-06-27"
updated: "2026-06-30"
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
- 2026-06-29: a PARTIAL implementation landed in commit 14f6b76 (bundled with task 0011
  per maintainer decision): a previous-sessions screen (`stateHistory`, `historyView`,
  `updateHistory`, `fetchHistory`/`reopenSession`, opened with `ctrl+r` from the menu) that
  lists persisted sessions and reopens one via `ResumeSession`, with two tests. STILL TODO:
  the reusable list+detail modal component + backlog-browser refactor, the read-only
  transcript drill-in, and the unified "browse" menu entry. Build on the committed
  `ctrl+r` history screen rather than re-implementing it.
- 2026-06-30 plan: Build the TUI session browser + shared list+detail modal, in parts:  1. Shared list+detail modal component (internal/tui): a lightweight reusable struct (`browser`/`listDetail`) owning navigable list 
…[truncated]
- 2026-06-30 implementer report: Implemented task 0035: TUI session browser (list, read-only transcript, reopen) + a shared list+detail modal component, plus the supporting transcript RPC.  ## What changed  **Shared list+detail modal
…[truncated]
- 2026-06-30 review tier: high-powered — reviewers: Claude
- 2026-06-30 review (Claude): accept — The change satisfies the task's acceptance criteria. It adds a GetSessionTranscript read RPC (proto/server/session/event layers), a session browser that lists live+persisted sessions, drills into a re
…[truncated]
- 2026-06-30 revision: Addressed the review finding that the shared `browser` component's navigation was effectively dead code (only exercised by TestBrowserNav while the update handlers duplicated cursor arithmetic inline)
…[truncated]
- 2026-06-30 review (Claude): accept — The revision resolves the main finding from my prior review. Cursor navigation arithmetic is now centralized in navUp/navDown/clampCursor and reused by both the shared browser component methods and ev
…[truncated]
- 2026-06-30 decision: accept — commit: TUI session browser + shared list+detail browser surface (task 0035)  Add a session browser to the TUI: a navigable list of live + persisted sessions that drills into a read-only replayed transcript (
…[truncated]
- 2026-06-30 usage: 69,470 tok (in 274, out 69,196, cache_r 12,173,422, cache_w 284,412) · cost n/a (unpriced)
