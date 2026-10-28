---
id: "0485"
title: 'iOS ActivityView: move to the Theme palette'
status: todo
priority: 3
created: "2026-10-28"
updated: "2026-10-28"
depends_on:
    - "0483"
    - "0475"
spec_refs: []
---

## Description
The 0483 iOS restyle skipped clients/ios/App/ActivityView.swift because it carried heavy uncommitted Activity-view work (0475/0477) from another session. It still uses .blue/.purple/.orange/.teal for state. Once that work is committed, switch it to Theme (attention/live/danger/info, StatusWord, mono metadata) like the rest of the app.

Acceptance: no ad-hoc system status colours left in ActivityView.swift; Mac build + tests pass; screenshot of the Activity view in light/dark reviewed.

## Acceptance criteria

## Work log
