---
id: "0325"
title: Transactional Patch tool with file revisions and occurrence selectors
status: blocked
priority: 3
created: "2026-08-12"
updated: "2026-09-10"
depends_on: []
spec_refs:
    - §8 Tools and access policy
---

## Description
Add a revision-bound, single-file `Patch` tool alongside the existing exact `Edit` tool. Text `Read` results expose a whole-file SHA-256 content revision, including when only a window is returned. `Patch` accepts that revision and a bounded batch of exact replacements; a replacement may explicitly select a 1-based textual occurrence. All replacement ranges are resolved against the same base content, validated as non-overlapping, and committed together.

This is optimistic transactional editing: `Read` supplies an ETag-like revision and `Patch` applies only when that revision still matches. Line numbers remain navigational output and diagnostics, not mutation authority. The first version does not use fuzzy matching or require a unified-diff parser. Existing exact unique `Edit` remains the simple, safe default.

Coordination with bounded `Read`: partial text windows have an 8 MiB source-scan budget and must remain streaming and memory-bounded. Producing a whole-file revision therefore requires an explicit, cancellable streaming scan separate from the window read; it must not restore whole-file allocation or silently hide an additional full-file scan.

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

## Plan

Implement a bounded, revision-bound single-file Patch tool alongside unchanged Edit. Resolve every replacement against one base snapshot, validate occurrence selectors/non-overlap before one write, and emit bounded rune-safe diagnostics with current revision and per-target status. Extend text Read's existing revision scan to provide whole-file hashes for partial windows without whole-file allocation, explicitly documenting the separate cancellable full scan while preserving the window scan budget. Reuse existing path confinement and mutation receipts where appropriate. Add focused tests for transactional rejection, selectors, encoding/newlines, revision scanning/cancellation, callback behavior, and escapes; verify scoped clean-HEAD compatibility and review security/atomicity before committing. Do not modify pre-existing dirty source/spec/client paths or absorb unrelated work.

### Starting points
- internal/tools/worker.go: Editing, readFile, fullContentRevision, sourceRevisionNote, editFile
- internal/tools/tools.go: Workspace.resolveWrite and OnWrite
- internal/tools/editdiag.go; internal/tools/worker_test.go; internal/tools/writeroots_test.go
- Working tree has many unrelated staged changes. internal/tools is clean; preserve unrelated index/worktree state. Existing ownership guard rejects editing baseline-dirty source files.

## Work log

- Preflight found `internal/tools` clean and recorded an implementation plan, but `spawn_implementer` failed before running any model or editing source: `establish changeset: task changes overlap paths that were already dirty at baseline acf21d93aae6510615f9e054d38fa0b7cff1a4d28dfbde8e170da71625991afa`, including this task's own pre-existing staged backlog file and the pre-existing staged backlog files 0370–0373 updated during selection. The guard covers task bookkeeping as well as source files: changing status or adding a plan to a baseline-dirty task prevents delegation even when implementation paths are clean. No code, tests, review, or commit occurred.
- Blocked on repository/session isolation, not a Patch design decision. Resume in an isolated clean worktree/session containing the accepted task, or have the existing owners resolve their staged work before capturing a new baseline. Do not discard, absorb, or falsely commit that unrelated work; do not retry the same delegation in this baseline. Implement the retained plan and original criteria once isolation is available.
