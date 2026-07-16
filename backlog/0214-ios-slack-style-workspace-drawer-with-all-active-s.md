---
id: "0214"
title: 'iOS: Slack-style workspace drawer with all-active session inbox'
status: todo
priority: 2
created: "2026-07-15"
updated: "2026-07-15"
depends_on:
    - "0215"
spec_refs:
    - docs/design/ios-client.md#Navigation shell — workspace drawer + active-session inbox
---

## Description
Replace the iOS home screen's compact project-filter menu and crowded project-destination toolbar with a left-edge, overlay workspace drawer inspired by Slack/Discord. The drawer must be reachable by both a visible menu button and an interactive left-edge swipe; it contains an All active destination plus registered projects and Add project. Selecting a project scopes sessions/backlog/usage/workstreams/new-session context and closes the drawer.

Acceptance criteria:
- Drawer opens via hamburger and left-edge swipe, closes via scrim/reverse swipe, and preserves the current navigation destination while overlaid.
- All active is the default multi-project landing destination and renders the cross-project aggregate from task 0215; each row names its project.
- Drawer badges show active counts and needs-answer counts globally and per project; waiting-input is visually highest priority.
- Project selection closes the drawer and consistently scopes session navigation, new-session defaults, backlog, usage, and workstreams.
- Add project remains accessible at the bottom of the workspace list.
- Single-project/one-shot behavior remains useful and uncluttered.
- YccKit model logic is headless-tested, and both `swift test` and the iOS simulator build pass.

## Acceptance criteria

## Work log
