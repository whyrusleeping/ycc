---
id: "0075"
title: Scroll TUI backlog view when items exceed terminal height
status: done
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
- 2026-06-30 plan: Fix the backlog browser overflow by adding vertical windowing to the shared `browserCard` component in internal/tui/tui.go (this also benefits the sessions/browse browsers that use it).  1. Add a smal
…[truncated]
- 2026-06-30 implementer report: Implemented vertical windowing for the shared `browserCard` component so the backlog (and sessions/browse) browsers clip and scroll within the terminal instead of overrunning the bottom.  Changes in i
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — The change adds a well-documented `listWindow` helper and integrates vertical windowing into the shared `browserCard`, which the backlog browser uses. The chrome budget (`m.h - 6`) correctly accounts 
…[truncated]
- 2026-06-30 decision: accept — commit: tui: window backlog browser to viewport height so long lists scroll instead of overrunning  Add listWindow helper and apply vertical windowing in the shared browserCard component, keeping the cursor v
…[truncated]
- 2026-06-30 usage: 11,402 tok (in 66, out 11,336, cache_r 411,661, cache_w 34,505) · cost n/a (unpriced)
