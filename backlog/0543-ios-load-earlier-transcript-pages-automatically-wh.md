---
id: "0543"
title: 'iOS: load earlier transcript pages automatically when scrolling to the top'
status: done
priority: 3
created: "2026-11-11"
updated: "2026-11-27"
depends_on: []
spec_refs:
    - docs/design/ios-client.md
---

## Description

User request (session s_2fe4458091f50595, event 2, 2026-11-11): on mobile, long sessions shouldn't need a manual tap on "Load earlier" at the top; scrolling up should page history in.

## Outcome

71995be and ce47ce4 implement automatic older-page loading and fix the top-edge measurement. The simulator reached the oldest page and resumed following; the owner confirmed transcript paging works on device.

Commit: Close automatic transcript paging after device review
