---
id: "0572"
title: 'Session search web UI: search sessions from the list and palette, open at the matching row'
status: todo
priority: 3
created: "2026-11-21"
updated: "2026-11-21"
depends_on:
    - "0571"
spec_refs:
    - docs/design/web-client.md
---

## Description
**Status:** accepted (todo). It waits on 0571 (session search daemon RPC). Split from 0570 (full-text session search) on 2026-11-21; it delivers 0570's web outcome.

## Acceptance criteria

- [ ] The session list search and the command palette query `SearchSessions` for the current project and show hits with highlighted snippets. While the index is still building, they say so.
- [ ] Selecting a hit opens the session scrolled to the matching row, paging earlier history in when the row is far back.
- [ ] `docs/design/web-client.md` describes the search UI.


## Work log
