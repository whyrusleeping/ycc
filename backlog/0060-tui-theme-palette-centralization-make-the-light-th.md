---
id: "0060"
title: TUI theme/palette centralization + make the light theme real
status: done
priority: 3
created: "2026-06-29"
updated: "2026-06-29"
depends_on: []
spec_refs:
    - Client UI (TUI)
---

## Description

The TUI's colors are hardcoded ANSI-256 indices scattered as magic numbers across
`internal/tui/tui.go` (e.g. `titleStyle` white-on-`63`, `headerStyle` white-on-`238`,
selection `213`, dim `245`) and `internal/tui/highlight.go` (chroma hardcoded to
`monokai`). They all assume a dark background. The settings overlay exposes a
`dark`/`light` theme toggle that is **persisted but dead**: `makeRenderer()` hardcodes
glamour's `"dark"` style and chroma is fixed to `monokai`, so switching to "light" does
nothing.

This is the foundational UX task: pull every color into one central `theme`/palette and
make the existing theme toggle actually switch the rendering. It unlocks the other
UI-pass tasks (0061/0062/0063) cheaply by giving them named color roles to build on.

### Design
- Introduce a `theme` struct (e.g. `internal/tui/theme.go`) with **named semantic roles**
  rather than raw indices: `accent`, `accentBg`, `selFg`/`selBg`, `dim`, `muted`,
  `success`, `danger`, `warn`, `info`, plus the per-actor colors and diff colors. Provide
  a `darkTheme` and a `lightTheme` value.
- Prefer `lipgloss.AdaptiveColor{Light, Dark}` where a single style should work on both
  backgrounds, so most styles need not be duplicated.
- Replace the package-level `var (...)` style block and `actorStyle()` color literals with
  lookups into the active theme. No magic color numbers should remain inline.
- Wire the `prefs.Theme` value through:
  - `makeRenderer()` selects glamour `"light"`/`"dark"` standard style from the pref
    (keeping the no-`WithAutoStyle` fix — never query the terminal background).
  - `highlight.go` selects a chroma style per theme (e.g. `monokai` for dark, `github`
    for light) instead of a hardcoded package-level `chromaStyle`.
- Changing the theme in the settings overlay must re-render live: rebuild the glamour
  renderer, reset `bodyCache`, and `rebuild()` so the open session/menu repaints in the
  new theme without a restart.

## Acceptance criteria
- [ ] All TUI colors are defined in one place as named roles; no raw `lipgloss.Color("NN")`
      literals remain scattered through `tui.go`/`highlight.go` rendering code.
- [ ] Selecting "light" in the settings overlay visibly switches the palette, glamour
      style, and chroma syntax style; selecting "dark" switches back — both live, without
      restarting the TUI.
- [ ] The theme choice is still persisted via `clientconfig` and restored on next launch.
- [ ] Existing TUI behavior/tests are unaffected (no functional regressions in the event
      stream, overlays, or pickers).

## Notes
- Keep the safety property from `makeRenderer()`: never use `WithAutoStyle` (it reads the
  terminal background off stdin, which Bubble Tea owns, and freezes the UI).
- Foundation for 0061 (app frame), 0062 (status bar), 0063 (event-stream polish) — they
  should consume these named roles rather than reintroduce literals.

## Work log
- 2026-06-29 plan: Centralize TUI colors into a theme palette and make the dark/light pref toggle live.  1. New file `internal/tui/theme.go`:    - `type theme struct{...}` with NAMED semantic role colors (lipgloss.Termi
…[truncated]
- 2026-06-29 implementer report: Centralized all TUI colors into a theme palette and made the dark/light settings toggle switch the rendering live.  Changes: - New `internal/tui/theme.go`: `theme` struct with named semantic role colo
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): revise — The color centralization is solid: every raw lipgloss.Color literal is removed from tui.go/highlight.go and replaced by named roles in a new theme.go, actorStyle/chroma/glamour are all theme-driven, a
…[truncated]
- 2026-06-29 revision: Addressed the reviewer's issue: a persisted "light" theme wasn't fully restored at launch (only init() ran, always dark, so the lipgloss palette/chroma stayed dark while glamour was light).  Fix: in `
…[truncated]
- 2026-06-29 review (Claude): accept — The revision fully addresses the prior major finding: initialModel() now calls applyTheme(themeByName(prefs.Theme)) at launch, so the lipgloss palette and chroma syntax style are restored from the per
…[truncated]
- 2026-06-29 decision: accept — commit: tui: centralize colors into a theme palette + make the light theme real [0060]  Introduce internal/tui/theme.go with named semantic color roles and dark/light palettes. Package-level styles, actorStyl
…[truncated]
