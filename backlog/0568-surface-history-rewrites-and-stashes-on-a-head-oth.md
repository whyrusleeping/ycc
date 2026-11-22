---
id: "0568"
title: Surface history rewrites and stashes on a HEAD other live sessions share
status: todo
priority: 2
created: "2026-11-20"
updated: "2026-11-20"
depends_on: []
spec_refs:
    - spec.md#7.3
---

## Description
Current state: split from 0565 (stop commits sweeping in other sessions' uncommitted changes) on 2026-11-20; not started.

This delivers 0565's shared-HEAD outcome. It is separate because it is Bash-tool detection, independent of how commits are scoped.

## Why
In the wheeler project, where many parallel sessions shared one tree (2026-11-15 to 2026-11-20), 12 sessions rewrote the shared HEAD (28 amends, 17 resets), 21 ran `git stash` push or pop, and 28 built commits with plumbing (`hash-object`, `update-index`, `commit-tree`) in a tree other live sessions shared. Rewriting HEAD or stashing there risks losing their work and bypasses change attribution.

## Acceptance criteria
- History-rewriting or stash operations on a shared HEAD while other live sessions exist are surfaced to the agent in the tool result, not silently allowed.
- spec §7.3 describes the behaviour.

## Work log
- 2026-11-21 groom: glossed "wheeler" as the user's project whose shared-tree session logs supplied the evidence. Otherwise reviewed, no change; nothing is implemented yet.
