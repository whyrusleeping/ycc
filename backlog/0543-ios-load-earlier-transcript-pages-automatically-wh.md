---
id: "0543"
title: 'iOS: load earlier transcript pages automatically when scrolling to the top'
status: in_review
priority: 3
created: "2026-11-11"
updated: "2026-11-11"
depends_on: []
spec_refs:
    - docs/design/ios-client.md
---

## Description
User request: on mobile, long sessions shouldn't need a manual tap on "Load earlier" at the top; scrolling up should page history in.

## Done (uncommitted, awaiting on-device use)
- SessionView: `autoLoadEarlierIfAtTop()` runs `loadEarlierRows()` (the same anchor-preserving path as the button) when the top of the mounted history comes within 80 pt, but only after the finger lifts (a drag clears the restore anchor) and only from user scrollback (`!isFollowingLatest && !isBrowsingEarlier`, so an unread-reply open, a restored page, or an expanded run can't chain-load). Triggered from top-geometry movement, drag end, and the drag watchdog. Analytics `transcript.load_earlier` via `gesture`.
- The row shows a spinner while a fetched page loads (`SessionViewModel.isLoadingEarlier`, now observable); the button stays for VoiceOver and retry.
- SessionViewModel: every installed earlier page prefetches the next one at once, full-size; an explicit load cancels an in-flight prefetch for the same cursor.
- Docs: docs/design/ios-client.md, plans/ios-client-smoke.md step 4.

## Verification
- Mac: `swift test` (YccKit) 601/601 pass, including new `testLoadedEarlierPagePrefetchesTheNextFullPage` (the first run hit the known 0535 GatedBacklogSource crash); `xcodebuild` simulator build succeeded.
- 2026-11-12 fix: on device it never fired. The top-edge preference always read NaN (sibling subtrees reduced their default in over the measured value), so `lastTopY >= -80` was never true. `TranscriptTopPreferenceKey.reduce` now ignores NaN. Simulator (iOS 27): flinging up a 2.5k-event session paged in by itself back to "Session started"; Jump to latest and scrolling back down both resumed follow.
- Still to do: on-device: fling up through several pages of a long session; check that the reading row stays put, momentum stops cleanly at each page, holding a finger at the top waits until release, and Jump to latest still works.

## Acceptance criteria

## Work log
