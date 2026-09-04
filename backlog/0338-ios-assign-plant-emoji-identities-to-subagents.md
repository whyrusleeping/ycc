---
id: "0338"
title: 'iOS: assign plant emoji identities to subagents'
status: in_review
priority: 3
created: "2026-08-16"
updated: "2026-08-16"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Navigation and interaction
---

## Description
Give each non-coordinator agent in the native iOS session transcript a stable plant emoji identity. Assign from a fixed palette in first-seen event order, reuse the assignment across durable and transient rows for that actor, and prefix every actor-owned log row without replacing the textual actor label needed for accessibility and disambiguation.

## Acceptance criteria

- Each subagent actor receives a stable plant emoji within a session projection.
- The emoji appears on all iOS transcript row kinds owned by that subagent, including live tails, model output, thinking, and tool activity.
- Coordinator, user, and daemon/system rows are not assigned subagent emoji.
- Assignments reconstruct deterministically from replay and remain stable across reconnects.
- More concurrent actors than palette entries degrade deterministically without crashing or losing textual actor identity.
- Headless Swift tests cover stability, replay, exclusions, and palette overflow.

## Work log
