---
id: "0199"
title: Confine workstream paths under the daemon worktrees root
status: todo
priority: 1
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - Persistence & remote sync#Parallel workstreams (git worktrees)
---

## Description
Explicit project names are accepted verbatim and then used as filesystem components in `filepath.Join(worktreesRoot, projectName, workstreamID)`. Names containing `..` or separators can escape the configured worktree root, violating the documented location invariant and widening creation/cleanup beyond daemon state.

Replace display-name-derived paths with a safe opaque/sanitized project directory and enforce final containment before any mkdir, git worktree add/remove, or cleanup operation.

## Acceptance criteria
- [ ] Workstream paths are derived from a safe stable project identifier or collision-resistant sanitized/hash representation, not raw display names.
- [ ] A final path-containment check guarantees every generated worktree path is beneath the configured worktrees root.
- [ ] Project names containing `..`, absolute-looking text, slash/backslash separators, Unicode, spaces, and collisions cannot escape or alias another project's directory.
- [ ] Cleanup/merge/discard validates registry paths before destructive filesystem or git operations and fails closed for out-of-root paths.
- [ ] Existing safe project names and persisted workstreams remain supported or have a documented migration/reconciliation path.
- [ ] Unit tests exercise traversal names such as `../../escape` and verify no directories are created outside the root.
- [ ] `go test ./...` passes.

## Work log
