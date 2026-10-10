---
id: "0404"
title: 'iOS perf phase 3: fast session open (cache, smaller first page, off-main markdown, off-main stream decode)'
status: done
priority: 2
created: "2026-09-28"
updated: "2026-10-10"
depends_on:
    - "0402"
spec_refs: []
---

## Description
From 2026-09-28 audit. Opening a session = GetSessionView (200 rows/384KiB) → decode → eager VStack layout of all rows with MarkdownText parsing + AttributedString(markdown:) inside body, then Subscribe; reopening repeats everything; pushing a diff view may tear down the stream via onDisappear.
- Small LRU of SessionViewModels/projections in AppModel; reopen shows cached content instantly and resubscribes from lastPersistedSeq.
- Smaller initial page (~40–60 rows) with background earlier-page load (paging exists).
- Precompute markdown blocks/AttributedStrings off-main (in the detached decode) or cache by text hash; ToolRowView parses JSON once.
- Stop stream only on real pop, not when covered by a pushed view.
- Stream frames decode on main (connect-swift delegateQueue .main): custom HTTPClientInterface with background delegate queue; make RetryGuard unary queue concurrent.
- Split hot vs cold observable state in SessionViewModel so 25ms publishes don't re-evaluate the whole SessionView body.
- Keep eager VStack unless device evidence says otherwise (see 0378). Needs on-device Instruments before/after.

## Acceptance criteria

## Work log

2026-09-28 — Implemented (uncommitted, iOS convention; builds on uncommitted 0402/0403). **Linux-harness evidence only; no Xcode build, no device run.**
- Session model cache: `SessionModelCache` (YccKit, 5 entries) in `AppDataCache.sessionModels`, cleared (streams stopped) by `AppDataCache.clear()`; keyed by project+session and transport identity. SessionView `init` only looks up; `onAppear` activates (MRU, eviction of unpresented models, parked-persisted→live adoption) the instance @State kept. A `SessionPresentation` claim per screen: the model streams while claimed and is parked (stream stopped, projection/cursor/optimistic state kept) when the last claim ends — pop, stack replacement (claim dropped with the view), not a pushed diff or a sheet.
- Resume: live, parked ≤5 min → subscribe from `lastPersistedSeq` with no snapshot (daemon ChangesSince replays every row change incl. tombstones); a first catch-up update with >60 upserts is dropped for a bounded revalidating snapshot; a transient drop or older park revalidates (snapshot discarded if its cursor is unchanged, keeping loaded pages/scroll); not-found after a cursor resume catches up once as persisted. Persisted parked sessions always revalidate silently; if the log moved on or a question is pending the model probes live (subscribe from cursor; not-found → persisted).
- First page 50 rows / 96 KiB (was 200 / 384 KiB); one first-page-sized earlier page is prefetched after first display and installed synchronously by Load earlier (no auto-prepend); fetched pages re-apply the Load-earlier anchor on arrival.
- Markdown: block splitter moved verbatim into `MarkdownBlocks` (YccKit) with bounded caches; the detached decode task pre-splits and, via an app-registered hook, pre-parses `AttributedString`s; `MarkdownText.body` only looks up. ToolRowView computes its preview once.
- Transport: `RetryGuardHTTPClient` hops stream delegate callbacks to one serial background queue (order preserved; retry guard and metrics stay on main) and delivers unary responses on a concurrent queue.
- Observation split: `projection` is unobserved; `durableRows`, `liveTails` and `chrome` are separately observed mirrors written only on change; SessionView's transcript content and follow trigger moved into child views, so streamed tails no longer re-evaluate toolbar/banners/menus or the durable ForEach. Eager VStack kept (0378).
- Tests: +34 YccKit tests (SessionModelCacheTests, MarkdownBlocksTests); Docker swift:6.2-noble harness 500 tests, only the 2 known Linux failures (DiffFormatter CRLF, EventContract testdata path); cache/view-model suites stable over 5 reruns; seeded mutations of resume/eviction/probe/catch-up bound fail the tests. Review fixes applied (persisted always revalidates + live probe, catch-up bound, synchronous resume state, revalidating retry, side-effect-free init, smaller prefetch).
- Linux-only component timing (release, AMD EPYC 9684X; NOT iOS evidence): splitting 400 real model_turn texts (506 KB, 1878 blocks) 112.8 ms median uncached vs 3.4 ms from cache; largest 21 KB text 5.5 ms.
- Unverified: App target compile/run in Xcode, stream decode off main at runtime (compile-only on Linux), SwiftUI body-count reduction, off-main `AttributedString(markdown:)`, sheet presentation for a question already pending on reopen (deferred ~450 ms past the push). Residual risks: a reopened cached model lays out every row it accumulated (loaded pages + streamed) eagerly; a covered session keeps streaming. On-device checks: plans/ios-client-smoke.md §7 plus the 0378 Instruments before/after.
