---
id: "0327"
title: Enforce the delegated-work mutation boundary
status: proposed
priority: 2
created: "2026-08-12"
updated: "2026-08-12"
depends_on: []
spec_refs:
    - §8 Tools and access policy
    - §10 Work orchestration
---

## Description
In delegated work sessions, keep the coordinator focused on task selection, evidence, review decisions, and handoffs instead of allowing it to become a second long-lived implementer. Production-file mutations should go through the retained/fresh implementer; if the coordinator must take over, make that an explicit recorded strategy transition with single-writer checks rather than an accumulation of ad-hoc Edit calls in coordinator history. Backlog/status operations and read-only inspection remain coordinator-owned.

Acceptance criteria:
- Delegate-mode coordinator cannot silently perform a sequence of production Write/Edit mutations; it must use the implementer or an explicit takeover transition.
- A takeover records its reason, ends/replaces the implementer safely, and changes the session's effective implementation strategy visibly.
- Tiny metadata-only backlog updates remain possible without creating an implementer round.
- Direct-mode behavior is unchanged.
- Tests cover ordinary delegation, fresh implementer recovery, explicit takeover, and single-writer enforcement.

## Acceptance criteria

## Work log
