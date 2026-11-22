---
id: "0568"
title: Surface history rewrites and stashes on a HEAD other live sessions share
status: done
priority: 2
created: "2026-11-20"
updated: "2026-11-22"
depends_on: []
spec_refs:
    - spec.md#7.3
---

## Description

Current state: split from 0565 (stop commits sweeping in other sessions' uncommitted changes) on 2026-11-20; not started.

This delivers 0565's shared-HEAD outcome. It is separate because it is Bash-tool detection, independent of how commits are scoped.

## Acceptance criteria

- History-rewriting or stash operations on a shared HEAD while other live sessions exist are surfaced to the agent in the tool result, not silently allowed.
- spec §7.3 describes the behaviour.

## Outcome

Bash (foreground and background) now brackets commands with HEAD branch/commit and refs/stash snapshots of the shell's worktree and of cd / git -C targets (bounded, absolute targets prioritized), plus a quote/here-doc-aware scan for git stash. Non-fast-forward rewrites, branch switches/detaches, stash changes, or stash text in a worktree other live sessions share append a [shared HEAD] warning naming them (other projects' sessions only counted), worded as observed-while-running or tentative when the location is uncertain. Peers come from a new workspacelease SetSharedHeadPeers hook installed by session.Manager; not gated on coordination. Spec §7.3 documents it. Tests: TestSharedHeadRewritesAreSurfaced, TestGitTargets, TestSharedHeadPeersNameOtherSessionsInTheWorktree; full go test ./... green.

Commit: Warn in Bash results when a shell rewrites, switches or stashes a HEAD other live sessions share (0568)
