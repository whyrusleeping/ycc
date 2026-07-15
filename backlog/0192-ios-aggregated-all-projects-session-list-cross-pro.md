---
id: "0192"
title: 'iOS: aggregated "All projects" session list (cross-project home view)'
status: proposed
priority: 3
created: "2026-07-09"
updated: "2026-07-09"
depends_on: []
spec_refs:
    - Daemon modes & project registry
---

## Description
With a persistent multi-project daemon, the iOS session list defaults to `project=""` (the daemon default workspace) and the user must flip the project filter to see other projects one at a time. For the "one daemon, control everything" flow, an aggregated view would surface needs-answer sessions across ALL registered projects at once.

Options: client-side fan-out (N × ListSessionHistory, merge + tag rows with project) or a daemon-side `project="*"` / `all_projects` flag on ListSessionHistoryRequest that aggregates and stamps `SessionSummary.project`.

## Acceptance criteria
- The session list can show sessions from every registered project, most-recent-first, with needs-answer pinned across projects.
- Each row indicates which project it belongs to when the aggregate view is active.
- Default selection decided (aggregate vs current default-workspace behaviour) and documented.

## Work log
