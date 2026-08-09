---
id: "0290"
title: 'iOS: render workstream ready / needs_attention statuses'
status: in_review
priority: 3
created: "2026-08-07"
updated: "2026-08-08"
depends_on:
    - "0251"
spec_refs:
    - docs/design/workstream-integration.md#7. Surfaces
---

## Description
Task 0251 introduced two new non-terminal workstream registry statuses that flow through `WorkstreamInfo.status`: `ready` and `needs_attention` (the latter with a reason recorded in the `workstream_needs_attention` event and the registry's `status_reason`). The iOS `WorkstreamStatus` enum (clients/ios/YccKit/Sources/YccKit/WorkstreamsModel.swift) currently maps unrecognized statuses to `.unknown`, so these rows render indistinctly.

## Acceptance criteria

- [ ] `WorkstreamStatus` gains `ready` and `needsAttention` cases mapped from the proto status strings ("ready", "needs_attention").
- [ ] Workstream rows visually distinguish "still working" (active/session status), "ready", and a loud "needs attention" (mirroring the TUI's ⚠ treatment).
- [ ] Merge affordances that gate on `active` also allow `ready` and `needs_attention` (parity with the daemon's InFlight semantics).
- [ ] YccKit tests updated (`swift test` on the user's Mac; ad-hoc signing per project memory).

## Plan

Render the two new non-terminal workstream statuses (`ready`, `needs_attention`) in the iOS client, with merge-affordance parity to the daemon's gates.

1. YccKit — clients/ios/YccKit/Sources/YccKit/WorkstreamsModel.swift:
   - Add `ready` and `needsAttention` cases to `WorkstreamStatus`. Give `needsAttention` the explicit raw value `"needs_attention"` so the existing `init(status:)` (rawValue on the lowercased string) parses the proto string; `ready` parses via its default raw value.
   - Titles: "Ready", "Needs attention".
   - Replace the single `isActionable` with daemon-accurate gates (verified in internal/session/workstream_merge.go):
     * `isMergeable` — Preview/Merge allowed iff status is InFlight: `active | ready | needsAttention` (registry.go InFlight()). NOTE: `stale` is NOT mergeable on the daemon; the current iOS code wrongly offers merge for stale.
     * `isDiscardable` — Discard allowed for InFlight OR `stale` (DiscardWorkstream gate).
   - Keep `isActionable` only if convenient as a deprecated alias, otherwise remove it and update call sites (it's used only in WorkstreamsView).
2. App — clients/ios/App/WorkstreamsView.swift:
   - Use `isMergeable` for the Preview merge / Merge… context-menu items and `isDiscardable` for swipe-delete + Discard… items.
   - `WorkstreamStatusBadge`: colors for the new cases — `ready` → mint/teal-ish positive tone (distinct from active's green and merged's blue); `needsAttention` → loud red, and prefix the badge text with a warning symbol (e.g. exclamationmark.triangle.fill icon inside the pill) mirroring the TUI's "⚠ needs attention" treatment. For needs-attention rows, keep it visually loud (red pill is sufficient; optionally tint the branch line).
   - Rows for ready/needs_attention should still show session status line as-is (registry status wins in the badge; no other row logic changes needed).
3. Tests — clients/ios/YccKit/Tests/YccKitTests/WorkstreamsModelTests.swift:
   - Parsing: "ready" → .ready, "needs_attention"/"NEEDS_ATTENTION" → .needsAttention, unknown fallback still works.
   - Gates: isMergeable true for active/ready/needsAttention, false for stale/merged/discarded/unknown; isDiscardable true additionally for stale.
   - Titles for the new cases.

Constraints: DO NOT touch any file that is already modified/staged in the working tree (git status is full of other in_review tasks' work) — only WorkstreamsModel.swift, WorkstreamsView.swift, WorkstreamsModelTests.swift, all currently clean. No proto changes (status rides as a string). No Swift toolchain here: code must be written carefully; `swift test` runs on the user's Mac later (task ends in_review per project convention for iOS work... actually acceptance allows commit; leave verification note in report).

### Starting points
- clients/ios/YccKit/Sources/YccKit/WorkstreamsModel.swift — WorkstreamStatus enum + isActionable (lines ~29-56)
- clients/ios/App/WorkstreamsView.swift — rowActions/swipeActions use WorkstreamsModel.status(...).isActionable; WorkstreamStatusBadge at bottom
- internal/workstream/registry.go:51-54 — InFlight = active|ready|needs_attention (NOT stale)
- internal/session/workstream_merge.go:191,239 (merge gates on InFlight), :452 (discard allows InFlight or stale)
- internal/tui/workstreams.go:108-128 wsRowStatus — TUI's '⚠ needs attention' loud treatment

## Work log
- 2026-08-08 plan: Render the two new non-terminal workstream statuses (`ready`, `needs_attention`) in the iOS client, with merge-affordance parity to the daemon's gates.  1. YccKit — clients/ios/YccKit/Sources/YccKit
…[truncated]
- 2026-08-08 context hints: 5 recorded with plan
- 2026-08-08 context hints: internal/workstream/registry.go:51-54 — InFlight = active|ready|needs_attention (NOT stale); internal/session/workstream_merge.go:191,239 merge/preview gate on InFlight; :452 discard allows InFlight
…[truncated]
- 2026-08-08 preload: 3 file(s), ~40 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented Task 0290 in the three permitted iOS files only.  Changes: - Added `ready` and `needsAttention = "needs_attention"` to `WorkstreamStatus`, including the titles “Ready” and “Needs att
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): accept — The change fully satisfies Task 0290. `WorkstreamStatus` now parses and labels `ready` and `needs_attention`; merge and discard gates match daemon semantics, including removing merge from stale while 
…[truncated]
- 2026-08-08 status: in_review — implemented + review-accepted; left UNCOMMITTED per iOS convention (no Swift toolchain here). Remaining: `swift test` in clients/ios/YccKit on the Mac + on-device check of the Workstreams pane (ready badge = teal, needs-attention = red ⚠ pill; merge offered for ready/needs_attention, not stale; discard still offered for stale). Only WorkstreamsModel.swift / WorkstreamsView.swift / WorkstreamsModelTests.swift changed.
- 2026-08-08 usage: 251,358 tok (in 183,539, out 14,059, cache_r 990,729, cache_w 37,932) · cost n/a (unpriced)
  implementer: 159,438 tok (in 109,122, out 3,724, cache_r 46,592, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 82,611 tok (in 74,377, out 1,066, cache_r 7,168, cache_w 0) · cost n/a (unpriced)
  coordinator: 9,309 tok (in 40, out 9,269, cache_r 936,969, cache_w 37,932) · cost n/a (unpriced)
