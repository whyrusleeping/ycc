---
id: "0215"
title: 'iOS: aggregate active sessions across all projects'
status: todo
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Navigation shell — workspace drawer + active-session inbox
---

## Description
Add a daemon-wide active-session data model for the iOS home screen. Load registered projects, fan out ListSessionHistory per distinct workspace, attach stable project identity to every summary, retain active/attention sessions (live running or paused, all waiting-input, and live errors), pin waiting-input sessions, and merge the remainder most-recent-first. Idle/stopped history stays project-scoped. This is the data source for the All active destination in the workspace drawer.

Acceptance criteria:
- Sessions from every distinct registered workspace appear in one list and retain project name/path identity for display and navigation.
- Waiting-input sessions are pinned globally; remaining rows are sorted by latest activity.
- Duplicate registry/default-workspace queries and duplicate session rows are avoided.
- One project failing does not hide successful results; a partial-results warning identifies failures.
- Empty registry falls back cleanly to the daemon default workspace.
- Opening/resuming/sending to an aggregate row uses that row's project rather than ambient project selection.
- Global and per-project active/needs-answer counts are exposed for drawer badges.
- Aggregation and error behavior have headless YccKit tests.

## Acceptance criteria

## Work log
