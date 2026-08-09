---
id: "0272"
title: Stabilize iOS session transcript auto-follow and composer scrolling
status: in_review
priority: 2
created: "2026-07-16"
updated: "2026-08-08"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Investigate and fix the iOS SessionView transcript failing to remain pinned while live output streams and jumping/twitching while the user types. Preserve intentional scroll-up behavior and the jump-to-latest affordance.

## Acceptance criteria
- A transcript that is following the latest content remains pinned as streaming tail text, durable events, keyboard visibility, and multiline composer height change.
- Streaming updates do not queue overlapping scroll animations or visibly twitch/reset the feed.
- An intentional user scroll away from the bottom disables auto-follow and shows the jump-to-latest pill; new events do not yank the transcript.
- Scrolling back to the bottom or tapping the pill resumes following.
- The iOS app builds successfully.

## Plan

Scope: clients/ios/App/SessionView.swift only (the follow-mode machinery). The existing design — bottom-marker geometry preference, coalesced token-based scrollTo, drag-gesture opt-out — stays; we close the gaps that make follow mode drop out and never recover.

Diagnosis of remaining defects vs acceptance criteria:
1. Re-follow on scrolling back to the bottom is missing: `isFollowingLatest = true` is only set by the jump-to-latest pill.
2. Any downward drag component disables follow even when the transcript is at the bottom (rubber-band wiggle), and nothing self-heals — the pill shows spuriously and pinning "randomly" stops.
3. `isDraggingTranscript` can wedge true when the simultaneous DragGesture's `onEnded` is swallowed by the ScrollView's pan (known SwiftUI flakiness). A wedged true value lets transient marker excursions (keyboard animation, tail growth) call stopFollowingLatest without user intent.
4. No corrective pinning: if a coalesced scrollTo lands short (late MarkdownText sizing, keyboard animation frames), isFollowingLatest stays true but the viewport rests above the bottom and nothing re-triggers a scroll until the next event.

Changes:
A. Track drag recency without per-frame view invalidation: add a reference box (like the existing ScrollToken) holding `lastDragActivity: Date` (and keep the `isDraggingTranscript` @State bool). Update the box on every DragGesture.onChanged; define a quiet period constant (~0.35s).
B. onPreferenceChange(TranscriptBottomPreferenceKey) becomes the follow-state reconciler:
   - compute isLatestVisible as today;
   - existing rule: if isDraggingTranscript && !isLatestVisible → stopFollowingLatest();
   - NEW resume rule: if isLatestVisible && !isFollowingLatest && !isDraggingTranscript && quiet period elapsed since lastDragActivity → isFollowingLatest = true (no scroll needed; already at bottom). This both implements "scrolling back to the bottom resumes following" and self-heals a wedged drag flag once the quiet period passes.
   - NEW corrective rule: if !isLatestVisible && isFollowingLatest && !isDraggingTranscript → requestScrollToLatest(proxy:) so a short-landed scroll or late layout can't leave a "following" transcript stranded mid-content. This is bounded: the request is coalesced through the existing token and non-animated, and once the marker is visible the rule stops firing. (This requires the preference-change closure to reach the ScrollViewProxy — restructure so the transcript builder or the handler has access to `proxy`, e.g. pass it into `transcript(proxy:)`.)
C. DragGesture.onEnded: set isDraggingTranscript = false, stamp lastDragActivity, and (fast path) if isLatestVisible → resume following immediately + requestScrollToLatest to settle flush. Also schedule a delayed recheck Task (~0.4s) that, on the main actor, resumes following iff still !isFollowingLatest && isLatestVisible && !isDraggingTranscript — this covers the rubber-band release-at-bottom case where no further preference changes arrive after the settle, and the case where deceleration ends at the bottom in an idle session. Guard the recheck only by current state (it is self-invalidating: if the user dragged away again, isLatestVisible is false).
D. Keep the downward-swipe stopFollowing rule in onChanged, but it now self-heals via B/C, so a bounce at the bottom shows the pill at most briefly (or not at all when onEnded's fast path runs).
E. Update the doc comments to describe the reconciler (follow ⇔ live edge visible, user drags opt out, quiet period arbitrates) — the current comment on isDraggingTranscript references a "marker-driven re-follow" that didn't exist; now it will.
F. No changes to YccKit, the composer, KeyboardObserver, or SessionViewModel. No behavior change to the pill except it now disappears when the user scrolls back to the bottom themselves.

Verification: no Swift toolchain in this environment — verification is by careful code review (single-opus tier) plus swift-syntax sanity reading; the task will be left in_review per project convention (iOS in_review = implemented, awaiting on-device use). Commit selectively: SessionView.swift carries an unrelated uncommitted hunk (0296 memory menu, snapshotted at /tmp/0272-pre.diff), so the commit will be built from HEAD-file + our interdiff via hash-object/update-index, plus the 0272 backlog file — NOT the commit tool (it does git add -A).

### Starting points
- clients/ios/App/SessionView.swift — all changes live here; follow machinery is lines ~24–135 (state + onChange wiring) and ~365–510 (transcript, gesture, requestScrollToLatest, stopFollowingLatest, pill)
- ScrollToken reference-box pattern at line ~44 — reuse this pattern for lastDragActivity to avoid per-frame @State writes (onChange writing state multiple times per frame traps)
- TranscriptBottomPreferenceKey at bottom of file; onPreferenceChange handler at ~line 416
- Do NOT touch the unrelated pending hunk in actionMenu (Memory menu item, task 0296)
- No Swift toolchain here — do not attempt to build; keep the diff surgical

## Work log
- 2026-08-06 renumbered 0215 → 0272 (duplicate id detected, 0215 kept by another task)
- 2026-08-08 plan: Scope: clients/ios/App/SessionView.swift only (the follow-mode machinery). The existing design — bottom-marker geometry preference, coalesced token-based scrollTo, drag-gesture opt-out — stays; we
…[truncated]
- 2026-08-08 context hints: 5 recorded with plan
- 2026-08-08 context hints: All edits go in clients/ios/App/SessionView.swift; do NOT touch the pending unrelated hunk in actionMenu (Memory menu item — belongs to another task) or any other file.; Reuse the ScrollToken refere
…[truncated]
- 2026-08-08 preload: 1 file(s), ~28 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0272 in `clients/ios/App/SessionView.swift` only. Added reference-box drag recency tracking and quiet/stale intervals; threaded `ScrollViewProxy` into the transcript builder; made bot
…[truncated]
- 2026-08-08 revision: Refined `DragGesture.onEnded` in `SessionView.swift` to inspect the gesture value and skip immediate live-edge re-follow when `predictedEndTranslation.height > 0`, preventing a downward fling toward o
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): revise — The follow-state reconciler and non-animated corrective pinning are directionally sound, but the known swallowed-`onEnded` path is not actually self-healing in an idle transcript. That leaves a direct
…[truncated]
- 2026-08-08 revision: Added the requested coalesced drag-inactivity watchdog in `SessionView.swift`. `DragActivity` now tracks whether one watchdog is scheduled; the first `onChanged` starts a single main-actor task whose
…[truncated]
- 2026-08-08 review (sol): revise — The new activity-scheduled watchdog fixes the idle swallowed-`onEnded` case, but live growth can still erase the evidence that the user reached the bottom before either delayed reconciler runs. Conseq
…[truncated]
- 2026-08-08 revision: Addressed both re-review items.  - Extended the transcript scroll reference state with `sawLiveEdge` and `lastTopY`. - Added a padded-content top-edge geometry preference. Recent drag/deceleration off
…[truncated]
- 2026-08-08 revision: Restored the bottom-preference handler’s active-drag classification to `isDraggingTranscript && dragAge < Self.dragActivityStalePeriod`, preventing post-release streaming growth from disabling follo
…[truncated]
- 2026-08-08 review (sol): accept — The revision addresses the outstanding race by preserving live-edge intent across content-only growth and invalidating it on drag/deceleration-driven viewport movement. The inactivity watchdog now cov
…[truncated]
- 2026-08-08 usage: 2,130,880 tok (in 801,466, out 62,982, cache_r 3,773,957, cache_w 208,253) · cost n/a (unpriced)
  implementer: 1,811,823 tok (in 591,979, out 14,596, cache_r 1,205,248, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 281,698 tok (in 209,423, out 11,091, cache_r 61,184, cache_w 0) · cost n/a (unpriced)
  coordinator: 37,359 tok (in 64, out 37,295, cache_r 2,507,525, cache_w 208,253) · cost n/a (unpriced)
