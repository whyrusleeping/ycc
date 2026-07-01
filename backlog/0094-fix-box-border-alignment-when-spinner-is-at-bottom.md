---
id: "0094"
title: Fix box border alignment when spinner is at bottom near text input
status: done
priority: 3
created: "2026-06-30"
updated: "2026-07-01"
depends_on: []
spec_refs: []
---

## Description
Since moving the spinner to the bottom by the text input, the surrounding box border bars are offset/misaligned. This appears to be a spacing/width calculation issue in the TUI rendering (likely in `internal/tui/tui.go`).

## Acceptance criteria
- The box border bars align correctly when the spinner is rendered at the bottom near the text input.
- Alignment holds across spinner animation frames (spinner width changes do not shift the border).
- No regression to box rendering when the spinner is not shown / idle.

## Acceptance criteria

## Work log
