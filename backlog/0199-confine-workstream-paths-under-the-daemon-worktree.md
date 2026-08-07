---
id: "0199"
title: Confine workstream paths under the daemon worktrees root
status: done
priority: 1
created: "2026-07-15"
updated: "2026-08-07"
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

## Plan

Goal: workstream worktree paths must always live beneath the configured worktrees root, regardless of hostile project display names or tampered registry entries.

1. New helpers in internal/workstream (e.g. paths.go):
   - `SafeProjectDir(name string) string`: maps a project display name to a safe single path component. Names already matching a conservative pattern (`^[A-Za-z0-9][A-Za-z0-9._-]*$`, not "." or "..") map to themselves so existing persisted workstreams under safe names keep their directories. Anything else (separators, `..`, absolute-looking text, Unicode, spaces, empty) is sanitized to safe chars AND suffixed with a short collision-resistant hash of the raw name (e.g. first 8 hex of sha256) so distinct raw names cannot alias each other.
   - `ContainedPath(root string, parts ...string) (string, error)`: joins root+parts, resolves to absolute, and verifies via filepath.Rel (no `..` escape, not equal to root itself) that the result is strictly beneath root; error otherwise.
   - `VerifyUnderRoot(root, path string) error`: same final containment check for already-recorded absolute paths.
2. internal/session/session.go SpawnWorkstream (~line 1585): build the worktree dir via SafeProjectDir + ContainedPath against m.worktreesRoot; fail before any mkdir / `git worktree add` if containment fails (belt-and-braces — should be unreachable after sanitization).
3. Destructive-path validation (fail closed): in cleanupWorktree and DiscardWorkstream (internal/session/workstream_merge.go), validate ws.WorktreePath with VerifyUnderRoot(m.worktreesRoot, ...) before calling repo.RemoveWorktree; skip the worktree removal (but still allow branch delete + prune + status transitions) when the recorded path is outside the root. Document this fail-closed behavior in comments.
4. Compatibility: safe names keep identical paths (no migration needed). Unsafe-named projects previously escaping the root will get new safe dirs; their old entries reconcile via the existing stale-detection path. Note this in a comment on SafeProjectDir.
5. Tests:
   - internal/workstream: table test for SafeProjectDir (safe names unchanged; `../../escape`, `a/b`, `a\b`, absolute-looking `/etc`, Unicode, spaces, "." / ".." / empty all yield safe distinct components; collision pair like `a/b` vs `a_b` don't alias) and for ContainedPath/VerifyUnderRoot rejection of escapes.
   - internal/session: a manager test registering a project named `../../escape` (git repo in a temp dir), spawning a workstream, asserting the created worktree is beneath the configured root and that nothing was created outside it; plus a cleanup fail-closed test where a registry entry's WorktreePath points outside the root and Discard leaves that path untouched.
6. `go build ./... && go test ./internal/workstream/... ./internal/session/...`, then full `go test ./...` (aware of known flakes).

### Starting points
- internal/session/session.go:1585 — dir := filepath.Join(m.worktreesRoot, cfg.Project, id) in SpawnWorkstream; root set at :1417 (SetWorkstreams) and :1372 (default)
- internal/session/workstream_merge.go — cleanupWorktree (:351) and DiscardWorkstream (:371) call repo.RemoveWorktree(ws.WorktreePath) unvalidated
- internal/workstream/registry.go — DefaultWorktreesRoot; Workstream.WorktreePath is persisted JSON (treat as untrusted on load)
- internal/session/workstream_test.go and workstream_merge_test.go show how tests build a Manager + temp git repo + SetWorkstreams(reg, root)
- internal/git/worktree.go — AddWorktree/RemoveWorktree/PruneWorktrees

## Work log
- 2026-08-07 plan: Goal: workstream worktree paths must always live beneath the configured worktrees root, regardless of hostile project display names or tampered registry entries.  1. New helpers in internal/workstream
…[truncated]
- 2026-08-07 context hints: 5 recorded with plan
- 2026-08-07 context hints: internal/session/session.go:1585 — dir := filepath.Join(m.worktreesRoot, cfg.Project, id) in SpawnWorkstream (spawn body starts ~1555); root default at :1372, SetWorkstreams at :1417; internal/sessi
…[truncated]
- 2026-08-07 implementer report: Implemented task 0199.  Changes: - Added `internal/workstream/paths.go` with stable `SafeProjectDir`, strict `ContainedPath`, and untrusted-registry `VerifyUnderRoot` helpers. - Safe existing ASCII pr
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): revise — The core containment helpers, spawn integration, and destructive-path guards are well structured, and both the focused tests and `go test ./...` pass. However, the project-directory mapping still has
…[truncated]
- 2026-08-07 revision: Addressed both reviewer findings.  Changes: - Reserved the generated `-<16 lowercase hex>` suffix namespace in `SafeProjectDir`. Otherwise-safe literal names matching that shape are now hashed again,
…[truncated]
- 2026-08-07 review (sol): accept — The revision addresses both prior findings: transformed project names now occupy a reserved suffix namespace that cannot be preserved verbatim, and reconciliation fails closed on persisted out-of-root
…[truncated]
- 2026-08-07 decision: accept — commit: Confine workstream paths under the daemon worktrees root (task 0199)  Worktree dirs are now derived via workstream.SafeProjectDir (verbatim only for conservative ASCII names outside the reserved -<16 
…[truncated]
