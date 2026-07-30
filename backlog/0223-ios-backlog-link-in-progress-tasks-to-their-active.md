---
id: "0223"
title: 'iOS backlog: link in-progress tasks to their active session'
status: in_review
priority: 2
created: "2026-07-21"
updated: "2026-07-21"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Phase 2 — start work, backlog
---

## Description
When an iOS backlog task is `in_progress`, look up live session-history rows focused on that task and present a direct “Open active session” action from task detail. Match only genuinely active (`running` or `paused`) live sessions; if more than one matches, expose each. Opening the action navigates to the existing live `SessionView` rather than starting a duplicate work session.

## Acceptance criteria
- An in-progress task with a live running/paused session focused on its task id shows a direct session action.
- Tapping the action opens that session's live transcript.
- Tasks without a matching active session keep the existing start-work action.
- Matching/session lookup behavior has headless YccKit tests.

## Work log
