---
id: "0188"
title: 'iOS: usage & budget views (GetUsage / GetBudget)'
status: todo
priority: 4
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0180"
spec_refs:
    - "20.5"
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Cost visibility from the phone per `docs/design/ios-client.md` §6 phase 3 step 9 (spec §20.5).

## Description
- Usage view: `GetUsage` with groupBy pickers (task | model | session | day) and a since/until date filter; rows show token classes, cost, priceStatus (priced/unpriced/partial); totals row.
- Budget view: `GetBudget` caps (session/loop cost + tokens) with "unlimited" rendering for zeros.
- int64-as-string token counts handled by the generated client.

## Acceptance criteria
- Grouped usage renders correctly against a daemon with real usage data, including unpriced/partial rows.
- Date filters round-trip; empty usage shows a sane empty state.
- View-model logic under `swift test`.

## Work log
