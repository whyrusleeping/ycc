---
id: "0542"
title: Show session duration (time since start) in web and iOS session header and running rows
status: done
priority: 3
created: "2026-11-10"
updated: "2026-11-27"
depends_on: []
spec_refs: []
---

## Description

User: "There's no easy way to see how long a session has been running for." Chosen meaning: total time since the session started (SessionSummary.started_at), on web and iOS.

## Acceptance criteria

- Web: helper unit-tested; `npm run typecheck` and tests pass.
- iOS: YccKit helper unit-tested (`swift test` on the Mac); app builds. Ends in_review pending on-device use.

## Outcome

Web typecheck and tests pass; iOS duration formatting and app build were verified on the Mac. The owner confirmed session durations work on a recent physical-device build.

Commit: Close session duration review on web and iOS
