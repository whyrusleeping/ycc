---
id: "0305"
title: 'iOS: render work-loop waiting state (resume time + wait kind)'
status: in_review
priority: 3
created: "2026-08-08"
updated: "2026-08-08"
depends_on:
    - "0295"
spec_refs: []
---

## Description
Task 0295 taught the daemon work loop to enter a live `waiting` state when a loop session dies on a retryable provider failure (subscription/usage limit, overload, network), with escalating retries up to 8h patience and auto-resume. `WorkLoopInfo` gained `resume_at` (RFC3339) and `wait_kind`; the state string can now be `waiting`. Swift protos are already regenerated.

The iOS work-loop screen should:
- Treat `waiting` as a live state (same as running/stopping) for polling/observation and the start/stop button state.
- Render the waiting status clearly: e.g. "Waiting for provider (rate_limit) — resumes 03:15" using `resume_at`/`wait_kind`.

Acceptance:
- YccKit work-loop model treats state `waiting` as live, exposes resume time + kind; headless unit tests cover it.
- Work-loop view renders the waiting row; stop works during waiting.
- Builds/tests on-device per iOS convention (ends in_review awaiting on-device use).

## Acceptance criteria

## Plan

Render the daemon work loop's `waiting` state (task 0295: retryable provider failure, auto-resume) in the iOS client. Only three files change, all currently clean in the tree: WorkLoopModel.swift, WorkLoopView.swift, WorkLoopModelTests.swift. No proto changes (Swift protos already regenerated with `resumeAt` field 16 / `waitKind` field 17 on WorkLoopInfo).

1. YccKit — clients/ios/YccKit/Sources/YccKit/WorkLoopModel.swift:
   - Add `case waiting` to `WorkLoopState` (parses via rawValue from the lowercased wire string, matching daemon state "waiting"). Title: "Waiting".
   - Gates: `isActive` → running | waiting | stopping (drives polling + stop-button visibility); `canStop` → running | waiting (daemon StopWorkLoop wakes a waiting loop immediately — workloop.go:234 accepts stop for running/waiting/stopping); `canStart` unchanged (none|finished — daemon rejects start while waiting).
   - Add pure helpers for the waiting row so they're headless-testable:
     * `static func resumeAtDate(for loop:) -> Date?` — parse `loop.resumeAt` via the existing RFC3339 parsers (reuse `parseTimestamp`).
     * `static func waitingLine(for loop:, now: Date = Date()) -> String` — e.g. `Waiting for provider (rate_limit) — resumes 03:15`; omit the parenthetical when `waitKind` is empty, omit the "— resumes …" clause when `resumeAt` is empty/unparseable; format the resume time as a localized short time (and include a relative/absolute form sensibly). Mirror the TUI's wording (internal/tui/workloop.go:81-87 "loop waiting for provider (kind); resumes 15:04") but iOS-styled. Known wait kinds ride as raw strings: rate_limit | overloaded | network | server_error (from internal/engine/apierror.go) — display raw kind, no mapping table needed.
2. App — clients/ios/App/WorkLoopView.swift:
   - Header: when `model.state == .waiting`, render a loud-but-calm waiting row, e.g. `Label(WorkLoopModel.waitingLine(for: loop), systemImage: "hourglass")` in orange, below the totals line (like the finished/outcome row).
   - `WorkLoopStateBadge`: add `.waiting` → orange (or yellow; distinct from stopping's orange — use yellow for waiting OR swap: keep waiting orange and it's fine since stopping is transient; pick yellow to keep them distinct).
   - Stop button already gates on `model.state.isActive` / `canStop`, so it appears and works during waiting once the enum gates are updated — verify no other switch statements over WorkLoopState need a new case (the badge switch is exhaustive → add the case).
   - Polling continues during waiting automatically via `shouldPoll == isActive`.
3. Tests — clients/ios/YccKit/Tests/YccKitTests/WorkLoopModelTests.swift:
   - "waiting" parses to `.waiting`; `isActive`/`shouldPoll`/`canStop` true, `canStart` false.
   - `waitingLine` composition: kind + resume time present; kind-only; neither (bare "Waiting for provider"); unparseable resume_at ignored.
   - `resumeAtDate` parses plain and fractional RFC3339.
   - Model-level: refresh applying a waiting snapshot keeps polling and allows stop; stop from waiting applies the stopping snapshot (reuse the fake source pattern).

Constraints: do NOT touch any file already modified in the working tree (many other in_review tasks live there uncommitted). No Swift toolchain here — write carefully; `swift test` happens on the user's Mac. Task ends in_review, changes left uncommitted per iOS convention.

### Starting points
- clients/ios/YccKit/Sources/YccKit/WorkLoopModel.swift — WorkLoopState enum (lines 17-46), parseTimestamp/isoWithFraction helpers (219-238)
- clients/ios/App/WorkLoopView.swift — header() (~189), WorkLoopStateBadge switch (~304-324), stop button gates on state.isActive/canStop (~174-183)
- clients/ios/YccKit/Tests/YccKitTests/WorkLoopModelTests.swift — fake WorkLoopSource + loop(...) builder pattern
- proto/ycc/v1/ycc.proto:655,668-669 — state: running|waiting|stopping|finished; resume_at RFC3339; wait_kind
- internal/tui/workloop.go:81-87 — TUI wording: 'loop waiting for provider (kind); resumes 15:04'
- internal/engine/apierror.go — wait kinds: rate_limit, overloaded, network, server_error
- internal/session/workloop.go:234 — stop accepted for running|waiting|stopping

## Work log
- 2026-08-08 plan: Render the daemon work loop's `waiting` state (task 0295: retryable provider failure, auto-resume) in the iOS client. Only three files change, all currently clean in the tree: WorkLoopModel.swift, Wor
…[truncated]
- 2026-08-08 context hints: 7 recorded with plan
- 2026-08-08 context hints: clients/ios/YccKit/Sources/YccKit/WorkLoopModel.swift — WorkLoopState enum lines 17-46; parseTimestamp/iso formatters lines 219-238; clients/ios/App/WorkLoopView.swift — header() ~189-216; WorkLoo
…[truncated]
- 2026-08-08 preload: 3 file(s), ~33 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented Task 0305 in exactly the three permitted files, left uncommitted: - Added `WorkLoopState.waiting` with polling/active semantics, stop eligibility, and title support. - Added RFC3339 `resum
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): revise — The Swift implementation itself correctly recognizes `waiting` as active/stoppable, keeps polling, formats and renders the provider wait details, and adds appropriate headless model tests. However, th
…[truncated]
- 2026-08-08 revision: Updated only `clients/ios/App/WorkLoopView.swift`: the stop confirmation now detects `.waiting` and explains that stopping ends the provider wait immediately; running/stopping states retain the existi
…[truncated]
- 2026-08-08 review (sol): revise — The waiting-state implementation remains correct, and the revised confirmation text now accurately describes stopping while waiting. The prior blocking lifecycle finding is still unresolved: the task
…[truncated]
- 2026-08-08 review (sol): accept — The change now satisfies the task: `waiting` is modeled as a live, stoppable, polling state; resume time and wait kind are exposed and rendered; the waiting stop flow has accurate confirmation copy; h
…[truncated]
- 2026-08-08 usage: 715,123 tok (in 401,388, out 17,287, cache_r 1,138,399, cache_w 42,776) · cost n/a (unpriced)
  reviewer:sol: 397,723 tok (in 215,890, out 4,425, cache_r 177,408, cache_w 0) · cost n/a (unpriced)
  implementer: 308,814 tok (in 185,464, out 4,310, cache_r 119,040, cache_w 0) · cost n/a (unpriced)
  coordinator: 8,586 tok (in 34, out 8,552, cache_r 841,951, cache_w 42,776) · cost n/a (unpriced)
