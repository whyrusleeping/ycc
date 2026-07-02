---
id: "0118"
title: 'ListWorkstreams enrichment: reuse the project repo instead of re-opening per workstream'
status: todo
priority: 5
created: "2026-07-02"
updated: "2026-07-02"
depends_on:
    - "0085"
spec_refs: []
---

## Description
## Context
Review note from task 0085: `Manager.WorkstreamCommitCount` calls `primaryRepo` → `git.Open` (which spawns `git rev-parse`) once per non-terminal workstream on every `ListWorkstreams` call, and the TUI Workstreams panel polls this every ~3s. Redundant subprocess work at scale.

## Scope
Open/resolve each project's primary repo once per ListWorkstreams call (or cache per project on the Manager), reusing it for all of that project's workstream enrichment (commit count, and any future fields).

## Acceptance criteria
- [ ] ListWorkstreams enrichment opens each project's repo at most once per call.
- [ ] Behaviour unchanged (best-effort, 0/empty on error); build/vet/test pass.

## Acceptance criteria

## Work log
