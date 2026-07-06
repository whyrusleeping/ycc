---
id: "0163"
title: 'Spec update: record forge integration (import, publish, auth model)'
status: proposed
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0156"
    - "0157"
spec_refs:
    - docs/design/forge-integration.md#11. Prompt-level vs. first-class, and phased rollout
---

## Description
From docs/design/forge-integration.md §11 task 9 (design spike 0146). Keeps the spec true (spec §1) once forge features land.

Add a short §14.2 (or extend §6.2/§14.1) recording forge integration: `ycc task import` + the `origin:` task field, the workstream `published` terminal state + `workstream_published` event + PublishWorkstream RPC, the gh/glab delegation + no-tokens-in-ycc auth model (incl. the daemon-environment caveat), and the confirmation-gate semantics for public side effects.

## Acceptance criteria
- [ ] Spec sections updated to match shipped behaviour; spec-check passes.

## Work log
