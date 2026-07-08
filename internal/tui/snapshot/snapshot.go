// Package snapshot renders a TUI ANSI frame into a PNG image so a maintainer
// (or the agent, via the multimodal Read tool) can visually inspect what the
// screen looks like — colors, layout, alignment, borders, wrapping — when
// diagnosing a styling or layout bug.
//
// This is purely additive dev/test tooling: it does not change runtime TUI
// behaviour. The rasterizer is self-contained (no external processes such as
// vhs/ttyd/ffmpeg) — it parses ANSI into a cell grid with
// github.com/charmbracelet/ultraviolet and draws a fixed monospace grid with
// the embedded Go Mono font.
package snapshot

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Default foreground/background used when a cell leaves them unset.
var (
	defaultBg = color.RGBA{0x12, 0x12, 0x12, 0xff}
	defaultFg = color.RGBA{0xe0, 0xe0, 0xe0, 0xff}
)

// Font rendering parameters. 14pt at 72 DPI yields a comfortably readable cell.
const (
	fontPoints = 14.0
	fontDPI    = 72.0
)

var (
	fontsOnce    sync.Once
	regularFace  font.Face
	boldFace     font.Face
	cellW, cellH int
	baseline     int
	fontInitErr  error
)

func initFonts() {
	fontsOnce.Do(func() {
		reg, err := opentype.Parse(gomono.TTF)
		if err != nil {
			fontInitErr = fmt.Errorf("parse gomono: %w", err)
			return
		}
		bold, err := opentype.Parse(gomonobold.TTF)
		if err != nil {
			fontInitErr = fmt.Errorf("parse gomonobold: %w", err)
			return
		}
		opts := &opentype.FaceOptions{Size: fontPoints, DPI: fontDPI, Hinting: font.HintingFull}
		regularFace, err = opentype.NewFace(reg, opts)
		if err != nil {
			fontInitErr = fmt.Errorf("new regular face: %w", err)
			return
		}
		boldFace, err = opentype.NewFace(bold, opts)
		if err != nil {
			fontInitErr = fmt.Errorf("new bold face: %w", err)
			return
		}

		// Cell width from a representative monospace glyph advance.
		adv, ok := regularFace.GlyphAdvance('M')
		if !ok {
			adv = fixed.I(8)
		}
		cellW = adv.Ceil()
		if cellW < 1 {
			cellW = 1
		}

		m := regularFace.Metrics()
		ascent := m.Ascent.Ceil()
		descent := m.Descent.Ceil()
		cellH = ascent + descent
		if cellH < 1 {
			cellH = 1
		}
		baseline = ascent
	})
}

// CellSize returns the pixel width and height of a single character cell.
// A rendered image is exactly cols*w by rows*h pixels.
func CellSize() (w, h int, err error) {
	initFonts()
	if fontInitErr != nil {
		return 0, 0, fontInitErr
	}
	return cellW, cellH, nil
}

// Grid is the minimal read-only view of a cols×rows cell grid the rasterizer
// needs: a per-coordinate cell lookup that returns nil for an unset/out-of-range
// cell. Both ultraviolet's *ScreenBuffer and the vt package's *Emulator (and its
// concurrency-safe *SafeEmulator) satisfy this, so RenderScreen can rasterize a
// live terminal-emulator screen with the exact same drawing path as RenderANSI.
type Grid interface {
	CellAt(x, y int) *uv.Cell
}

// RenderANSI parses an ANSI terminal frame (e.g. the output of a TUI model's
// render()/View()) into a cols×rows cell grid and rasterizes it into an
// image. Per-cell foreground/background colors are honored, along with the
// bold, faint and reverse SGR attributes. Cell alignment follows each cell's
// terminal Width (so wide runes line up).
func RenderANSI(ansiStr string, cols, rows int) (image.Image, error) {
	if cols < 1 || rows < 1 {
		return nil, fmt.Errorf("invalid grid size %dx%d", cols, rows)
	}
	buf := uv.NewScreenBuffer(cols, rows)
	buf.Method = ansi.GraphemeWidth
	uv.NewStyledString(ansiStr).Draw(buf, uv.Rect(0, 0, cols, rows))
	return RenderScreen(buf, cols, rows)
}

// RenderScreen rasterizes a live cell grid (e.g. a terminal emulator's screen)
// into a cols×rows image, using the same per-cell drawing path as RenderANSI.
// This is the entry point the e2e TUI harness uses to screenshot the real
// rendered screen of the ycc binary running under a PTY (see internal/e2e):
// pipe the PTY output into a vt.Emulator and hand it straight to RenderScreen.
func RenderScreen(grid Grid, cols, rows int) (image.Image, error) {
	initFonts()
	if fontInitErr != nil {
		return nil, fontInitErr
	}
	if cols < 1 || rows < 1 {
		return nil, fmt.Errorf("invalid grid size %dx%d", cols, rows)
	}

	img := image.NewRGBA(image.Rect(0, 0, cols*cellW, rows*cellH))

	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			cell := grid.CellAt(x, y)
			if cell == nil {
				fillRect(img, x*cellW, y*cellH, cellW, cellH, defaultBg)
				continue
			}

			fg := toRGBA(cell.Style.Fg, defaultFg)
			bg := toRGBA(cell.Style.Bg, defaultBg)
			attrs := cell.Style.Attrs

			if attrs&uv.AttrReverse != 0 {
				fg, bg = bg, fg
			}
			if attrs&uv.AttrFaint != 0 {
				fg = blend(fg, bg, 0.5)
			}

			width := cell.Width
			if width < 1 {
				width = 1
			}

			// Background spans all columns occupied by a (possibly wide) cell.
			fillRect(img, x*cellW, y*cellH, cellW*width, cellH, bg)

			content := cell.Content
			if content != "" && content != " " {
				face := regularFace
				if attrs&uv.AttrBold != 0 {
					face = boldFace
				}
				drawer := &font.Drawer{
					Dst:  img,
					Src:  image.NewUniform(fg),
					Face: face,
					Dot:  fixed.P(x*cellW, y*cellH+baseline),
				}
				drawer.DrawString(substituteMissing(content, face))
			}

			// Skip the columns spanned by a wide cell; ultraviolet leaves the
			// trailing cells empty.
			if width > 1 {
				x += width - 1
			}
		}
	}

	return img, nil
}

// fallbackRune is drawn in place of any rune the active face has no glyph for,
// so missing TUI icon runes (e.g. the box-drawing / status glyphs Go Mono
// lacks) render as a neutral mark instead of the notdef "tofu" box.
const fallbackRune = '•'

// substituteMissing replaces every rune the face has no glyph for with a
// fallback the face does have, so cell content never rasterizes as the notdef
// box. Spaces and runes already present in the face pass through unchanged.
// GlyphAdvance reports ok=false for a rune that maps to glyph index 0 (notdef),
// which is exactly the missing-glyph case.
func substituteMissing(s string, face font.Face) string {
	missing := false
	for _, r := range s {
		if r == ' ' {
			continue
		}
		if _, ok := face.GlyphAdvance(r); !ok {
			missing = true
			break
		}
	}
	if !missing {
		return s // fast path: every rune is covered
	}

	fb := fallbackRune
	if _, ok := face.GlyphAdvance(fb); !ok {
		fb = '*' // last resort; ASCII is always present
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == ' ' {
			b.WriteRune(r)
			continue
		}
		if _, ok := face.GlyphAdvance(r); ok {
			b.WriteRune(r)
		} else {
			b.WriteRune(fb)
		}
	}
	return b.String()
}

// WritePNG renders the ANSI frame and writes it to path as a PNG file.
func WritePNG(path, ansiStr string, cols, rows int) error {
	img, err := RenderANSI(ansiStr, cols, rows)
	if err != nil {
		return err
	}
	return encodePNG(path, img)
}

// WriteScreenPNG rasterizes a live cell grid (a terminal emulator's screen) and
// writes it to path as a PNG file. It is the screenshot entry point for the e2e
// TUI harness (internal/e2e), which captures the real rendered screen of the
// ycc binary running under a PTY.
func WriteScreenPNG(path string, grid Grid, cols, rows int) error {
	img, err := RenderScreen(grid, cols, rows)
	if err != nil {
		return err
	}
	return encodePNG(path, img)
}

// encodePNG writes img to path as a PNG file.
func encodePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return fmt.Errorf("encode png: %w", err)
	}
	return nil
}

// fillRect paints a solid rectangle of color c into img.
func fillRect(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	b := img.Bounds()
	for dy := 0; dy < h; dy++ {
		py := y + dy
		if py < b.Min.Y || py >= b.Max.Y {
			continue
		}
		for dx := 0; dx < w; dx++ {
			px := x + dx
			if px < b.Min.X || px >= b.Max.X {
				continue
			}
			img.SetRGBA(px, py, c)
		}
	}
}

// toRGBA converts a possibly-nil color.Color into a concrete opaque RGBA,
// falling back to def when c is nil.
func toRGBA(c color.Color, def color.RGBA) color.RGBA {
	if c == nil {
		return def
	}
	r, g, b, a := c.RGBA()
	if a == 0 {
		return def
	}
	return color.RGBA{
		R: uint8(r >> 8),
		G: uint8(g >> 8),
		B: uint8(b >> 8),
		A: 0xff,
	}
}

// blend mixes a toward b by t (0 returns a, 1 returns b).
func blend(a, b color.RGBA, t float64) color.RGBA {
	mix := func(x, y uint8) uint8 {
		return uint8(float64(x)*(1-t) + float64(y)*t)
	}
	return color.RGBA{
		R: mix(a.R, b.R),
		G: mix(a.G, b.G),
		B: mix(a.B, b.B),
		A: 0xff,
	}
}
