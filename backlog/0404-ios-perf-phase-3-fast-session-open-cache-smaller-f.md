---
id: "0404"
title: 'iOS perf phase 3: fast session open (cache, smaller first page, off-main markdown, off-main stream decode)'
status: todo
priority: 2
created: "2026-09-28"
updated: "2026-09-28"
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
