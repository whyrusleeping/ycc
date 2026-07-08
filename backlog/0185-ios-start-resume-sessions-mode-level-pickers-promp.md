---
id: "0185"
title: 'iOS: start & resume sessions — mode/level pickers, prompt composer'
status: todo
priority: 3
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0181"
    - "0182"
spec_refs:
    - 9. Modes (the home menu)
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Start and resume sessions from the phone per `docs/design/ios-client.md` §6 phase 2 step 5.

## Description
- "New session" flow: mode picker from `ListModes` (pm/chat/work + preset descriptions), interaction level picker (interactive/judgement/autonomous), project picker (registered projects), multiline prompt composer → `StartSession`, then navigate directly into the live session view (Subscribe from seq 0).
- Resume: `ResumeSession` action on persisted session rows (re-opens on the existing log, idempotent if live); navigate into the live view on success.
- Sensible defaults (last-used mode/level/project remembered client-side).

## Acceptance criteria
- Starting a work/pm/chat session from the app lands in a live streaming view of that session.
- Resuming a persisted session continues the same event log (seq continuity visible).
- Errors (unknown project, daemon unreachable) surfaced cleanly.
- View-model logic under `swift test`.

## Work log
