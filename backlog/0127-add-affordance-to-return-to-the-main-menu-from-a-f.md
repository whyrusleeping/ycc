---
id: "0127"
title: Add affordance to return to the main menu from a finished work session
status: todo
priority: 3
created: "2026-07-03"
updated: "2026-07-03"
depends_on: []
spec_refs: []
---

## Description
## Description

When a work session finishes (the agent goes idle / the session completes), there is no easy or obvious way to get back to the TUI main menu cleanly. Today the user is left in the session view with no clear "you're done — go back" affordance, and the paths out are unclear (quit entirely, or opaque key presses).

Add explicit affordances in the session view once a session is finished/idle, e.g.:

- A visible hint in the status bar / footer (e.g. "session finished — press esc/q to return to menu").
- A key binding that stops/detaches the finished session and returns to `stateMenu`, ensuring the session is stopped or persisted cleanly (stream closed, no orphaned daemon session).
- Make sure this interacts sensibly with the "work (loop)" driver (which already stops idle sessions itself) and with the ctrl+c quit guard.

## Acceptance Criteria

- [ ] When a session reaches a finished/idle state, the UI shows a clear hint for returning to the main menu.
- [ ] A single key press returns the user to the main menu from a finished session, cleanly closing/stopping the session (no dangling stream or orphaned session).
- [ ] Behavior does not break the work-loop driver's own idle→stop handling.
- [ ] Help overlay (help.go) documents the new binding.

## Acceptance criteria

## Work log
