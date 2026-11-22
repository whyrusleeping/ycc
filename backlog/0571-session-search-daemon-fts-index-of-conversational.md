---
id: "0571"
title: 'Session search daemon: FTS index of conversational text and SearchSessions RPC'
status: done
priority: 3
created: "2026-11-21"
updated: "2026-11-21"
depends_on: []
spec_refs:
    - spec.md#18.6 Session history and reopen
---

## Description

**Status:** in progress. Split from 0570 (full-text session search) on 2026-11-21; it delivers 0570's daemon outcome: the index, the RPC and the spec contract. The web (0572) and iOS (0573) clients depend on it. It is a separate task because both clients build on the RPC, and the iOS client has its own Mac build-and-check cycle.

## Acceptance criteria

- [ ] A query matches text from a session that has never been opened, once its backfill completes.
- [ ] Hits carry a session summary, row id, kind, a snippet with match spans, and the matching-row count.
- [ ] The index follows appends to live logs and rebuilds after a schema bump, a deleted database, or a replaced log.
- [ ] Tool calls, tool results and thinking are not indexed.
- [ ] Spec §18.6 describes the search contract.

## Outcome

Per-project FTS5 index at .ycc/session-search.sqlite (WAL, owner-only, schema-versioned) follows each log from a durable watermark with the presentation index's source checks. It indexes user input, coordinator text and idle reports, and questions with their human answers, each on its presentation row id. SearchSessions refreshes every history session in the background, waits up to 1.5 s, and returns one bm25-ranked hit per session: summary, row id, kind, highlighted snippet spans and match count. It reports indexing progress and pages by cursor. A deleted database is reopened and rebuilt. Named-project view reads resolve logs within that project (this also closes 0574). The spec §18.6 contract and the Go/TS/Swift bindings are included, along with the rebuilt web bundle. The Swift bindings have not been built yet; 0573 builds them on the Mac. Also splits 0570 into 0571-0573.

Commit: Session search daemon: FTS5 index of conversational text and SearchSessions RPC
