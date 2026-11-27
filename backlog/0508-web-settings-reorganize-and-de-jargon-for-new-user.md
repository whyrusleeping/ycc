---
id: "0508"
title: 'Web Settings: reorganize and de-jargon for new users'
status: done
priority: 2
created: "2026-11-09"
updated: "2026-11-27"
depends_on: []
spec_refs:
    - spec.md#18.2 Settings
---

## Description

The web Settings page is a long list of ten sections (Health, Accounts, Notifications, Default roles, Reasoning, Work, Review tiers, Models, Modes, Spend guard) with jargon and dense prose; a new user can't tell what matters or where to connect a model.

Acceptance:
- Sections are ordered and grouped by what a user is trying to do, starting with connecting models (accounts + models together), then which model does what, then everything else; diagnostics (health) is no longer the first thing.
- Each section opens with one plain sentence on what it controls; internal jargon (coordinator/implementer, tiers, modes) is explained or renamed in the UI.
- No setting is lost; existing behavior and RPCs unchanged.
- Verified visually in light and dark mode against a scratch daemon.

## Outcome

e0602905 reorganized Settings around models, roles, reviews, notifications, spending and health while preserving the old section links. The light/dark scratch-daemon checks, current web suite and owner's acceptance of the current layout (including Status without an intro sentence) complete review.

Commit: Close the web Settings organization review
