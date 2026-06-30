package tui

import (
	"image/color"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/whyrusleeping/ycc/internal/tui/snapshot"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// TestSnapshotSessionView drives a representative session screen to an image
// and asserts it is a valid, colored frame of the expected pixel dimensions.
// When YCC_TUI_SNAPSHOT_DIR is set the PNG is written there so a maintainer (or
// the agent, via the multimodal Read tool) can visually inspect the layout.
// Ordinary `go test ./...` performs only the in-memory assertions and writes
// nothing.
func TestSnapshotSessionView(t *testing.T) {
	const cols, rows = 80, 24

	m := model{
		state: stateSession, status: "running", mode: "implement",
		sessionID: "sess12345678abcdef", follow: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: cols, Height: rows})
	m = updated.(model)

	m.appendEvent(&v1.Event{Seq: 1, Type: "user_input", Actor: "user", DataJson: `{"text":"add a feature"}`})
	m.appendEvent(&v1.Event{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"On it — reading the file first."}`})
	m.appendEvent(&v1.Event{Seq: 3, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read","args":"{\"file_path\":\"main.go\"}"}`})
	m.appendEvent(&v1.Event{Seq: 4, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"package main\nfunc main() {}"}`})
	m.appendEvent(&v1.Event{Seq: 5, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"Done. The change is in place."}`})
	m.rebuild()

	frame := m.render()

	img, err := snapshot.RenderANSI(frame, cols, rows)
	if err != nil {
		t.Fatalf("RenderANSI: %v", err)
	}
	if img == nil {
		t.Fatal("RenderANSI returned nil image")
	}

	cw, ch, err := snapshot.CellSize()
	if err != nil {
		t.Fatalf("CellSize: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != cols*cw || b.Dy() != rows*ch {
		t.Fatalf("image bounds = %dx%d, want %dx%d", b.Dx(), b.Dy(), cols*cw, rows*ch)
	}

	// The session view uses themed colors; confirm SGR survived to pixels.
	defaultBg := color.RGBA{0x12, 0x12, 0x12, 0xff}
	colored := false
	for y := b.Min.Y; y < b.Max.Y && !colored; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			c := color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 0xff}
			if c != defaultBg {
				colored = true
				break
			}
		}
	}
	if !colored {
		t.Fatal("session view rendered monochrome (no color survived to pixels)")
	}

	if dir := os.Getenv("YCC_TUI_SNAPSHOT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
		path := filepath.Join(dir, "session_view.png")
		if err := snapshot.WritePNG(path, frame, cols, rows); err != nil {
			t.Fatalf("WritePNG: %v", err)
		}
		t.Logf("wrote %dx%d snapshot to %s", b.Dx(), b.Dy(), path)
	}
}
