---
id: "0217"
title: Fix disappearing iOS session transcript during live updates
status: done
priority: 1
created: "2026-07-16"
updated: "2026-07-16"
depends_on: []
spec_refs:
    - docs/design/ios-client.md
---

## Description
The iOS session transcript can render blank while an agent is working, then reappear only after the user scrolls. Investigate and fix the SwiftUI transcript layout/rendering path so durable rows remain visible during rapid live-tail updates and automatic bottom-following.

## Acceptance criteria
- Existing transcript rows do not disappear during streaming/live row updates.
- Bottom-follow and manual scrolling behavior remain intact.
- Relevant iOS/YccKit tests and an iOS simulator build pass.

## Work log
