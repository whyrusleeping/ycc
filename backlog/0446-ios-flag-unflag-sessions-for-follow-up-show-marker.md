---
id: "0446"
title: 'iOS: flag/unflag sessions for follow-up, show marker and Follow-up filter'
status: todo
priority: 3
created: "2026-10-10"
updated: "2026-10-10"
depends_on:
    - "0441"
spec_refs: []
---

## Description
Split from 0441. 0441 adds the daemon's follow-up flag: `SetSessionFollowUp` RPC, `SessionSummary.follow_up` / `follow_up_at`, and TUI support. This task covers the iOS client (clients/ios).

## Acceptance criteria
- The user can flag and unflag a session from the iOS session list (for example a swipe action or the context menu) and from the open session.
- Flagged sessions show a clear marker. Session lists can be filtered to only flagged sessions, or show a "Follow up" section.
- The flag follows 0441's documented clearing behavior: manual clear only.
- Regenerate the Swift protos. Test the pure YccKit logic in Docker (see memory). The on-device check happens on the user's Mac, so in_review is the expected end state.

## Work log
