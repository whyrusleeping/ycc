---
id: "0102"
title: 'TUI: Ctrl+Left/Right for word-wise cursor movement in prompt input'
status: todo
priority: 3
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs: []
---

## Description
In the TUI prompt/text input (the `textarea.Model` used for session/menu input in `internal/tui/tui.go`), pressing **Ctrl+Left** should move the cursor backward one whole word and **Ctrl+Right** should move it forward one whole word, matching common terminal/editor behavior.

The prompt is built on `charm.land/bubbles/v2/textarea` (and there is also a `textinput.Model`). The textarea component supports word-motion actions (e.g. `WordBackward`/`WordForward` in its key map) — verify whether the default key bindings are wired to Ctrl+Left/Right, and if not, bind them. Ensure the emitted key sequences for Ctrl+Left/Right are recognized by Bubble Tea's key handling and not swallowed by other TUI key handling.

## Acceptance criteria
- In the multi-line prompt input, Ctrl+Left moves the cursor back to the start of the previous word.
- Ctrl+Right moves the cursor forward to the end/next word boundary.
- Behavior is consistent within a line and across word boundaries; no crash at start/end of buffer.
- Existing prompt editing/submission behavior is unaffected.
- If the single-line `textinput` prompt shares this input path, the same bindings apply there too.

## Notes
- Likely a small change: configure the textarea/textinput key map for word motions, or add key handling in the TUI update loop.

## Acceptance criteria

## Work log
