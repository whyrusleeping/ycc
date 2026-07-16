---
id: "0214"
title: 'iOS: reference focused backlog tasks in session names'
status: done
priority: 3
created: "2026-07-16"
updated: "2026-07-16"
depends_on: []
spec_refs:
    - Session & event log
    - Attribution to a backlog task (session → task focus)
---

## Description
When an agent establishes a backlog-task focus, make the iOS session list name visibly reference that task using the `focus_tasks` already returned by `ListSessionHistory`.

## Acceptance criteria
- iOS session rows prefix their normal derived/fallback name with the focused task id(s), matching the session-browser contract in spec §20.2.
- Sessions without task focus keep their existing names.
- Multiple distinct focused tasks render in their API order without duplication or blank ids.
- YccKit unit tests cover focused and unfocused session names.

## Work log
- Updated the iOS session-list title projection to prefix focused task ids (for example, `[0214] Implement the widget`) while preserving the existing title fallback.
- Normalized focused ids defensively by trimming, removing blanks, and deduplicating in API order; added focused, unfocused, multiple-task, and fallback test coverage.
- `git diff --check` passes. YccKit tests could not run in this environment because `swift` is not installed (`swift: not found`).
