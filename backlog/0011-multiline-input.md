---
id: "0011"
title: Multiline session input (textarea)
status: done
priority: 3
created: "2026-06-26"
updated: "2026-06-29"
depends_on:
    - "0006"
spec_refs:
    - Client UI (TUI)
---

## Description
The session input is a single-line `textinput`; long prompts and multi-paragraph
answers are awkward. Switch to a Bubble Tea `textarea` that wraps and grows. See
spec §18.1.

## Acceptance criteria
- [ ] session input uses `textarea` and wraps long lines
- [ ] Enter sends the buffer and clears it; Shift+Enter inserts a newline
- [ ] textarea height is bounded (a few rows) and scrolls internally beyond that
- [ ] does not crowd out the event stream / status bar at any terminal size

## Work log
- 2026-06-29 plan: Convert the session input (`model.input`) from `textinput.Model` to a bounded, growing `textarea.Model` in internal/tui/tui.go. Leave the menu `prompt`, `captureInput`, and model-backend `mbInputs` as
…[truncated]
- 2026-06-29 implementer report: Converted the session input (`model.input`) in internal/tui/tui.go from `textinput.Model` to a bounded, growing `textarea.Model`.  Changes: - Imports: added `github.com/charmbracelet/bubbles/key` and 
…[truncated]
- 2026-06-29 revision: Fixed the test-breaking zero-value textarea panic without weakening the feature.  Changes in internal/tui/tui.go: 1. Extracted the session-input construction into `newSessionInput() textarea.Model` (p
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The session input is correctly converted from textinput to a bounded, growing textarea. All four acceptance criteria are met: it uses textarea.New() with wrapping (SetWidth), Enter is intercepted to s
…[truncated]
- 2026-06-29 decision: accept — commit 14f6b76: TUI: multiline session input (textarea) [0011]  Convert the session input from a single-line textinput to a bounded, growing textarea: Enter sends+clears, shift+enter/ctrl+j insert a newline, height g
…[truncated]
- 2026-06-29 usage: 47,082 tok (in 130, out 46,952, cache_r 2,294,321, cache_w 122,488) · cost n/a (unpriced)
