---
id: "0586"
title: Attribute code-file changes line by line in shared worktrees
status: proposed
priority: 3
created: "2026-11-22"
updated: "2026-11-22"
depends_on: []
spec_refs: []
---

## Description
Proposed follow-up to 0580 and its non-blocking replacement. Review snapshots and commits take a code file whole, so in a shared worktree they carry other sessions' uncommitted hunks. Backlog task files and `memory.md` are already attributed line by line (`git.Attribution.Lines`, `Changeset.Partial`). Extending line attribution to code files would let review show, and commit take, only this session's lines, leaving other sessions' lines in the worktree.

Open design questions: whether a line view that interleaves two sessions' hunks still builds; handling of overlapping hunks; how shell edits (no per-line record) are attributed; interaction with 0567's live-shared commit refusal.

## Acceptance criteria
- A code file changed by this session and another session reviews and commits with only this session's line changes, and the other session's lines stay uncommitted in the worktree.
- Overlapping or unattributable hunks fall back to the current whole-file behaviour, with a note.

## Work log
