---
id: "0156"
title: 'ycc task import <issue-url>: GitHub issue → backlog task (origin field, dedupe)'
status: proposed
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0155"
spec_refs:
    - 6.2 Backlog — structured items, markdown-rendered
    - docs/design/forge-integration.md#5. Flow 1 — issues → backlog (`ycc task import`)
---

## Description
From docs/design/forge-integration.md §5 (design spike 0146).

New `ycc task import <issue-url>` subcommand in cmd/ycc/task.go beside add/list/show, reusing the existing `taskBackend` resolution (direct docs.Store or daemon RPC), so it works with or without a daemon.

- Fetch via `gh issue view <url> --json number,title,body,url,labels,state` (GitHub only in this task; glab parity is a follow-on).
- Field mapping per the doc: title→title, body→`## Description`, labels ignored in v1.
- Add an optional `origin:` frontmatter field to `docs.Task` holding the issue URL (rejected reusing spec_refs — see doc §5), threaded through both `directBackend` (docs.Store.Create) and `rpcBackend` (new `origin` field on CreateTaskRequest/TaskDetail proto).
- Dedupe on re-import: if a task's `origin` equals the issue URL, update in place (refresh title/body, work-log breadcrumb) and print the existing id — idempotent.

## Acceptance criteria
- [ ] `ycc task import <github-issue-url>` creates a task with title/body/origin; works via both backends.
- [ ] Re-running the same import updates in place, never duplicates.
- [ ] No gh / not authed / unparseable URL / 404 → specific actionable error, no task created (doc §9).
- [ ] Documented in docs/cli.md.

## Work log
