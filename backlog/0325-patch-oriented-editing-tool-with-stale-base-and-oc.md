---
id: "0325"
title: Transactional Patch tool with file revisions and occurrence selectors
status: proposed
priority: 3
created: "2026-08-12"
updated: "2026-08-12"
depends_on: []
spec_refs:
    - §8 Tools and access policy
---

## Description
Add a revision-bound, single-file `Patch` tool alongside the existing exact `Edit` tool. Text `Read` results expose a whole-file SHA-256 content revision, including when only a window is returned. `Patch` accepts that revision and a bounded batch of exact replacements; a replacement may explicitly select a 1-based textual occurrence. All replacement ranges are resolved against the same base content, validated as non-overlapping, and committed together.

This is optimistic transactional editing: `Read` supplies an ETag-like revision and `Patch` applies only when that revision still matches. Line numbers remain navigational output and diagnostics, not mutation authority. The first version does not use fuzzy matching or require a unified-diff parser. Existing exact unique `Edit` remains the simple, safe default.

On a stale revision, missing or ambiguous target, invalid occurrence, overlap, or malformed request, no changes are made. Diagnostics return the current revision and bounded current-file context sufficient for one corrective retry. Atomicity is guaranteed relative to ycc's single-writer scheduling; it does not claim filesystem compare-and-swap against non-cooperating external writers.

## Acceptance criteria

- Text `Read` results include a whole-file content revision, including partial-window reads.
- `Patch` accepts one file, a required base revision, and a bounded list of exact replacements.
- A replacement without `occurrence` must match exactly once; one with `occurrence` selects that 1-based textual occurrence.
- All targets are resolved against the supplied base content, must not overlap, and commit together with one file write.
- A stale revision, missing target, ambiguous target, invalid occurrence, overlap, or malformed request leaves the file unchanged and does not invoke `OnWrite`.
- Failure diagnostics include the current revision, each failed target's status, bounded match line locations, and bounded rune-safe current context suitable for one corrective retry.
- Successful `Patch` returns the resulting content revision.
- Existing exact `Edit` remains unchanged and available.
- `Patch` uses the same workspace/write-root and symlink-aware path confinement as `Write` and `Edit`.
- Tests cover duplicate snippets, stale revisions, same-file multi-hunk atomicity, overlapping targets, path confinement and symlink escapes, CRLF and final-newline preservation, and UTF-8 diagnostics.

## Work log
