---
id: "0249"
title: Serialize backlog id allocation per project (fixes duplicate ids from parallel worktrees)
status: todo
priority: 1
created: "2026-08-06"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - docs/design/workstream-integration.md#6. Worktree ergonomics
---

## Description

`docs.Store.nextID` (`internal/docs/docs.go:332`) allocates ids as `max(existing)+1` by
scanning the backlog directory **of the current tree**. Each workstream worktree has its own
checkout of `backlog/`, so two parallel streams that both create a task mint the *same* id,
and the collision only shows up after merge. This repo already carries 14 duplicate ids
(0120, 0175, 0192–0195, 0211–0218) — see task 0242 — which is almost certainly this bug.

Make id allocation a per-project, daemon-serialized operation: a counter in the daemon state
dir keyed by project, seeded from `max(existing ids in the primary tree)` on first use and
monotonically bumped under a lock. Worktree sessions ask the daemon for the next id rather
than scanning their own tree. Fall back to today's scan when there is no daemon-owned project
(one-shot in a plain directory) — that path has no parallelism.

## Acceptance criteria

- [ ] Two concurrent `create_task` calls from two different worktrees of the same project
      receive different ids (test).
- [ ] The counter seeds correctly from an existing backlog and never re-issues an id already
      present in the primary tree.
- [ ] A tree with no daemon project still allocates ids as before.
- [ ] Task 0242's cleanup is unblocked (duplicates stop being regenerated).


## Work log
