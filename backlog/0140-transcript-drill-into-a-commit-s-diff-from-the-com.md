---
id: "0140"
title: 'Transcript: drill into a commit''s diff from the commit_made row'
status: todo
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 18.9 Transcript rendering is incremental (render caches)
---

## Description
The transcript renders `commit_made` as sha + message, and `< / >` jumps between commits — but the user can't *see the change* without dropping to a shell. Reviewing what the agent actually committed is the core trust loop of the harness; it should be one keypress. The diff renderer + syntax highlighting already exist (implementer diffs, workstream merge preview).

## Acceptance criteria
- [ ] Enter (or a dedicated key) on a selected `commit_made` row expands/opens the commit's diff (`git show`), syntax-highlighted, scrollable, foldable per file for large commits.
- [ ] Works in the read-only session-browser transcript too (same rendering path).
- [ ] Large diffs are windowed/truncated safely (never blow up the render cache invariants of §18.9).

## Work log
