---
id: "0272"
title: Stabilize iOS session transcript auto-follow and composer scrolling
status: todo
priority: 2
created: "2026-07-16"
updated: "2026-08-06"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Investigate and fix the iOS SessionView transcript failing to remain pinned while live output streams and jumping/twitching while the user types. Preserve intentional scroll-up behavior and the jump-to-latest affordance.

## Acceptance criteria
- A transcript that is following the latest content remains pinned as streaming tail text, durable events, keyboard visibility, and multiline composer height change.
- Streaming updates do not queue overlapping scroll animations or visibly twitch/reset the feed.
- An intentional user scroll away from the bottom disables auto-follow and shows the jump-to-latest pill; new events do not yank the transcript.
- Scrolling back to the bottom or tapping the pill resumes following.
- The iOS app builds successfully.

## Work log
- 2026-08-06 renumbered 0215 → 0272 (duplicate id detected, 0215 kept by another task)
