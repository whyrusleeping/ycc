---
id: "0375"
title: Avoid baseline capture for read-only Git RPCs
status: proposed
priority: 3
created: "2026-09-09"
updated: "2026-09-09"
depends_on:
    - "0353"
spec_refs: []
---

## Description
Review of 0353 found git.Open captures a full worktree baseline, including in read-only callers such as WorkstreamCommitCounts, PreviewWorkstreamMerge and GetCommitDiff. This can introduce avoidable full-tree hashing and baseline-specific failures into read-only operations.

Acceptance: use an existing-repo/read-only open path for callers that never need a mutation baseline, preserving fresh-session capture before mutation; verify read-only operations do not materialize baseline snapshots and measure representative cost reduction. Keep scoped mutation safety unchanged.

## Acceptance criteria

## Work log
