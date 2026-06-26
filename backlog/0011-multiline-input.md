---
id: "0011"
title: Multiline session input (textarea)
status: todo
priority: 3
created: 2026-06-26
updated: 2026-06-26
depends_on: ["0006"]
spec_refs: ["Client UI (TUI)"]
---

## Description
The session input is a single-line `textinput`; long prompts and multi-paragraph
answers are awkward. Switch to a Bubble Tea `textarea` that wraps and grows. See
spec §18.1.

## Acceptance criteria
- [ ] session input uses `textarea` and wraps long lines
- [ ] Enter sends the buffer and clears it; Shift+Enter inserts a newline
- [ ] textarea height is bounded (a few rows) and scrolls internally beyond that
- [ ] does not crowd out the event stream / status bar at any terminal size

## Work log
