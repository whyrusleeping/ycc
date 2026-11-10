---
id: "0542"
title: Show session duration (time since start) in web and iOS session header and running rows
status: in_review
priority: 3
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs: []
---

## Description
User: "There's no easy way to see how long a session has been running for." Chosen meaning: total time since the session started (SessionSummary.started_at), on web and iOS.

## Behaviour
- Duration = end − started_at, where end = now for live sessions (ticking ~30s) and last_activity for persisted ones.
- Format: `<1m`, `42m`, `2h 05m`, `3d 4h` (≥48h).
- Session header (web `.session-meta`, iOS principal toolbar subtitle) shows it, with the start time available (web tooltip / iOS accessibility label).
- Session list rows of running sessions (web sidebar/page rows; iOS row trailing status) show it, since those rows currently hide or zero out time.

## Acceptance criteria
- Web: helper unit-tested; `npm run typecheck` and tests pass.
- iOS: YccKit helper unit-tested (`swift test` on the Mac); app builds. Ends in_review pending on-device use.

## Work log
