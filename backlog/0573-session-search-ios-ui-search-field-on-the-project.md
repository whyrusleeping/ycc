---
id: "0573"
title: 'Session search iOS UI: search field on the project session list, open at the matching row'
status: todo
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


## Work log
