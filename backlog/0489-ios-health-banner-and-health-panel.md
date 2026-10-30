---
id: "0489"
title: 'iOS: health banner and Health panel'
status: in_review
priority: 2
created: "2026-10-29"
updated: "2026-10-29"
depends_on:
    - "0487"
spec_refs: []
---

## Description
When any daemon health check (GetHealth) is warn/fail, the iOS home session list shows a banner naming the failing checks; tapping opens a Health sheet listing every check with status, detail, remedy, last-checked time, and a "Check now" action (GetHealth refresh=true). Refreshes on foreground and pull-to-refresh. No banner when everything is ok.

## Acceptance criteria

## Work log
