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

// RenderANSI parses an ANSI terminal frame (e.g. the output of a TUI model's
// render()/View()) into a cols×rows cell grid and rasterizes it into an
// image. Per-cell foreground/background colors are honored, along with the
// bold, faint and reverse SGR attributes. Cell alignment follows each cell's
// terminal Width (so wide runes line up).
func RenderANSI(ansiStr string, cols, rows int) (image.Image, error) {
	initFonts()
	if fontInitErr != nil {
		return nil, fontInitErr
	}
	if cols < 1 || rows < 1 {
		return nil, fmt.Errorf("invalid grid size %dx%d", cols, rows)
	}

	buf := uv.NewScreenBuffer(cols, rows)
	buf.Method = ansi.GraphemeWidth
	uv.NewStyledString(ansiStr).Draw(buf, uv.Rect(0, 0, cols, rows))

	img := image.NewRGBA(image.Rect(0, 0, cols*cellW, rows*cellH))

	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			cell := buf.CellAt(x, y)
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
				drawer.DrawString(content)
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

// WritePNG renders the ANSI frame and writes it to path as a PNG file.
func WritePNG(path, ansiStr string, cols, rows int) error {
	img, err := RenderANSI(ansiStr, cols, rows)
	if err != nil {
		return err
	}
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
