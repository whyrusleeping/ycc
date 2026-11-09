---
id: "0508"
title: 'Web Settings: reorganize and de-jargon for new users'
status: in_review
priority: 2
created: "2026-11-09"
updated: "2026-11-09"
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

## Acceptance criteria

## Work log

- 2026-11-09: Implemented. Sections now: Models (subscriptions + model list with per-model health status, pricing inline), Who does what (main agent / reviewers / implementer, each with model, reasoning select, and the work-implementation choice), Code review (plain intro, inline default-tier Apply, user-facing built-in tier descriptions), Notifications (details collapsed), Spending limits, Status (health, moved last). Removed the read-only Session modes reference (shown on New session). Old #accounts/#work/#thinking/#modes links still land. Health banner says "not set up" instead of "failing" for absent credentials. Verified light/dark screenshots against a scratch daemon; reviewed by opus with no regressions found. Uncommitted.
