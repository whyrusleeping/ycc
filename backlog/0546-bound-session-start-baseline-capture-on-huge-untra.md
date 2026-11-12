---
id: "0546"
title: Bound session-start baseline capture on huge untracked files
status: done
priority: 2
created: "2026-11-12"
updated: "2026-11-12"
depends_on: []
spec_refs: []
---

## Description

`git.Open` → `CaptureBaseline` → `writeWorktreeTree(head, nil)` runs `git add -A -- .` into a temp index, which hashes every untracked, non-ignored file into the repo's object store. Session start blocks until it finishes, with no size limit or timeout. In ~/code/simclusters an untracked 31 GB `follows-dataset.csv` stalled three start attempts. Each one wrote a 0.5–1.2 GB `tmp_pack_*` per few minutes into `.git/objects/pack`, and the session events stayed empty. If capture had finished, `PersistBaseline` would have pinned the blob under `refs/ycc`. The same path also runs on every changeset inspection (`changeset.go` `writeWorktreeTree(head, nil)` around line 457).

Acceptance:
- Session start in a repo with a multi-GB untracked, non-ignored file returns promptly and does not write that file's content into `.git`.
- Oversized untracked files are excluded from baseline and changeset snapshots by a documented threshold. The session surfaces this (for example, a notice that names the file and suggests adding it to `.gitignore`) instead of failing silently.
- Tracked files and normal-sized untracked files keep the current attribution and commit behaviour; existing `internal/git` tests pass.
- A regression test covers a large untracked file (sparse file or a test-lowered threshold).

## Outcome

Whole-worktree snapshots (CaptureBaseline, Changes, git.Open's init commit) exclude untracked, non-ignored regular files over 100 MiB (absent from HEAD and the real index) via exclude pathspecs, so they are never hashed. Baseline.Oversized()/Changeset.Oversized report them; session start emits a warning notice naming them; commit results note them. Spec documents the limit. Verified: new internal/git tests; git/orchestrator/session suites pass; simclusters baseline capture takes 0.66s with follows-dataset.csv reported. internal/server TestGetBacklogGroom fails identically on prior HEAD (stale hardcoded date).

Commit: git: leave untracked files over 100 MiB out of worktree snapshots
