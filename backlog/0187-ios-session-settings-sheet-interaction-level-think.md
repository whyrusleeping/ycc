---
id: "0187"
title: 'iOS: session settings sheet — interaction level, thinking, role/model config'
status: todo
priority: 4
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0182"
spec_refs:
    - "18.2"
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Phone analog of the TUI settings overlay (spec §18.2) per `docs/design/ios-client.md` §6 phase 3 step 8.

## Description
- A per-session settings sheet on the session view: interaction level (`SetInteractionLevel`), thinking/effort (`SetThinking`), per-role model bindings (`SetRoleConfig`, populated from `ListModels`).
- Read current values where the API exposes them (e.g. GetModelConfig / session events) so the sheet reflects reality, not defaults.
- Changes apply to the live session and are reflected in subsequent events.

## Acceptance criteria
- Each setting round-trips against a live daemon and visibly affects the session (level change event, model change on next turn).
- Invalid combinations surface the daemon's error cleanly.
- View-model logic under `swift test`.

## Work log
