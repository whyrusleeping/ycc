---
id: "0566"
title: Commit only a session's own lines of shared backlog and memory files
status: done
priority: 2
created: "2026-11-20"
updated: "2026-11-20"
depends_on: []
spec_refs:
    - spec.md#6.2
    - spec.md#7.3
---

## Description

Current state: split from 0565 (stop commits sweeping in other sessions' uncommitted changes) on 2026-11-20; in progress.

This delivers 0565's bookkeeping outcomes (its first two acceptance criteria). It is its own task because it adds a persistent line-attribution record that feeds every commit, the highest-risk part of 0565; the shared-code and shared-HEAD parts are separate, lower-risk tasks.

## Acceptance criteria

- A session committing its own work no longer includes another session's uncommitted backlog or `memory.md` edits by default; tests cover concurrent edits to one task file by two sessions.
- Bookkeeping written by watch runs does not remain as long-lived dirty state that later commits sweep in.
- spec §6.2 and §7.3 describe the behaviour.

## Outcome

Scoped writes to backlog task files and memory.md (docs-store writes and Edit/Write) are now recorded as line changes per session, in the git dir (workspacelease/lines.go). Where another live session has recorded lines in one of these files, a changeset takes HEAD plus only this session's and unattributed lines and reports the file as partial. Supporting rules: changes it builds on are adopted, and a parent heading or key is kept; a task version that does not parse is refused; exact published versions are folded after a commit; HEAD is compared as cleaned blobs; staged entries are kept. Lines of sessions that are no longer live ride along with later commits. watch_report commits the run's own bookkeeping, and a failed commit is refused unless the report is an alert. Spec §6.2 and §7.3 updated, with tests. Also lands the split bookkeeping for 0565 (0567, 0568 todo) and proposed 0569 (pre-existing index-publication race).

Commit: Commit only a session's own lines of shared backlog and memory files; split 0565 into 0566-0568
