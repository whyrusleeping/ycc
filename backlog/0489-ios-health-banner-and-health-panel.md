---
id: "0489"
title: 'iOS: health banner and Health panel'
status: done
priority: 2
created: "2026-10-29"
updated: "2026-11-10"
depends_on:
    - "0487"
spec_refs: []
---

## Description
When any daemon health check (GetHealth) is warn/fail, the iOS home session list shows a banner naming the failing checks; tapping opens a Health sheet listing every check with status, detail, remedy, last-checked time, and a "Check now" action (GetHealth refresh=true). Refreshes on foreground and pull-to-refresh. No banner when everything is ok.

## Acceptance criteria

## Work log
- 2026-11-10: Owner-requested simulator review against a scratch daemon whose mock switches health probes between ok, warn and fail, plus an unreachable Exa: no banner when healthy, warn and fail banners, a "+N more" overflow, the sheet with remedies, Check now, the Settings summary, and refresh on foreground and pull. PASS. Fixed one cosmetic defect: the banner's " · " and "+N more" and Settings' "No problems" took the accent tint (`.secondary` inside a Button label), so they now use `Color(.secondaryLabel)`.
