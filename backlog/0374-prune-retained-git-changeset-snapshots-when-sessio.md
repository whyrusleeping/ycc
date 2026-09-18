---
id: "0374"
title: Prune retained Git changeset snapshots when session evidence expires
status: done
priority: 3
created: "2026-09-09"
updated: "2026-09-18"
depends_on:
    - "0353"
spec_refs: []
---

## Description

Task 0353 pins immutable baseline/changeset trees under refs/ycc and persists baseline records in the Git directory so restart/GC cannot invalidate review evidence. There is currently no cleanup policy, so refs and otherwise-collectible trees accumulate.

Acceptance: define retention tied to session/evidence lifetime; remove expired refs/records without invalidating live or reopened sessions and retained review commands; provide a safe cleanup/doctor path and tests for live versus expired evidence. Do not prune snapshots merely because a coordinator finishes.

## Outcome

Added conservative evidence-lifetime snapshot cleanup with doctor dry-run/apply flags, shared-repository locking, nested-workspace ownership, retained review/finalization protection, legacy retention, and resumable deletion. Both independent reviewers accepted. Final isolated scoped tree passed uncached internal/git, cmd/ycc, internal/session tests, Git race tests and vet; relevant orchestrator regression tests also passed. Unrelated existing changes preserved.

Commit: Prune Git snapshots only after session evidence expires
