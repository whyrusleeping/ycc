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

Status: in review. It was implemented in `9d3b011`, and what remains is a check on a device. 0558 (ListCommits RPC + web History page) is done. Requested by the user on 2026-11-17 in session s_86ed638051f9e53b, the same session as 0558 (event 2; scope answers at event 35). iOS is a separate task because it must be built and checked on the configured Mac and then waits `in_review` for on-device use.

## Acceptance criteria

- YccKit client method plus paging/model logic covered by `swift test`.
- The app builds and its tests pass on the Mac, with simulator screenshots of the list and the diff drill-in against the ios-review daemon.

## Outcome

Branch selection, paged history, task/session badges and diff drill-in passed Mac tests and scratch-repository simulator checks; the owner confirmed git history works on device.

Commit: Close the iOS git history device review
