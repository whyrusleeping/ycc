---
id: "0558"
title: 'Git history view: ListCommits RPC + web History page (branch picker, session/task badges)'
status: done
priority: 3
created: "2026-11-17"
updated: "2026-11-17"
depends_on: []
spec_refs:
    - docs/design/web-client.md
---

## Description

Status: in progress. Requested by the user 2026-11-15 (this session): a git history view in the apps, with a branch picker that includes workstream branches and badges linking commits to the ycc session/task that made them. Per-file history from the file viewer was explicitly deferred ("not now").

This task delivers the daemon RPC and the web client. The iOS view is a separate task because it has its own Mac build/verification gate and `in_review` on-device step.

## Acceptance criteria

- Go tests cover paging stability, branch validation, stats parsing, and session/task linkage (including the rebased fallback).
- `npm run typecheck`, web tests, and the committed bundle are up to date; the page is checked against a scratch daemon.

## Outcome

ListCommits RPC pages a local branch's git log (cursor pinned to the first tip, shortstat, branch list with workstream branches marked) and links commits to the recording session/task via commit_made (sha, or subject + author time within 10 min for rebased workstream commits). Web History page at p/:project/history: branch picker in ?branch=, load-more list, session/task badges, inspector commit diff, non-git/empty states. Go test TestListCommits, vitest history.test.ts, bundle rebuilt; light/dark screenshots checked against a scratch daemon. iOS follows in 0559.

Commit: Git history view: ListCommits RPC and web History page with branch picker and session/task badges
