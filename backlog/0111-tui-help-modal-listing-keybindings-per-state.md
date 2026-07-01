---
id: "0111"
title: TUI help modal (?) listing keybindings per state
status: todo
priority: 4
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 18. Client UI (TUI)
---

## Description
## Description
The footer is the only key documentation and it's width-clamped — hints drop exactly when the terminal is narrow. The binding count (per-state: menu, session, browsers, pickers, overlays) has outgrown a one-line footer. Add a help modal on `?` (when the focused input is empty; always on ctrl+h/ctrl+_) listing bindings grouped by context, using the shared modal-card surface.

## Acceptance criteria
- [ ] `?`/ctrl+h opens a help modal over menu and session; esc closes
- [ ] Bindings grouped by state (home, session, question picker, browsers, overlays) and stay in sync with reality (single source of truth preferred over a hand-copied list)
- [ ] `?` still types into a non-empty input
- [ ] Footer mentions the help key

## Acceptance criteria

## Work log
