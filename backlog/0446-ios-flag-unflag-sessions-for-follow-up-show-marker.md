---
id: "0446"
title: 'iOS: flag/unflag sessions for follow-up, show marker and Follow-up filter'
status: in_review
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
- 2026-10-10: Implemented, UNCOMMITTED (iOS in_review convention; also the changeset tool cannot attribute it because clients/ios/App/SessionView.swift already carried unrelated dirty actor-run-heading hunks, shared with SessionProjection*.swift). Files: YccClient.setSessionFollowUp; SessionListModel (SessionListSource.setSessionFollowUp, optimistic setFollowUp with rollback + followUpErrorMessage, showsFollowUpOnly filter, followUpCount, session(sessionID:), project(for:) fallback to historyLoads); LandingView (flag.fill row marker, leading swipe + context-menu flag/unflag, Follow up (N) filter bar shown when something is flagged or filter on, empty state, error alert); SessionView actionMenu "Flag for follow-up"/"Remove follow-up flag" (sessionList passed from LandingView); Analytics allowlist. Swift protos were already regenerated in 0441. YccKit Docker check: /tmp/ycc-0446-checks (`docker run --rm -v /tmp/ycc-0446-checks:/check -w /check swift:6.2-noble swift test`) — 76 SessionListModel tests incl. 5 follow-up tests pass. App/ SwiftUI not compiled here: needs Xcode build + on-device check (swipe/context menu, filter bar layout above List incl. large-title collapse, session menu toggle).
