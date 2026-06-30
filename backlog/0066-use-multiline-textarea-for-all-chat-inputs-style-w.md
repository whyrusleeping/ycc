---
id: "0066"
title: Use multiline textarea for all chat inputs + style with rounded expanding frame (per lsp.webp)
status: done
priority: 3
created: "2026-06-29"
updated: "2026-06-30"
depends_on:
    - "0058"
spec_refs: []
---

## Description
## Description
Currently the multiline `textarea` input is only used on the session/chat-log screen (`input textarea.Model` in `internal/tui/tui.go`), while the main menu and the "quick add" panel use the single-line `textinput.Model` (`prompt`). Unify these so every chat-input surface uses the multiline textarea, and restyle the input to match the reference image at `~/lsp.webp`.

Note: the reference image (`~/lsp.webp`) lives outside the workspace and could not be read during capture — confirm the exact framing details against it. The desired look is a rounded-border frame whose framing characters wrap/expand to surround the textbox (rather than the current dark background block).

## Areas
- `internal/tui/tui.go` — `prompt` (textinput) used on the menu; `input` (textarea) on the session screen; the "quick add" panel input.

## Acceptance criteria
- [ ] The multiline textarea input is used everywhere chat input is accepted: session/chat-log screen, main menu, and the "quick add" panel (replacing the single-line `textinput` where applicable).
- [ ] Input is styled per `~/lsp.webp`: rounded framing characters that expand to surround the textbox; remove the current dark-background block styling.
- [ ] Multiline/grow + send behavior (Enter sends, shift+enter/ctrl+j newlines) is consistent across all input surfaces.
- [ ] go build ./... and go test ./internal/tui/... pass.

## Acceptance criteria

## Work log
- 2026-06-30 plan: Unify all chat inputs on the multiline textarea and restyle with a rounded, expanding frame (per lsp.webp), in internal/tui.  1. Shared constructor + styling. Add `newChatInput(placeholder string) tex
…[truncated]
- 2026-06-30 implementer report: Unified all chat-input surfaces onto the multiline `textarea` and restyled them with a rounded, expanding frame (per lsp.webp), in `internal/tui/`.  Changes (internal/tui/tui.go): - Converted the `pro
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — The change unifies all three chat-input surfaces (menu prompt, session input, quick-add capture) onto the multiline `textarea` via a shared `newChatInput` constructor, replacing the single-line `texti
…[truncated]
- 2026-06-30 decision: accept — commit: Unify chat inputs on multiline textarea with rounded expanding frame (task 0066)  Convert the menu prompt and quick-add capture inputs from single-line textinput to the multiline textarea (the session
…[truncated]
- 2026-06-30 usage: 33,859 tok (in 164, out 33,695, cache_r 4,593,308, cache_w 187,869) · cost n/a (unpriced)
