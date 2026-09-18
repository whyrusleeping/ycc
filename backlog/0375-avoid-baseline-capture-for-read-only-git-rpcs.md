---
id: "0375"
title: Avoid baseline capture for read-only Git RPCs
status: done
priority: 3
created: "2026-09-09"
updated: "2026-09-18"
depends_on:
    - "0353"
spec_refs: []
---

## Description

Review of 0353 found git.Open captures a full worktree baseline, including in read-only callers such as WorkstreamCommitCounts, PreviewWorkstreamMerge and GetCommitDiff. This can introduce avoidable full-tree hashing and baseline-specific failures into read-only operations.

Acceptance: use an existing-repo/read-only open path for callers that never need a mutation baseline, preserving fresh-session capture before mutation; verify read-only operations do not materialize baseline snapshots and measure representative cost reduction. Keep scoped mutation safety unchanged.

## Outcome

Confirmed previously committed OpenExisting paths cover commit counts, merge preview, and commit diff while fresh sessions retain mutation baseline capture. Added regression proving these reads do not store an untracked baseline blob and a reproducible 1,000-file benchmark (median 51.33 ms vs 1.81 ms, about 28x faster). Independent review accepted; focused Git/session/server tests and benchmark passed, including scoped-commit and reopen baseline safety. Broader reviewer tests hit unrelated cross-device-link environment failures. Existing unrelated work preserved.

Commit: Verify read-only Git operations avoid baseline snapshots
