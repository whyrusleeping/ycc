---
id: "0488"
title: 'Web: health banner and Health panel'
status: in_review
priority: 2
created: "2026-10-29"
updated: "2026-10-29"
depends_on:
    - "0487"
spec_refs: []
---

## Description
When any daemon health check (GetHealth) is warn/fail, the web shell shows a banner naming the failing checks; clicking opens a Health panel listing every check with status, detail, remedy, last-checked time, and a "Check now" action (GetHealth refresh=true). Polls about once a minute. No banner when everything is ok.

## Acceptance criteria

## Work log
