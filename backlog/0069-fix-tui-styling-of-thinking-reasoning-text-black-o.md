---
id: "0069"
title: Fix TUI styling of thinking/reasoning text (black-on-white background and extra line spacing)
status: done
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
## Problem

In the TUI, thinking/reasoning text is rendered with poor styling: it appears as black text on a white background, and there is extra spacing between each line. This looks visually broken and inconsistent with the rest of the output.

## Scope

Pure visual/styling fix for the TUI rendering of thinking/reasoning text only. No behavioral changes to how thinking content is captured or streamed.

## Acceptance Criteria

- [ ] Thinking/reasoning text no longer renders as black-on-white; it uses styling consistent with the TUI theme (e.g. dimmed/muted foreground without a jarring background).
- [ ] Extra line spacing between thinking text lines is removed so the block renders with normal line spacing.
- [ ] Change is limited to TUI rendering/styling; no changes to thinking content capture or streaming logic.

## Acceptance criteria

## Work log
- 2026-06-30 plan: Fix TUI thinking/reasoning rendering (internal/tui).  Two root causes confirmed: 1. Black-on-white "background": thinkStyle uses Italic(true). Many terminals render the italic SGR (3) as reverse video
…[truncated]
- 2026-06-30 implementer report: Fixed TUI thinking/reasoning text styling in internal/tui only.  Changes: 1. theme.go: Removed `Italic(true)` from `thinkStyle` (was `lipgloss.NewStyle().Italic(true).Foreground(c(t.think))` → `lipg
…[truncated]
- 2026-06-30 review tier: simple (coordinator self-review)
- 2026-06-30 decision: accept — commit: Fix TUI thinking text styling: drop italic (reverse-video bg) and per-line wrap to remove spurious line spacing
- 2026-06-30 usage: 5,095 tok (in 24, out 5,071, cache_r 248,027, cache_w 12,056) · cost n/a (unpriced)
