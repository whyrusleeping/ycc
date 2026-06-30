package snapshot

import (
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

// A hand-built ANSI frame with explicit truecolor SGR exercises the rasterizer
// end to end: dimensions, color fidelity, bold and reverse.
func TestRenderANSIColored(t *testing.T) {
	const cols, rows = 20, 3
	// Red foreground "hi", then bold green, then reverse blue background.
	ansi := "\x1b[38;2;255;0;0mhi\x1b[0m\n" +
		"\x1b[1;38;2;0;255;0mbold\x1b[0m\n" +
		"\x1b[7;38;2;0;0;255mrev\x1b[0m"

	img, err := RenderANSI(ansi, cols, rows)
	if err != nil {
		t.Fatalf("RenderANSI: %v", err)
	}
	if img == nil {
		t.Fatal("RenderANSI returned nil image")
	}

	cw, ch, err := CellSize()
	if err != nil {
		t.Fatalf("CellSize: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != cols*cw || b.Dy() != rows*ch {
		t.Fatalf("image bounds = %dx%d, want %dx%d", b.Dx(), b.Dy(), cols*cw, rows*ch)
	}

	// Color must survive: at least one pixel differs from the default bg.
	foundColor := false
	foundRed := false
	for y := b.Min.Y; y < b.Max.Y && (!foundColor || !foundRed); y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			c := color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 0xff}
			if c != defaultBg {
				foundColor = true
			}
			if c.R > 0x80 && c.G < 0x40 && c.B < 0x40 {
				foundRed = true
			}
		}
	}
	if !foundColor {
		t.Fatal("rendered image is monochrome (no pixel differs from default bg)")
	}
	if !foundRed {
		t.Fatal("expected red foreground pixels from the SGR color, found none")
	}

	if dir := os.Getenv("YCC_TUI_SNAPSHOT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
		path := filepath.Join(dir, "snapshot_colored.png")
		if err := WritePNG(path, ansi, cols, rows); err != nil {
			t.Fatalf("WritePNG: %v", err)
		}
		t.Logf("wrote %s", path)
	}
}

func TestRenderANSIInvalidSize(t *testing.T) {
	if _, err := RenderANSI("x", 0, 5); err == nil {
		t.Fatal("expected error for zero cols")
	}
}
