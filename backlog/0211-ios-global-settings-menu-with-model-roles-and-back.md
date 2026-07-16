---
id: "0211"
title: 'iOS: global settings menu with model roles and backend management'
status: done
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Phase 3 — TUI parity
    - spec.md#18.2 Settings overlay (esc — "video-game style")
    - spec.md#13. Backends & model registry
---

## Description
Add a first-class Settings destination to the authenticated iOS app. It should expose the daemon-wide model configuration that is currently only practical to manage from the TUI, while retaining the existing live-session settings sheet for session-specific controls.

## Acceptance criteria
- [ ] The iOS home screen has an obvious Settings affordance.
- [ ] Global Settings lists the configured logical models and shows/edits the default coordinator, implementer, and multi-select reviewer assignments.
- [ ] Global Settings shows/edits the default per-role thinking levels.
- [ ] Users can add, edit, duplicate, and remove logical model backends using the existing model-registry RPCs.
- [ ] The model form covers backend, auth mechanism, endpoint, model id, key environment variable, reasoning settings, and optional pricing.
- [ ] Backend model discovery is available from the model form and handles curated fallback notes.
- [ ] Unauthorized and RPC failures are surfaced consistently with the rest of the app.
- [ ] YccKit model logic has focused unit tests, and Swift tests/build pass.
- [ ] iOS design documentation describes the global settings surface and distinguishes it from per-session settings.

## Work log
