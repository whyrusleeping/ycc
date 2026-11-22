---
id: "0583"
title: Web surfaces for session awareness and the notice board
status: done
priority: 4
created: "2026-11-22"
updated: "2026-11-22"
depends_on:
    - "0582"
spec_refs:
    - spec.md#7.3
    - spec.md#18.5
---

## Description

Current state: todo; its dependency 0582 (daemon API and CLI for the project sessions view: `ListProjectNotices.sessions`, `PostProjectNotice`) is done. Delivers the web outcome of 0562 (client surfaces for session awareness and the notice board). Separate from the iOS work because each client needs its own build and screenshot verification.

## Acceptance criteria

- `npm run typecheck` and web tests pass; Playwright screenshots of the sessions view, a held badge, the question notices and a coordination notice row, light and dark.
- `internal/web/dist` rebuilt.

## Outcome

Web client shows session coordination while a project's setting is on: /p/<project>/live lists live sessions (intent, held/earlier focus, activity, claimed paths) and active notices with post and clear-with-reason; a sidebar Live sessions line and palette entry link to it; backlog table, board and task detail badge held tasks linking to the holder; active notices sit above a pending question; coordination_notice events render as a distinct folded row kind with session ids linked. Typecheck and 487 vitest tests pass; dist rebuilt; light/dark Playwright shots and a post/clear/off-state/link flow verified on a scratch daemon (.ycc/tmp/coord0583). Review fix: cached board data is hidden once coordination turns off.

Commit: Web: live sessions view and notice board, held-task badges, question notices, coordination transcript rows (0583)
