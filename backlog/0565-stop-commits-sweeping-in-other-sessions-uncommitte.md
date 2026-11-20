---
id: "0565"
title: Stop commits sweeping in other sessions' uncommitted changes and agents working around the commit tool
status: todo
priority: 2
created: "2026-11-20"
updated: "2026-11-20"
depends_on:
    - "0566"
    - "0567"
    - "0568"
spec_refs:
    - spec.md#6.2
    - spec.md#7.3
---

## Description
Current state: accepted on 2026-11-20 (session s_18f059896f075e5a). Split on 2026-11-20 into 0566 (commit only a session's own lines of backlog and memory files, with watch runs committing their own bookkeeping), 0567 (refuse or narrow commits of code files another live session also changed) and 0568 (surface history rewrites and stashes on a shared HEAD); this task closes when they are done and its criteria are checked together. This was found while reviewing the wheeler project's session logs for 0443 (experimental session awareness), but it is independent of that feature.

## Why
In the wheeler project from 2026-11-15 to 2026-11-20, many parallel sessions worked in one shared tree. Session logs show:
- 133 of 184 successful `commit` tool results (72%) noted pre-existing, shared, or unclaimed paths.
- 93 commits adopted pre-existing changes whole: 132 backlog-file instances, 23 `memory.md`, and about 30 docs or plans files.
- About 30 commits included paths another session had also written, about 12 of them code, scripts, or YAML.
- Watch runs append to backlog work logs without committing (0 commits in 39 runs), which leaves backlog files dirty for others to pick up.

Agents worked around ycc's commit tool to avoid carrying other sessions' changes:
- 28 sessions built commits with git plumbing (`hash-object`, `update-index`, `commit-tree`).
- 12 sessions rewrote the shared HEAD: 28 amends and 17 resets in the reflog.
- 21 sessions ran `git stash` push or pop in the shared tree.
- 11 sessions made private `git worktree add` trees.

On 2026-11-16 a hunk-only commit requested by the user still took a whole file (`9bab6cb`), and a later commit (`87f4e39`) removed the other task's hunks from HEAD. Rewriting HEAD or stashing in a tree that other live sessions share risks losing their work, and it bypasses the attribution described in spec §7.3.

The current design (spec §6.2 and §7.3) intentionally never claims backlog task files and `memory.md`, and adopts baseline-dirty paths whole once the session changes them.

## Scope
Make the supported commit path good enough that agents stop working around it in a shared tree, and stop committing other sessions' unfinished work by default. Candidate directions to evaluate (not decided):
- Commit only this session's own structured updates to backlog and `memory.md`, since those writes go through the docs store, instead of adopting the whole file.
- Commit docs-layer bookkeeping such as watch-run work-log appends at the point it is written, so it does not linger dirty.
- A supported hunk-level or "only my changes" commit path for code files that another live scope also claims.
- Detect amend, reset, stash, and plumbing commits on a HEAD that other live sessions share, and warn or refuse in the tool result.

## Acceptance criteria
- A session committing its own work no longer includes another session's uncommitted backlog or `memory.md` edits by default; tests cover concurrent edits to one task file by two sessions.
- Bookkeeping written by watch runs does not remain as long-lived dirty state that later commits sweep in.
- A session has a supported way to commit only its own changes to a code file another live session also changed, or a clear refusal explaining the alternative (a workstream or waiting for the other commit).
- History-rewriting or stash operations on a shared HEAD while other live sessions exist are surfaced to the agent, not silently allowed.
- spec §6.2 and §7.3 describe the chosen behaviour.

## Work log
