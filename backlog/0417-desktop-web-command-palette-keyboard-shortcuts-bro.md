---
id: "0417"
title: 'Desktop web: command palette, keyboard shortcuts, browser notifications, unread tracking'
status: done
priority: 3
created: "2026-10-02"
updated: "2026-10-06"
depends_on:
    - "0410"
spec_refs:
    - docs/design/web-client.md#Desktop-first layout
    - docs/design/web-client.md#Data access
---

## Description
Desktop affordances from docs/design/web-client.md.

- Command palette (Ctrl/Cmd-K): jump to any session, task, or surface, and run actions (new session, capture, start/stop loop, interrupt). Later phases register their actions in it.
- Keyboard shortcuts for navigation (next/prev session, focus composer, toggle inspector, needs-answer jump) and a `?` help overlay that lists them. Shortcuts don't fire while typing in inputs, except explicit chords.
- Unread tracking: a per-session client-side watermark in localStorage (mirrors iOS SessionReadStore), shown as sidebar badges.
- Browser notifications (permission requested on user gesture) for questions, idle completion, and errors while the tab is hidden or another session is selected. The document title shows the needs-answer count. Clicking a notification focuses that session.

## Acceptance criteria
- [ ] Every primary action is reachable from the palette, and shortcuts are documented in the help overlay.
- [ ] With the tab backgrounded, a question in any session raises one notification that opens that session.

## Work log
