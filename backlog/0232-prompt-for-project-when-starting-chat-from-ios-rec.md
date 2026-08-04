---
id: "0232"
title: Prompt for project when starting chat from iOS recent sessions
status: done
priority: 2
created: "2026-08-04"
updated: "2026-08-04"
depends_on: []
spec_refs:
    - docs/design/ios-client.md
---

## Description
Update the iOS Recent Sessions new-chat action so it first presents a project picker, then starts the new chat in the selected project.

## Acceptance criteria
- [x] Tapping New Chat from Recent Sessions presents available projects rather than immediately starting in an implicit project.
- [x] Selecting a project starts the normal new-chat flow scoped to that project.
- [x] The user can cancel without starting a session.
- [x] Relevant iOS tests cover the project-selection behavior.

## Work log
- 2026-08-04: Added a cancellable Recent Sessions project chooser, routed its selection into the existing new-session composer, retained direct project-scoped starts, and added headless selection-policy tests. `git diff --check` passed; Swift/Xcode tests were unavailable on the Linux host (`swift: not found`).
