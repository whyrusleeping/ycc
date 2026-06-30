---
id: "0075"
title: Scroll TUI backlog view when items exceed terminal height
status: todo
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
## Problem

In the TUI, pressing Ctrl+B opens the backlog view screen. When the terminal height is less than the number of backlog items, the list overruns the bottom of the screen instead of being clipped/scrolled within the viewport.

## Expected behavior

The backlog view should render within the terminal bounds and scroll smoothly (e.g. via keyboard navigation / cursor movement) when there are more items than fit on screen, keeping the selected/active item visible.

## Acceptance criteria

- [ ] When backlog items exceed the terminal height, the list is clipped to the viewport and does not overrun the bottom of the screen.
- [ ] The view scrolls as the user navigates so the active item stays visible.
- [ ] Behavior is verified at small terminal heights.
- [ ] Resizing the terminal recomputes the visible window correctly.

## Acceptance criteria

## Work log
