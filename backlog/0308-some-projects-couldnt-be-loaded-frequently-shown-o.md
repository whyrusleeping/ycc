---
id: "0308"
title: “Some Projects couldnt be loaded” frequently shown on reopening the ios app
status: in_review
priority: 3
created: "2026-08-09"
updated: "2026-08-09"
depends_on: []
spec_refs: []
---

## Description

Foregrounding the iOS app can issue overlapping session-list refreshes while
CFNetwork still holds dead pooled connections or a VPN is reconnecting. Because
the history RPCs are POSTs, transient failures are not automatically retried;
one failed project then loses its rows and produces a frequent partial-load
warning until the user refreshes manually.

## Acceptance criteria

- Project-list and per-project history requests retry transient failures twice,
  with injectable delays, but never retry an unauthorized response.
- Concurrent refresh callers share one in-flight refresh and do not duplicate
  the project/history request burst.
- A persistently failing project keeps its last successfully loaded rows,
  routing, and drawer activity entry while still showing the existing warning.
- A first load where every project fails still surfaces a fatal error, and an
  unauthorized load still short-circuits to the reconnect flow.
- Headless model tests cover retry success/exhaustion, cached-row retention,
  unauthorized behavior, and refresh coalescing.

## Plan

## Diagnosis

"Some projects couldn’t be loaded: …" is `SessionListModel.partialWarning`, set in `apply(loads:)` when some per-project `listSessionHistory` calls fail during `refresh()`. On reopening the app:

1. `LandingView.observers` fires `refresh()` when scenePhase becomes `.active` (and `.task`/router-pop can fire it concurrently — overlapping refreshes).
2. iOS tears down pooled keep-alive TCP connections while the app is backgrounded; the parallel burst of per-project POSTs then reuses dead pooled connections and fails immediately (NSURLErrorNetworkConnectionLost et al). CFNetwork does not auto-retry non-idempotent POSTs. Tailscale/VPN re-establishment adds a transient failure window too.
3. So a subset of projects fails transiently → partial warning shown, and those projects' rows vanish from the aggregate list until the next manual refresh.

## Fix (all in YccKit `SessionListModel` — headlessly testable)

1. **Transient retry**: add a small retry helper used by (a) the top-level `source.listProjects()` call and (b) each per-project history load in `refreshAcrossProjects` (and the no-projects fallback). Up to 2 retries with short growing delays (e.g. 0.4s then 1.2s). Never retry `YccError.unauthorized`. Make the delays injectable via an init parameter (e.g. `retryDelays: [TimeInterval]` defaulting to `[0.4, 1.2]`) so tests script failures and run with `[]`-instant or zero delays.
2. **Refresh coalescing**: guard `refresh()` against overlap — keep the in-flight refresh `Task` and have concurrent callers await it instead of starting a second load (double bursts double the transient-failure exposure and can interleave state).
3. **Retain last-known-good rows**: in `apply(loads:)`, when a project fails but had rows from a previous successful load (current `allSessions` + `sessionProjects` routing), keep those rows (and keep it in `loadedProjects` so drawer badges don't vanish). Still set `partialWarning` for a persistent failure — reword nothing; existing text stays.
4. Keep behavior otherwise identical: all-fail → `errorMessage`; unauthorized short-circuits.

## Tests (YccKit `SessionListModelTests`, zero delays)

- Per-project load fails once then succeeds on retry → no `partialWarning`, sessions present (extend `MockListSource` with a scripted fail-N-times-then-succeed mode; it already records `requestedProjects` to assert retry counts).
- Persistent per-project failure → `partialWarning` still set after retries exhausted.
- After a successful refresh, a now-failing project retains its previously loaded rows and routing (and warning is shown).
- `listProjects` transient failure retried → no `errorMessage`.
- Unauthorized is not retried (attempt count stays 1, `unauthorized == true`).
- Overlapping `refresh()` calls coalesce (second call does not double the source calls).

## Verification

No Swift toolchain in this workspace — tests run on the user's Mac (`swift test` in clients/ios/YccKit). Land as `in_review` (implemented, awaiting on-device use, per project convention); leave uncommitted alongside the other in_review iOS work.

### Starting points
- clients/ios/YccKit/Sources/YccKit/SessionListModel.swift — refresh(), refreshAcrossProjects(), apply(loads:), HistoryLoad
- clients/ios/YccKit/Tests/YccKitTests/SessionListModelTests.swift — MockListSource, existing partialWarning tests (~line 260-344)
- clients/ios/App/LandingView.swift:216-250 — observers(): .task + scenePhase + router-pop all trigger refresh()
- clients/ios/YccKit/Sources/YccKit/YccError.swift — unauthorized case must not be retried

## Work log
- 2026-08-09 plan: ## Diagnosis  "Some projects couldn’t be loaded: …" is `SessionListModel.partialWarning`, set in `apply(loads:)` when some per-project `listSessionHistory` calls fail during `refresh()`. On reopen
…[truncated]
- 2026-08-09 context hints: 4 recorded with plan
- 2026-08-09 context hints: clients/ios/YccKit/Sources/YccKit/SessionListModel.swift — refresh() line ~264, refreshAcrossProjects ~303, apply(loads:) ~349, HistoryLoad struct ~34; clients/ios/YccKit/Tests/YccKitTests/SessionLi
…[truncated]
- 2026-08-09 preload: 2 file(s), ~63 KiB seeded into implementer context
- 2026-08-09 implementer report: Implemented Task 0308 and marked it `in_review`.  Changes: - Added injectable foreground retry delays (`[0.4, 1.2]` by default) for project-list, per-project history, and no-project fallback history r
…[truncated]
- 2026-08-09 review tier: single-opus — reviewers: sol
- 2026-08-09 review (sol): accept — The SessionListModel change satisfies the task: project-list, per-project history, and fallback history calls use injectable two-delay retries; unauthorized errors bypass retry; refreshes coalesce thr
…[truncated]
- 2026-08-09 usage: 1,503,089 tok (in 584,362, out 26,823, cache_r 1,300,283, cache_w 85,228) · cost n/a (unpriced)
  implementer: 1,118,287 tok (in 385,924, out 14,539, cache_r 717,824, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 377,270 tok (in 198,424, out 4,766, cache_r 174,080, cache_w 0) · cost n/a (unpriced)
  coordinator: 7,532 tok (in 14, out 7,518, cache_r 408,379, cache_w 85,228) · cost n/a (unpriced)
