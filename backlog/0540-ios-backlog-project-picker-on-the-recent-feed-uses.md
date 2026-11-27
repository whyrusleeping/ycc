---
id: "0540"
title: 'iOS: backlog project picker on the Recent feed uses the new-chat project sheet'
status: done
priority: 3
created: "2026-11-10"
updated: "2026-11-27"
depends_on: []
spec_refs: []
---

## Description

Tapping Backlog on the unscoped Recent feed used a plain confirmationDialog of project names. Reuse the new-chat project sheet (generalized as `ProjectChoiceSheet`: recency-ordered rows with branch/sync badge, last-active age, activity counts; preselected project; bottom action button).

Acceptance:
- Backlog from Recent (>1 project) opens the sheet titled "Backlog" with "Open backlog for <project>"; the last-viewed project is preselected (else most recently active).
- Choosing pushes that project's backlog; Cancel dismisses.
- New chat sheet unchanged in behavior.
- Builds on the Mac simulator.

## Outcome

1457c780 uses the project choice sheet for the unscoped Recent backlog action and built on the Mac simulator. The owner confirmed the backlog picker works on a recent physical-device build.

Commit: Close the Recent backlog project picker review
