---
id: "0061"
title: Unified TUI app frame + bordered modal cards
status: done
priority: 3
created: "2026-06-29"
updated: "2026-06-30"
depends_on:
    - "0060"
spec_refs:
    - Client UI (TUI)
---

## Description

The TUI screens have inconsistent chrome. The session view has a real status bar; the
menu, project picker, history, and backlog browser just render a `titleStyle` pill at the
top and a long, single-line, truncated dim help string at the bottom. The modal overlays
(settings, backlog browser, quick-capture, model-backends) are drawn as plain full-screen
text on the alt-screen with no border or framing — so a "modal over the session" doesn't
actually read as an overlay; it just replaces the screen as raw text.

This task gives every screen the same shape and makes modals look like modals.

### Design
- Define a small layout helper set (consuming the 0060 palette): a top **title/breadcrumb
  bar**, a content region, and a consistent **key-hint footer bar** rendered the same way
  across menu / picker / history / backlog / session.
- Render the modal overlays (settings, backlog browser/detail, quick-capture, model
  backends list/form/confirm) as **bordered cards**: `lipgloss.RoundedBorder` with padding,
  centered over the screen with `lipgloss.Place`. The card floats over a dimmed/!cleared
  backdrop so it visibly reads as an overlay rather than a full replacement.
- Normalize the footer: a compact, consistent key-hint line per screen (reuse the existing
  width-clamp so it can never wrap to a second physical row and corrupt line accounting).
- Keep all existing keybindings and behaviors identical — this is presentation only.

## Acceptance criteria
- [ ] Menu, project picker, history, and backlog screens share a consistent title bar and
      key-hint footer styling.
- [ ] Settings, backlog browser, quick-capture, and model-backends overlays render as
      bordered, centered cards that read as modals over the underlying screen.
- [ ] No regressions: every existing keybinding, navigation path, and the single-row
      header/footer width-clamp invariant still hold (no frame overflow / input overlap).
- [ ] TUI unit tests still pass; view snapshots updated where layout strings changed.

## Notes
- Depends on 0060 for the shared palette/roles (borders, backdrops, title bars).
- Be careful with `lipgloss.Place`/borders interacting with the viewport height math in
  `relayout()` — the session's scrollable region must keep its correct height.

## Work log
- 2026-06-30 plan: Presentation-only refactor of TUI chrome in internal/tui/tui.go, consuming the 0060 theme palette. No keybinding/behavior changes.  1. Add small layout helpers (new section in tui.go, colors from them
…[truncated]
- 2026-06-30 implementer report: Implemented Task 0061: unified TUI app frame + bordered modal cards (presentation-only) in `internal/tui/tui.go`, consuming the 0060 theme palette.  ## What changed  Added a small shared-chrome helper
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — Presentation-only refactor that adds unified TUI chrome helpers (titleBar/footerBar/modalCard) and renders the settings, backlog, quick-capture, and model-backends overlays as bordered, centered cards
…[truncated]
- 2026-06-30 decision: accept — commit: tui: unified app frame + bordered modal cards [0061]  Add shared chrome helpers (titleBar/footerBar/modalCard) consuming the 0060 theme palette. Menu, picker, history, and backlog share a consistent t
…[truncated]
- 2026-06-30 usage: 34,093 tok (in 114, out 33,979, cache_r 3,310,192, cache_w 177,095) · cost n/a (unpriced)
