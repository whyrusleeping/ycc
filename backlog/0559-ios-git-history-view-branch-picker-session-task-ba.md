---
id: "0559"
title: 'iOS: git history view (branch picker, session/task badges, diff drill-in)'
status: done
priority: 3
created: "2026-11-17"
updated: "2026-11-27"
depends_on:
    - "0558"
spec_refs:
    - docs/design/ios-client.md
---

## Description

Bring project git history to iOS using the ListCommits RPC from 0558 (daemon and web history). Requested on 2026-11-17 in session s_86ed638051f9e53b; implemented in 9d3b011.

## Acceptance criteria

- YccKit client method plus paging/model logic covered by `swift test`.
- The app builds and its tests pass on the Mac, with simulator screenshots of the list and the diff drill-in against the ios-review daemon.

## Outcome

Branch selection, paged history, task/session badges and diff drill-in passed Mac tests and scratch-repository simulator checks; the owner confirmed git history works on device.

Commit: Close the iOS git history device review
