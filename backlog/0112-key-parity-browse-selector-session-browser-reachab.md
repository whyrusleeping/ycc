---
id: "0112"
title: 'Key parity: browse selector + session browser reachable from within a session'
status: todo
priority: 4
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 18.6 Session history browser & reopen
---

## Description
## Description
Modal/browse chords are inconsistent between states: `ctrl+b` (backlog) and `ctrl+n` (capture) work in both menu and session, but `ctrl+o` (browse selector) and `ctrl+r` (session browser) work only on the home menu. From inside a session there is no route to the session browser, plans, or cost view except leaving via the overlay. Make the browse selector (and its targets) available globally.

## Acceptance criteria
- [ ] ctrl+o opens the browse selector from a session; targets (backlog/plans/sessions/cost) work and return to the session on esc
- [ ] Session-history browsing from within a session is read-only (no accidental reopen-over-live-session footguns; reopen may be disabled there)
- [ ] Session footer/help updated

## Acceptance criteria

## Work log
