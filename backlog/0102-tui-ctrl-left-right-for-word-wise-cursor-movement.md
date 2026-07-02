---
id: "0102"
title: 'TUI: Ctrl+Left/Right for word-wise cursor movement in prompt input'
status: done
priority: 3
created: "2026-07-01"
updated: "2026-07-02"
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

## Plan

1. In `newChatInput` (internal/tui/tui.go, ~line 1358), extend the textarea key map so word motions also fire on Ctrl+arrows:
   - `input.KeyMap.WordBackward` → keys "alt+left", "alt+b", "ctrl+left"
   - `input.KeyMap.WordForward` → keys "alt+right", "alt+f", "ctrl+right"
   (bubbles v2 textarea defaults only bind alt+left/alt+right/alt+b/alt+f; textinput already binds ctrl+left/ctrl+right by default, so the single-line mbInputs need no change.)
2. Verify no TUI update-loop switch intercepts "ctrl+left"/"ctrl+right" before the textarea's Update sees the key (existing switches only match plain "left"/"right", so they fall through).
3. Add a small unit test that constructs the chat input via newChatInput, sets a multi-word value, moves the cursor to end, sends a ctrl+left key press through Update and asserts the cursor lands at the previous word start; same for ctrl+right moving forward. Also exercise start/end-of-buffer no-crash cases.
4. `go build ./... && go test ./internal/tui/...` to confirm.

### Starting points
- internal/tui/tui.go: newChatInput() ~line 1358 — where InsertNewline is already rebound; add WordBackward/WordForward bindings there
- bubbles v2 textarea defaults: WordForward = alt+right/alt+f, WordBackward = alt+left/alt+b (no ctrl variants)
- textarea exposes KeyMap field and key.NewBinding(key.WithKeys(...)); see existing input.KeyMap.InsertNewline line for the pattern
- tests: internal/tui has existing *_test.go files; keys can be sent via tea.KeyPressMsg

## Work log
- 2026-07-02 plan: 1. In `newChatInput` (internal/tui/tui.go, ~line 1358), extend the textarea key map so word motions also fire on Ctrl+arrows:    - `input.KeyMap.WordBackward` → keys "alt+left", "alt+b", "ctrl+left"
…[truncated]
- 2026-07-02 context hints: 4 recorded with plan
- 2026-07-02 context hints: internal/tui/tui.go: newChatInput() ~line 1358 — pattern: input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "ctrl+j")); bubbles v2 textarea default keymap: WordForward = alt+ri
…[truncated]
- 2026-07-02 implementer report: Implemented Task 0102: Ctrl+Left/Right word-wise cursor movement in the TUI prompt input.  Change (internal/tui/tui.go, newChatInput ~line 1371): - Extended the textarea key map so word motions also f
…[truncated]
- 2026-07-02 review tier: simple (coordinator self-review)
- 2026-07-02 decision: accept — commit: tui: bind ctrl+left/ctrl+right to word-wise cursor motion in chat inputs (0102)
- 2026-07-02 usage: 9,695 tok (in 52, out 9,643, cache_r 671,128, cache_w 55,746) · cost n/a (unpriced)
  implementer: 5,286 tok (in 30, out 5,256, cache_r 488,988, cache_w 41,503) · cost n/a (unpriced)
  coordinator: 4,409 tok (in 22, out 4,387, cache_r 182,140, cache_w 14,243) · cost n/a (unpriced)
