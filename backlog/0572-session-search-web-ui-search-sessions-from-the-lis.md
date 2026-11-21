---
id: "0572"
title: 'Session search web UI: search sessions from the list and palette, open at the matching row'
status: done
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

## Outcome

The page session list's search field and plain or @ palette queries of 2+ characters query SearchSessions for the current project: the scope, otherwise the last viewed project, otherwise the sole registered one. Requests are debounced, and previous hits are never reused across projects. The list shows up to 30 hits in a "Messages in <project>" section above the loaded title matches. The palette shows up to 8 after its five best fuzzy matches. Each hit shows a highlighted snippet, who wrote it and when; list hits also show the matching-message count. While the index is building, both surfaces show indexed/total counts and re-poll every 2 s. A hit opens /…/s/<id>?row=<row id>. The session pages earlier history in (seekRow) with a banner while it loads, scrolls to the row, unfolds it and marks it. A row no longer present raises a toast. docs/design/web-client.md is updated and the embedded bundle rebuilt. Verified with vitest, typecheck and Playwright against a scratch daemon, including a 3000-session backfill.

Commit: Session search web UI: message hits in the session list and palette, open a hit at its row
