---
id: "0230"
title: Remove the interaction-level concept entirely
status: done
priority: 2
created: "2026-08-04"
updated: "2026-08-04"
depends_on: []
spec_refs:
    - Interaction levels
---

## Description
Remove interaction levels as a user-facing and session-level concept. Sessions use normal assistant judgement and may ask when input is useful. Unattended daemon work-loop execution remains an internal execution property so it cannot block on `ask_user`, but it is not configurable as an interaction level.

Acceptance criteria:
- Remove interaction-level selectors, settings, CLI flags, RPC fields/methods, events, projection state, prompts, and current documentation.
- Ordinary sessions always use normal interactive question behavior.
- Daemon work-loop sessions remain non-blocking through an internal unattended flag.
- Merge/publish safety gates use explicit operation semantics rather than interaction levels; clean workstream merges require acceptance.
- Regenerate protobuf clients and update Go/iOS tests.
- Preserve unrelated in-progress changes and historical backlog logs.
- Go tests pass; run Swift tests when tooling is available.

## Acceptance criteria

## Work log
