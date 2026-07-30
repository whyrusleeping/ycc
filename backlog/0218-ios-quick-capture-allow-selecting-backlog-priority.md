---
id: "0218"
title: 'iOS quick capture: allow selecting backlog priority'
status: done
priority: 2
created: "2026-07-16"
updated: "2026-07-16"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Phase 2 — start work, backlog
---

## Description
The iOS “New backlog item” flow always creates tasks at the default P3 and offers no priority control. Add a priority picker to the capture UI and propagate the selected value through YccKit to CreateTask.

## Acceptance criteria
- The new-backlog-item UI lets the user select P1 through P5.
- The selected priority is sent in the CreateTask request.
- P3 remains the initial default.
- Headless YccKit tests cover request propagation, and the iOS simulator build passes.

## Work log
