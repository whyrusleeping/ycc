---
id: "0220"
title: Eliminate iOS long-response streaming slowdown
status: in_review
priority: 2
created: "2026-07-20"
updated: "2026-07-20"
depends_on: []
spec_refs:
    - docs/design/ios-client.md#Session view + projection
    - spec.md#Transient (broadcast-only) events
---

## Description
Long streamed model responses become progressively laggier in the iOS session view. The client currently replaces a growing SwiftUI `Text` and deep-compares the complete transcript rows on every full-snapshot `turn_delta`, causing repeated whole-response work.

## Acceptance criteria
- [x] The live-tail renderer appends ordinary prefix-extending snapshots incrementally rather than replacing/re-laying the full text on every delta.
- [x] Retry/replacement snapshots that are not prefix extensions still render correctly.
- [x] Scroll-following reacts through a cheap revision token rather than equality-comparing the complete transcript.
- [ ] Existing projection/view-model tests pass and the iOS app builds.

## Work log
- 2026-07-20: Root cause confirmed: each 10 Hz full snapshot rebuilt a growing SwiftUI `Text`, `onChange(model.rows)` deep-compared the full transcript, and unchanged Markdown rows could be re-evaluated.
- 2026-07-20: Added loss-safe append hints to `turn_delta`, persistent TextKit append rendering with snapshot fallback, separate durable/tail rendering, equatable durable rows, and scalar transcript revisions. Engine tests pass; Swift/iOS verification is blocked in this Linux environment because `swift`/`xcodebuild` are unavailable.
