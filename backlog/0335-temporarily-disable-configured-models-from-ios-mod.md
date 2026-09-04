---
id: "0335"
title: Temporarily disable configured models from iOS model settings
status: done
priority: 2
created: "2026-08-15"
updated: "2026-08-15"
depends_on: []
spec_refs:
    - 7. Agent engine
---

## Description
Add a persisted enabled/disabled state for configured logical models and expose it as a small toggle on the iOS model settings page. Disabled models remain configured but are unavailable for new model selection/execution until re-enabled.

## Acceptance criteria
- A model can be disabled and re-enabled without deleting or re-entering its configuration.
- The iOS model editor exposes the state as a simple toggle.
- Disabled models are clearly identified in global settings and omitted from model/role selection for new work.
- Existing role configuration remains persisted, but attempts to start new inference explicitly with a disabled model fail clearly rather than silently using it.
- Protocol, daemon persistence, tests, generated clients, and durable design documentation stay in sync.

## Work log
