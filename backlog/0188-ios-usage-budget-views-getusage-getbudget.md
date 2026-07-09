---
id: "0188"
title: 'iOS: usage & budget views (GetUsage / GetBudget)'
status: done
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

## Plan

Usage & budget views for the iOS app (docs/design/ios-client.md §6 phase 3 step 9, spec §20.5).

1. YccKit:
   - YccClient wrappers: `getUsage(project:groupBy:since:until:)` → (rows, total, workspace); `getBudget()` → GetBudgetResponse.
   - `UsageModel` (@MainActor @Observable, injectable source protocol): groupBy selection (task | model | session | day — also agent since the proto supports it; single-select picker is fine), optional since/until dates (format YYYY-MM-DD), refresh, rows + total, price-status annotation helper, cost/token formatting helpers (abbreviated token counts like 1.2M, $ with 2-4 decimals; check how the TUI/`ycc cost` formats and mirror sensibly), errorMessage/unauthorized.
   - `BudgetModel` (or fold into UsageModel): loads GetBudget; formats each cap with "Unlimited" for 0.
   - Tests: groupBy request mapping, date filter round-trip formatting, row label selection per grouping, priced/unpriced/partial rendering data, unlimited-vs-set budget formatting, error surfacing.
2. App:
   - UsageView: toolbar/segmented groupBy picker, optional date-range filter UI (two DatePickers with on/off toggles or a menu), List of rows (label per grouping, token totals, cost, price-status badge for unpriced/partial), totals row pinned/last, empty state, pull-to-refresh.
   - BudgetView (can be a section within the same screen): session/loop cost + token caps, "Unlimited" rendering for zeros.
   - Entry point: a "Usage" toolbar item on LandingView (e.g. chart icon) pushing the view with project context.
   - Unauthorized routes to connect; errors inline.
3. Verify: swift test; xcodegen + xcodebuild simulator build; extend plans/ios-client-smoke.md with usage/budget smoke steps.

### Starting points
- proto/ycc/v1/ycc.proto — GetUsageRequest/UsageRow/GetBudgetResponse shapes (group_by: task|model|session|agent|day; price_status priced|unpriced|partial; 0 = unlimited caps)
- docs/remote-api.md §GetUsage/§GetBudget — wire examples
- clients/ios/YccKit/Sources/YccKit/YccClient.swift — wrapper style
- clients/ios/YccKit/Sources/YccKit/BacklogModel.swift — latest @Observable + source-protocol pattern
- clients/ios/App/BacklogView.swift — view style; LandingView toolbar entry-point pattern
- internal/tui or ycc cost CLI (rg 'GetUsage' internal/) — how token counts/cost are formatted elsewhere
- plans/ios-client-smoke.md — extend

## Work log
- 2026-07-08 plan: Usage & budget views for the iOS app (docs/design/ios-client.md §6 phase 3 step 9, spec §20.5).  1. YccKit:    - YccClient wrappers: `getUsage(project:groupBy:since:until:)` → (rows, total, worksp
…[truncated]
- 2026-07-08 context hints: 7 recorded with plan
- 2026-07-08 context hints: proto/ycc/v1/ycc.proto — GetUsageRequest/UsageRow/GetBudgetResponse (group_by: task|model|session|agent|day; price_status priced|unpriced|partial; 0 = unlimited caps); docs/remote-api.md §GetUsage/
…[truncated]
- 2026-07-08 implementer report: Implemented task 0188 — iOS usage & budget views (GetUsage / GetBudget).  ## Changes - **YccKit/YccClient.swift**: added `getUsage(project:groupBy:since:until:)` → (rows, total, workspace) and `ge
…[truncated]
- 2026-07-08 review tier: single-opus — reviewers: claude
- 2026-07-08 review (claude): accept — The change fully implements task 0188. YccClient gains getUsage/getBudget wrappers that correctly build requests and map errors; UsageModel is a well-structured @MainActor @Observable view model with 
…[truncated]
- 2026-07-08 decision: accept — commit: iOS: usage & budget views — grouped GetUsage breakdown with date filters, spend-guard caps (task 0188)
- 2026-07-08 usage: 26,489 tok (in 110, out 26,379, cache_r 2,128,586, cache_w 101,893) · $2.8915
  implementer: 17,607 tok (in 60, out 17,547, cache_r 1,189,834, cache_w 55,743) · $1.3823
  coordinator: 4,960 tok (in 18, out 4,942, cache_r 616,063, cache_w 15,787) · $1.0607
  reviewer:claude: 3,922 tok (in 32, out 3,890, cache_r 322,689, cache_w 30,363) · $0.4485
