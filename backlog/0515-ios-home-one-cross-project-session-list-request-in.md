---
id: "0515"
title: 'iOS home: one cross-project session-list request instead of 2 RPCs per project (reliable reopen)'
status: todo
priority: 2
created: "2026-11-09"
updated: "2026-11-09"
depends_on: []
spec_refs:
    - docs/design/ios-client.md
    - docs/remote-api.md
---

## Description
**Evidence (2026-11-09, iOS analytics since 10-14):** 70 of 1,082 iOS visits recorded RPC failures, almost always at the moment of foregrounding, in bursts of about 20 at the same instant: `rpc.ListSessionHistory` 209 (deadline_exceeded 118, unavailable 91) and `rpc.GetWorkLoop` 111. The home list fans out two unary calls per project (`SessionListModel.addFetches`: ListSessionHistory + GetWorkLoop), so 22 concurrent requests for 11 projects, each with the 20s unary deadline. On a cold or tunnelled mobile link they fail together, and the list stays stale or shows errors. The web client fans out the same way (`clients/web/src/api/queries.ts`).

**Change:**
- Add one daemon RPC (or a cross-project mode of ListSessionHistory) that returns the merged first page across all registered projects, with pinned/follow-up rows and per-session work-loop membership. One round trip replaces 2×N.
- iOS home uses it: show the cached list immediately on foreground, refresh with the single call, and back off quietly on failure instead of retrying every project. Per-project pagination keeps working for scoped views.
- Web may adopt it, but that isn't required for this task.

**Acceptance:**
- A home refresh issues one list request regardless of project count (test with a fake source).
- Ordering, pinned and unread rows, and loop badges match the current merged result (shared fixture).
- Foregrounding with the daemon unreachable shows the cached list with a quiet stale indicator and records at most a few errors per visit.
- Proto regenerated for Go/web/Swift and committed.
- Verified on the configured Mac simulator. Left in_review for on-device use.

## Acceptance criteria

## Work log
