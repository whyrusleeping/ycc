---
id: "0573"
title: 'Session search iOS UI: search field on the project session list, open at the matching row'
status: done
priority: 3
created: "2026-11-21"
updated: "2026-11-21"
depends_on:
    - "0571"
spec_refs: []
---

## Description

**Status:** accepted (todo). It waits on 0571 (session search daemon RPC). Split from 0570 (full-text session search) on 2026-11-21; it delivers 0570's iOS outcome. It is a separate task because iOS work is built and checked on the configured Mac.

## Acceptance criteria

- [ ] A search field on the project's session list queries `SearchSessions` and shows hits with highlighted snippets. While the index is still building, it says so.
- [ ] Selecting a hit opens the session scrolled to the matching row, loading earlier pages when the row is far back.
- [ ] The change builds and is checked in a simulator on the configured Mac.

## Outcome

The project session list has a native search field (SearchSessions, SessionSearchModel: 2+ chars, debounced, re-polls every 2 s while indexing). It shows up to 30 hits with highlighted snippets, who wrote each, matching-message counts, and a "still indexing (n of m sessions)" note. A hit opens SessionView at its row: SessionViewModel.seekRow pages earlier history in, and it handles cached reopen, a parked model, superseded pages and revalidation eviction. A banner shows while paging, and the row is tinted briefly. Also fixed a HEAD compile error in requestQuestionSheet (@MainActor func current). Verified on the configured Mac: build, xcodebuild test, 658 YccKit tests, and simulator checks of hits, the indexing note, and an earlier-page hit.

Commit: ios: message search on the project session list, open a hit at its matching row
