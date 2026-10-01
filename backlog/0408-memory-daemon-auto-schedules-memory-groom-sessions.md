---
id: "0408"
title: 'Memory: daemon auto-schedules memory-groom sessions under pressure'
status: in_review
priority: 2
created: "2026-09-30"
updated: "2026-09-30"
depends_on: []
spec_refs:
    - docs/design/project-memory.md#Write and grooming policy
---

## Description
Nudges ask the mid-task agent to "run the memory-groom flow", which only users can start; nobody does. The daemon should start an unattended memory-groom pm session for the project when active memory crosses the soft budget (on remember writes and at session start), using the preset binding / default coordinator (NOT a cheap model — user direction). One in flight per project, persisted cooldown, recursion-safe (groom's own writes don't retrigger), config kill switch. The mid-task agent's advice says grooming is handled automatically. The hard ceiling becomes a high backstop aligned with the prompt-injection cap. Unattended groom prompt: dedupe/retire/compact; propose (not apply) spec promotions as proposed tasks.

## Acceptance criteria

## Work log
