---
id: "0497"
title: 'Web: declutter the sidebar so the session list scans like iOS'
status: done
priority: 2
created: "2026-11-02"
updated: "2026-11-27"
depends_on: []
spec_refs:
    - docs/design/web-client.md#Visual language
---

## Description

The desktop sidebar is hard to scan compared with the iOS home list: session rows have no fixed shape (time floats, bordered task chips outshout titles, flag button overlays the title), chrome above and below the list leaves ~40% of the height for sessions, unread/loop activity is repeated in several places, and the loop card is the densest block.

Acceptance:
- Sidebar session rows follow the iOS row grammar: a muted where/when line (project, task ids as plain links, time right-aligned), a two-line title (bold when unread), and a status line only when there is real state; attention markers live in a fixed leading gutter; flag/mark-read never cover the title.
- Section navigation (Backlog … Memory & plans, Settings, shortcuts, sign-out) takes at most two compact rows, keeping labels reachable (tooltips/aria) and the loop/workstream badges visible.
- Unread count appears once in the sidebar chrome; loop status is a single compact line per loop that keeps failures/attention visible.
- Section headings are quiet lowercase captions.
- Light and dark screenshots at laptop width show the list taking clearly more of the sidebar; npm test passes and internal/web/dist is rebuilt.

## Outcome

4b2613b8 and the later sidebar refinements give session rows the inbox layout, compact navigation and one unread count. Light/dark laptop screenshots and the web test suite cover the change. The owner accepted the current layout, including a second line for active loop details.

Commit: Close the web sidebar layout review
