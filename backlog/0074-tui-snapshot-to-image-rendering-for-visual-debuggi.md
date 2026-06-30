---
id: "0074"
title: TUI snapshot-to-image rendering for visual debugging
status: done
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs:
    - Client UI (TUI)
    - Package layout
---

## Description
## Why this exists

Debugging the TUI today is hard: tests can call `model.View()` / `model.render()` and
assert on `stripANSI(out)` substrings (see `internal/tui/tui_test.go`), but neither a
human nor the agent can *see* what the screen actually looks like — colors, layout,
alignment, borders, wrapping. The ask: be able to render the TUI to an **image (PNG)**
of "what the screen looks like" so a maintainer (and the agent, via the multimodal
`Read` tool, spec §8) can look at it when diagnosing a layout/styling bug.

This is a **test/dev tooling** feature. It does not change runtime TUI behaviour.

## Approach (grounded in the codebase)

The model already exposes its full frame as an ANSI string: `model.render()` returns the
screen body and `model.View()` wraps it (`tui.go:3503`). The plan is a self-contained
ANSI-string → PNG rasterizer, no external processes (no vhs/ttyd/ffmpeg):

1. **Parse ANSI into a cell grid.** Use `github.com/charmbracelet/ultraviolet` (already an
   indirect dep): `ultraviolet.NewStyledString(ansiStr).Draw(buf, area)` fills a
   `*ultraviolet.Buffer`; iterate `buf.CellAt(x,y)` to read each `Cell` (`Content`,
   `Width`, and `Style{Fg, Bg, Attrs (bold/faint/italic/reverse/...)}`, all already
   resolved to `color.Color`).
2. **Rasterize the grid to an image.** Draw a fixed-size monospace cell grid into an
   `image.RGBA`: fill each cell's background rect, then draw its glyph in the foreground
   color with a monospace font. Use the embedded Go Mono face from
   `golang.org/x/image/font/gofont/gomono` + `golang.org/x/image/font/opentype` +
   `golang.org/x/image/font.Drawer` (add `golang.org/x/image` as a direct dep via
   `go get`). Honor at least bold (use `gomonobold`), reverse (swap fg/bg), and faint
   (blend toward bg); a sensible default fg/bg when a cell leaves them unset.
3. **Public helper.** Add a small package (suggested `internal/tui/snapshot`, or a
   `snapshot.go` in `internal/tui`) with e.g.
   `RenderANSI(ansi string, cols, rows int) (image.Image, error)` and
   `WritePNG(path, ansi string, cols, rows int) error`, plus a convenience that takes a
   `model` (or its `render()` output) at a given width/height.

## How it gets used

- **Test helper.** A helper usable from `internal/tui` tests that, given a constructed
  `model` sized via `tea.WindowSizeMsg{Width,Height}` (the existing tests already do
  this), writes a PNG to disk — gated so normal `go test` doesn't spew files (e.g. only
  when `YCC_TUI_SNAPSHOT_DIR` is set, or via an explicit opt-in test). The agent can then
  `Read` the PNG to *see* the rendered screen.
- At least one example test that drives a representative screen (e.g. a session view with
  a few events, or the settings overlay) to a PNG and asserts it is a valid, non-empty
  image of the expected pixel dimensions (cols*cellW × rows*cellH).

## Notes / pitfalls

- **Color profile.** `.View()` must actually emit color SGR for the rasterizer to show
  color. Tests construct `model{}` directly and lipgloss/theme styles drive color; the
  helper may need to force a truecolor profile (lipgloss/v2) so colors are present rather
  than stripped. Verify the produced PNG is colored, not monochrome.
- Keep it dependency-light and offline: only `ultraviolet` (already vendored indirectly)
  and `golang.org/x/image` (new direct dep). No network, no headless terminal emulator.
- Wide runes / grapheme clusters: rely on `Cell.Width`; advance the x cursor by the cell
  width so alignment matches the terminal.
- This is purely additive tooling under `internal/tui` (spec §15, §18). Update spec §18
  with a short "Snapshot rendering for debugging" note once it lands.

## Acceptance criteria
- [ ] A reusable helper (new pkg or file under `internal/tui`) converts a TUI ANSI frame
      (from `model.render()` / `View()`) + (cols, rows) into an `image.Image` and can
      write it as a PNG.
- [ ] ANSI styling is honored: per-cell foreground/background colors, plus bold and
      reverse at minimum; cell grid alignment matches the source (uses `Cell.Width`).
- [ ] The produced PNG is colored (not stripped to monochrome) — color profile handled so
      `.View()` emits SGR.
- [ ] A test drives at least one representative screen to a PNG; output gated so ordinary
      `go test ./...` does not litter the tree (env-var or explicit opt-in).
- [ ] `go build ./...` and `go test ./...` pass; `golang.org/x/image` added as a direct
      dependency.
- [ ] spec §18 gets a brief note documenting the snapshot-rendering debug aid.

## Acceptance criteria

## Work log
- 2026-06-30 plan: Add a self-contained ANSI-frame → PNG rasterizer for the TUI so a maintainer/agent can see the rendered screen.  1. Add `golang.org/x/image` as a direct dependency (`go get golang.org/x/image`) for 
…[truncated]
- 2026-06-30 plan: Add a self-contained ANSI-frame → PNG rasterizer for the TUI under internal/tui/snapshot.  1. Dependency: `go get golang.org/x/image` to make it a direct dep (provides font, opentype, gofont/gomono 
…[truncated]
- 2026-06-30 implementer report: Implemented TUI snapshot-to-image (PNG) rendering for visual debugging — purely additive dev/test tooling under internal/tui; no runtime behaviour changed.  What I added: - `internal/tui/snapshot/sn
…[truncated]
- 2026-06-30 revision: Addressed the reviewer's robustness fix: the snapshot tests now create the output directory before writing, so the documented debug workflow (set YCC_TUI_SNAPSHOT_DIR to any path and run the test) wor
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): revise — The task-0074 snapshot tooling is implemented correctly and fully meets every acceptance criterion: a reusable internal/tui/snapshot package (RenderANSI/WritePNG/CellSize) parses an ANSI frame via ult
…[truncated]
- 2026-06-30 review (Claude): accept — The revision resolves both previous findings: the stray 144 KB debug screenshot at the repo root is gone, and the unrelated runtime changes (autonomous loop sessions / event-channel reuse fix) were mo
…[truncated]
- 2026-06-30 decision: accept — commit: TUI snapshot-to-image (PNG) rendering for visual debugging (task 0074)  Add internal/tui/snapshot: a self-contained ANSI-frame -> PNG rasterizer (RenderANSI/WritePNG/CellSize) that parses a TUI frame 
…[truncated]
- 2026-06-30 usage: 47,673 tok (in 198, out 47,475, cache_r 3,197,830, cache_w 131,723) · cost n/a (unpriced)
