---
id: "0068"
title: Allow reading trusted paths outside the workspace (e.g. Go mod cache)
status: done
priority: 3
created: "2026-06-29"
updated: "2026-06-29"
depends_on: []
spec_refs:
    - Tools
---

## Description
## Description

File-read access is currently confined to the workspace (path confinement in
`tools.Workspace.resolve`). This is too strict for legitimate read-only locations
that live outside the workspace — most notably the Go module cache
(`$GOMODCACHE` / `$GOPATH/pkg/mod`), where the model frequently needs to read
dependency source. Reads of these paths currently fail.

Make read access smarter: keep the workspace as the default writable/confined
root, but allow reads from a configurable allowlist of trusted external roots.
Seed sensible defaults (e.g. the Go module cache; consider other read-only
toolchain/std-lib paths) and let users extend the list.

Scope note: this is about *read* access. Write/mutation confinement to the
workspace should remain unchanged (see also task 0008).

## Acceptance criteria

- [ ] The model can read files under the Go module cache (`$GOMODCACHE`, falling back to `$GOPATH/pkg/mod` / default `~/go/pkg/mod`)
- [ ] An allowlist of trusted read-only roots outside the workspace is configurable
- [ ] Go mod cache is included by default; resolution handles env vars not being set
- [ ] Writes remain confined to the workspace (no new write access outside it)
- [ ] Path checks are symlink-aware so the allowlist can't be used to escape into arbitrary locations
- [ ] Reads outside the workspace and outside the allowlist still fail with a clear error

## Acceptance criteria

## Work log
- 2026-06-29 plan: Allow Read (only) to access a configurable allowlist of trusted read-only roots outside the workspace; writes stay confined.  1. internal/tools/tools.go:    - Add `ReadRoots []string` to `Workspace` (
…[truncated]
- 2026-06-29 implementer report: Implemented Task 0068: read access to trusted paths outside the workspace (e.g. Go mod cache), while keeping writes confined.  Changes: - internal/tools/tools.go:   - Added `ReadRoots []string` to `Wo
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — Task 0068 is correctly and completely implemented. The Read tool now resolves via a new `resolveRead` that keeps the existing in-workspace behaviour while additionally allowing paths under a list of t
…[truncated]
- 2026-06-29 decision: accept — commit: Allow Read tool to access trusted read-only roots outside the workspace (0068)  Add Workspace.ReadRoots and resolveRead so the Read tool can read files under a symlink-aware allowlist of trusted roots
…[truncated]
