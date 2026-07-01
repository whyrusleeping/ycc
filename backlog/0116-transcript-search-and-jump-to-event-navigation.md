---
id: "0116"
title: Transcript search (/) and jump-to-event navigation
status: todo
priority: 4
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 18.6 Session history browser & reopen
    - 18. Client UI (TUI)
---

## Description
## Description
Long work sessions produce thousands of events; the only navigation is ↑/↓ selection-walking and pgup/pgdn. Add transcript navigation for the live session view and the read-only transcript browser: `/` incremental search (highlight + n/N next/prev match), and jump keys for semantically interesting events (previous/next question, review verdict, commit, error). Search should match the rendered text (headlines + expanded bodies).

## Acceptance criteria
- [ ] `/` searches the transcript; n/N cycle matches; esc cancels back to normal keys
- [ ] Jump keys for question/review/commit/error events (documented in help/footer)
- [ ] Works in both the live session view and the history transcript view
- [ ] Search state doesn't interfere with the input textarea (only when input empty or via an explicit mode)

## Acceptance criteria

## Work log
