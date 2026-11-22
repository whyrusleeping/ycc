---
id: "0584"
title: iOS surfaces for session awareness, the notice board, and the coordination setting
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
Current state: todo; its dependency 0582 (daemon API and CLI for the project sessions view: `ListProjectNotices.sessions`, `PostProjectNotice`) is done. Delivers the iOS outcome of 0562 (client surfaces for session awareness and the notice board). Separate from the web work because it needs the configured Mac build and simulator screenshots.

## Scope
- A project sessions view: live sessions with intent, held task, state and claimed paths, plus active notices with poster, subject and expiry; post and clear notices there. Shown only while the project's `session_coordination` is on.
- The backlog browser badges a task held by a live session and links to that session.
- The pending-question screen shows active notices.
- `coordination_notice` events (overlap notices and board digests) render distinctly, with session ids linked to the session.
- Settings shows and changes the per-project `session_coordination` setting through `SetProjectSettings`.

## Acceptance
- Built and tested on the configured Mac (`xcodebuild` build/test, `swift test` for YccKit model changes), with simulator screenshots of each surface.

## Acceptance criteria

## Work log
