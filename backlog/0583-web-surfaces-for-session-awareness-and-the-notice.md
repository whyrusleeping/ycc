---
id: "0583"
title: Web surfaces for session awareness and the notice board
status: todo
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

## Scope
- A project sessions view: live sessions with intent, held task, state and claimed paths, plus active notices with poster, subject and expiry; post and clear notices there. Shown only while the project's `session_coordination` is on.
- Backlog board and task detail badge a task held by a live session and link to that session.
- The pending-question UI shows active notices.
- `coordination_notice` events (overlap notices and board digests) render distinctly, with session ids linked to the session.

## Acceptance
- `npm run typecheck` and web tests pass; Playwright screenshots of the sessions view, a held badge, the question notices and a coordination notice row, light and dark.
- `internal/web/dist` rebuilt.

## Acceptance criteria

## Work log
