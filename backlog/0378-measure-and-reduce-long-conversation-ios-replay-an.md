---
id: "0378"
title: Measure and reduce long-conversation iOS replay and rendering costs
status: done
priority: 2
created: "2026-09-09"
updated: "2026-10-10"
depends_on:
    - "0376"
    - "0377"
spec_refs: []
---

## Description
Split from accepted task 0343. Complete measured iOS long-conversation performance work after diagnostics and paging.

Acceptance criteria:
- Measure representative large conversations; record before/after transcript decode/replay/first-display and rendering timings, counts, and Instruments evidence.
- Address measured replay/MainActor and rendering bottlenecks; window expensive transcript rendering based on evidence.
- Preserve scroll anchoring, live streaming, question visibility, unread/routing semantics and replay correctness.
- SessionView intentionally uses eager VStack to avoid a documented LazyVStack/streaming blank-transcript geometry regression: do not blindly restore LazyVStack.
- Swift tests at useful behavioral boundaries and on-device Instruments/scroll-streaming verification on user's Mac. Do not claim source audit or Linux timings as end-to-end iOS measurements.

## Acceptance criteria

## Plan

State at start: most structural work already landed (0343/0376/0377 and earlier): live/persisted YccClient sessions use the daemon's indexed GetSessionView (200 rows / 384 KiB pages, Load earlier via GetSessionViewPage), legacy full-event replay folds in Task.detached, stream updates coalesce to one publish per 25 ms, rows are Equatable, SessionView keeps an eager VStack (documented LazyVStack streaming regression — do NOT change), and OSSignposter spans exist for transcript fetch/decode/replay/firstDisplay. What remains is evidence-driven reduction plus on-device verification.

Remaining MainActor work visible in source: SessionProjection.installIndexed / prependIndexed / applyIndexed run on the MainActor and decode every presentation row by building a throwaway SessionProjection and JSONSerialization-parsing each event's data_json; prependIndexed and applyIndexed do durableRows.firstIndex(where:) per row (O(page × loaded)).

Steps:
1. Evidence (Linux component benchmark, not iOS): generate realistic GetSessionView / GetSessionViewPage payloads from the largest local session logs (.ycc/sessions/*/events.jsonl, e.g. s_2d2615834256b837 3.5k events / 10 MB) via a throwaway Go program or test using internal/sessionview's store (serialize the proto responses to files; do not commit large fixtures). Build a scratch SPM harness in Docker swift:6.2-noble (swift-protobuf + generated ycc.pb.swift + the pure YccKit sources needed by SessionProjection; stub anything Connect/UI) and time: installIndexed of a first page, repeated prependIndexed to all pages, applyIndexed streaming bursts against a large loaded durableRows, and legacy full replay fold. Record median timings.
2. Fix what the numbers show, keeping semantics: expected candidates — decode indexed rows off the MainActor (pure static decode in a detached task, cancellation + generation checks after the await like applyReplay, then a cheap main-actor install that still applies version/tombstone/pending/detail rules), and/or an id→index lookup for prepend/upsert if the quadratic cost is material. Skip any change the measurements don't justify.
3. Swift tests at behavioural boundaries in YccKitTests (SessionViewModelTests/SessionProjectionTests): off-main install preserves ordering, detail preservation, stale-response/generation rejection, pending question visibility, lastPersistedSeq/unread watermark, and Load-earlier merge. Run them in the Docker harness.
4. Re-measure after; record before/after Linux component timings in the task work log, explicitly labelled as NOT end-to-end iOS. Add an on-device Instruments checklist (signposts to capture, large-session scroll/streaming/Load-earlier/question steps) to plans/ios-client-smoke.md if not already present.
5. On-device Instruments + scroll/streaming verification needs the user's Mac; task goes to in_review after commit awaiting that.

### Starting points
- clients/ios/YccKit/Sources/YccKit/SessionViewModel.swift: startIndexedLoop (~L258), loadEarlierRows (~L90), publishPendingUpdates (~L754), applyReplay (~L798, detached-fold pattern to copy)
- clients/ios/YccKit/Sources/YccKit/SessionProjection.swift: installIndexed/prependIndexed/applyIndexed/decodeIndexedRow (~L749-980)
- internal/sessionview/store.go: View/EarlierPage, DefaultRows=200; internal/server/server.go GetSessionView ~L380 shows how rows→proto (presentationRow)
- clients/ios/YccKit/Sources/YccKit/LatencyDiagnostics.swift: spans/signposts
- memory: Docker swift:6.2-noble scratch SPM harness works for pure YccKit (6.0 too old); Linux lacks SwiftUI/AttributedString(markdown:)

## Work log

2026-09-26 — **Linux component measurements only; NOT iOS first-display/rendering/Instruments evidence.** A throwaway Go driver indexed three local JSONL logs with `internal/sessionview.Store.View/EarlierPage`, serialized Swift-compatible protobuf page/event payloads in ignored `.ycc/tmp/bench0378`, and a scratch SPM executable in Docker `swift:6.2-noble` (Swift 6.2.4, x86-64 AMD EPYC 9684X) measured medians of nine release-mode runs. Largest log `s_2d2615834256b837`: 3479 events/1420 rows, first page 167 rows + 8 pages (1253 rows); `s_7599fb8fcf0deef8`: 2280/914, first 168 + 5 (746); `s_1f3d838012dad27a`: 1934/773, first 180 + 4 (593). Session text and large fixtures are not committed.

| Component median ms (three logs in order above) | Before | After |
| --- | --- | --- |
| First page proto decode | 0.50 / 0.52 / 0.52 | 0.49 / 1.29 / 1.43 |
| First-page projection decode + install on calling thread | 4.16 / 4.47 / 4.07 | 0.06 / 0.07 / 0.07 projection install from predecoded rows |
| Install + prepend every earlier page on calling thread | 43.97 / 27.61 / 20.14 | 5.73 / 2.44 / 1.81 projection merge from predecoded rows |
| First page off-thread decode + MainActor merge, wall time | n/a | 4.52 / 11.79 / 10.59 |
| All earlier pages off-thread decode + MainActor merge, wall time | n/a | 94.30 / 60.21 / 41.67 |
| 20 loaded-row streaming upserts (1420/914/773 loaded) | 0.62 / 0.63 / 0.39 | 0.62 / 0.63 / 0.38 |
| Legacy full replay fold (whole event log) | 101.06 / 49.99 / 40.96 | 103.49 / 48.64 / 39.71 |

Off-thread work reduces the projection install/merge component's UI-executor occupation, **not necessarily elapsed page load time**: the detached-task wall medians vary with Linux scheduling (and all-page wall times increased). The 20-row live burst was <1 ms and an id→index map would add mutable state without measured benefit, so live-upsert merging stays as is. Index pages now decode away from the MainActor and reconcile version/tombstone, buffered upsert, loaded detail and pending state on installation; stale generation/cursor and replaced snapshot results are discarded/retried. Eager `VStack` and the existing 200-row initial presentation remain: no Linux proxy measures SwiftUI layout, so there is no evidence to justify altering rendering/scroll anchoring. Mac device Instruments, first-display, scroll/stream/Load-earlier/question validation remain outstanding; see `plans/ios-client-smoke.md` before closing review.
- 2026-09-26 coordinator: standard review (sol) accepted the off-main indexed decode (snapshot + Load earlier) with generation/cursor/revision guards; implementer's Docker swift:6.2 harness ran 104 projection/view-model tests green (not independently reproduced). Changes left UNCOMMITTED per the iOS in_review convention. Remaining before done: Xcode build + `swift test` in clients/ios/YccKit, and the on-device Instruments before/after run and scroll/streaming/Load-earlier/question checks in plans/ios-client-smoke.md; decide on any rendering windowing only from that device evidence.
