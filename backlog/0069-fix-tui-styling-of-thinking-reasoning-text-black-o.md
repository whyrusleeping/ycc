---
id: "0069"
title: Fix TUI styling of thinking/reasoning text (black-on-white background and extra line spacing)
status: todo
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
