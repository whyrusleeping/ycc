---
id: "0066"
title: Use multiline textarea for all chat inputs + style with rounded expanding frame (per lsp.webp)
status: todo
priority: 3
created: "2026-06-29"
updated: "2026-06-29"
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
