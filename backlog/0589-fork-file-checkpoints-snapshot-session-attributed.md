---
id: "0589"
title: 'Fork file checkpoints: snapshot session-attributed paths per user turn and opt-in restore'
status: proposed
priority: 4
created: "2026-11-22"
updated: "2026-11-22"
depends_on:
    - "0471"
spec_refs: []
---

## Description
Status: proposed. Follow-up idea from 0471 (session fork and rewind from a user turn), whose v1 forks conversation state only; on 2026-11-22 the 0471 session deferred file restore (agent inference; the user had left the question open). Needs the user's acceptance.

Sketch carried over from 0471: at each user turn, snapshot the session's attributed paths to a private ref (`refs/ycc/checkpoints/<session>/<seq>`) using a temporary index and `git write-tree`, leaving the user's index and HEAD untouched. When forking or rewinding, the user can opt in to restoring those paths. Restore covers only paths the session claimed, and refuses paths that another session or the user changed since the checkpoint, so it cannot clobber concurrent work in a shared worktree. 0471's sketch also listed an option to start a fork in a fresh workstream, so that two approaches run in isolated worktrees.

## Open questions
- Retention and garbage collection of checkpoint refs.
- Should restore be offered from the fork action, or as a separate "restore files to this turn" action?

## Acceptance criteria

## Work log
