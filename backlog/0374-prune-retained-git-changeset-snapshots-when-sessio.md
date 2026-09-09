---
id: "0374"
title: Prune retained Git changeset snapshots when session evidence expires
status: proposed
priority: 3
created: "2026-09-09"
updated: "2026-09-09"
depends_on:
    - "0353"
spec_refs: []
---

## Description
Task 0353 pins immutable baseline/changeset trees under refs/ycc and persists baseline records in the Git directory so restart/GC cannot invalidate review evidence. There is currently no cleanup policy, so refs and otherwise-collectible trees accumulate.

Acceptance: define retention tied to session/evidence lifetime; remove expired refs/records without invalidating live or reopened sessions and retained review commands; provide a safe cleanup/doctor path and tests for live versus expired evidence. Do not prune snapshots merely because a coordinator finishes.

## Acceptance criteria

## Work log
