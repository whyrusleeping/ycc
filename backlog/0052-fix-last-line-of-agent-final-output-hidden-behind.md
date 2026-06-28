---
id: "0052"
title: 'Fix: last line of agent final output hidden behind input box'
status: in_progress
priority: 2
created: "2026-06-27"
updated: "2026-06-28"
depends_on: []
spec_refs: []
---

## Description
## Problem

The final line of the agent's "final" output in the session view gets obscured/hidden behind the user input text box. This appears to be a layout/scrolling issue where the chat-log viewport height doesn't fully account for the input box, so the bottom-most line is clipped.

## Acceptance Criteria

- The last line of the agent's final output is fully visible above the input text box.
- No content is clipped behind the input box at the bottom of the session view.
- Verify across cases where output ends exactly at the viewport boundary (single-line and multi-line final messages).
- Layout remains correct on terminal resize.

## Acceptance criteria

## Work log
- 2026-06-28 plan: Fix the clipped final-output line in the session view (internal/tui/tui.go).  Diagnosis: `sessionView`'s line math is correct (header 1 + body vpHeight=H-3 + input 1 + help 1 = H), but the footer/help
…[truncated]
- 2026-06-28 implementer report: Fixed task 0052: the agent's final output line being hidden behind the input box in the TUI session view.  ROOT CAUSE: In `sessionView()` (internal/tui/tui.go) the line math was correct (header 1 + bo
…[truncated]
- 2026-06-28 review tier: single-opus — reviewers: claude
- 2026-06-28 review (claude): accept — The change fixes the clipped final-output line by clamping the session view's footer/help line (the actual culprit — a very long help string that wrapped to a second physical row, overflowing the H-
…[truncated]
