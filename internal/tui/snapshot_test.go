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

// TestGenerateReadmeScreenshot renders a richer, larger representative session
// frame and, when YCC_README_SCREENSHOT_DIR is set, writes it as docs/tui.png —
// the screenshot the README leads with. It is deterministic (the model is
// constructed directly, no clock- or network-dependent content) so re-running it
// reproduces the same picture. Regenerate with:
//
//	YCC_README_SCREENSHOT_DIR=docs go test ./internal/tui -run TestGenerateReadmeScreenshot
//
// Plain `go test ./...` performs only the in-memory assertions and writes
// nothing.
func TestGenerateReadmeScreenshot(t *testing.T) {
	const cols, rows = 110, 32

	m := model{
		state: stateSession, status: "running", mode: "work",
		sessionID: "s_9f3ac21b", follow: true,
		expanded: map[int]bool{7: true}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: cols, Height: rows})
	m = updated.(model)

	m.appendEvent(&v1.Event{Seq: 1, Type: "user_input", Actor: "user", DataJson: `{"text":"Add shell completion for the ycc CLI"}`})
	m.appendEvent(&v1.Event{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"On it. I'll read the CLI entrypoint, wire up urfave/cli completion, then document it."}`})
	m.appendEvent(&v1.Event{Seq: 3, Type: "tool_call", Actor: "implementer", DataJson: `{"id":"c1","name":"Read","args":"{\"file_path\":\"cmd/ycc/main.go\"}"}`})
	m.appendEvent(&v1.Event{Seq: 4, Type: "tool_result", Actor: "implementer", DataJson: `{"id":"c1","result":"package main\n\nfunc newRootCommand(a *app) *cli.Command {\n\treturn &cli.Command{Name: \"ycc\"}\n}"}`})
	m.appendEvent(&v1.Event{Seq: 5, Type: "tool_call", Actor: "implementer", DataJson: `{"id":"c2","name":"Edit","args":"{\"file_path\":\"cmd/ycc/main.go\",\"old_string\":\"Name: \\\"ycc\\\"\",\"new_string\":\"Name: \\\"ycc\\\", EnableShellCompletion: true\"}"}`})
	m.appendEvent(&v1.Event{Seq: 6, Type: "tool_result", Actor: "implementer", DataJson: `{"id":"c2","result":"edited cmd/ycc/main.go (1 replacement)"}`})
	m.appendEvent(&v1.Event{Seq: 7, Type: "tool_call", Actor: "implementer", DataJson: `{"id":"c3","name":"Bash","args":"{\"command\":\"go build ./... && go test ./cmd/ycc\"}"}`})
	m.appendEvent(&v1.Event{Seq: 8, Type: "tool_result", Actor: "implementer", DataJson: `{"id":"c3","result":"ok  \tgithub.com/whyrusleeping/ycc/cmd/ycc\t0.412s\nPASS"}`})
	m.appendEvent(&v1.Event{Seq: 9, Type: "model_turn", Actor: "reviewer", DataJson: `{"text":"Reviewed the diff across models — the completion command is registered and the flag value completers are best-effort. LGTM."}`})
	m.appendEvent(&v1.Event{Seq: 10, Type: "review_submitted", Actor: "reviewer", DataJson: `{"model":"claude-opus","verdict":"approve","summary":"Clean, well-scoped change; the completers degrade gracefully with no daemon."}`})
	m.appendEvent(&v1.Event{Seq: 11, Type: "commit_made", Actor: "coordinator", DataJson: `{"sha":"a1b2c3d","message":"cmd/ycc: add shell completion (bash/zsh/fish) + dynamic values"}`})
	m.appendEvent(&v1.Event{Seq: 12, Type: "user_input", Actor: "user", DataJson: `{"text":"Nice — also add a Shell completions section to the README"}`})
	m.appendEvent(&v1.Event{Seq: 13, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"Good call. Adding a short section with the source-in-shell one-liners."}`})
	m.appendEvent(&v1.Event{Seq: 14, Type: "tool_call", Actor: "implementer", DataJson: `{"id":"c4","name":"Edit","args":"{\"file_path\":\"README.md\"}"}`})
	m.appendEvent(&v1.Event{Seq: 15, Type: "tool_result", Actor: "implementer", DataJson: `{"id":"c4","result":"edited README.md (1 replacement)"}`})
	m.appendEvent(&v1.Event{Seq: 16, Type: "session_idle", Actor: "coordinator", DataJson: `{"report":"## Done\n\nAdded ` + "`ycc completion bash|zsh|fish`" + ` plus dynamic completion for\nsession ids and ` + "`--project`" + ` names, and documented it.\n\n- wired EnableShellCompletion on the root command\n- ShellComplete funcs for attach/stop/start/cost/export\n- README + docs/cli.md sections; build and tests green"}`})
	m.rebuild()

	frame := m.render()

	img, err := snapshot.RenderANSI(frame, cols, rows)
	if err != nil {
		t.Fatalf("RenderANSI: %v", err)
	}
	b := img.Bounds()
	cw, ch, err := snapshot.CellSize()
	if err != nil {
		t.Fatalf("CellSize: %v", err)
	}
	if b.Dx() != cols*cw || b.Dy() != rows*ch {
		t.Fatalf("image bounds = %dx%d, want %dx%d", b.Dx(), b.Dy(), cols*cw, rows*ch)
	}

	dir := os.Getenv("YCC_README_SCREENSHOT_DIR")
	if dir == "" {
		return // in-memory assertions only; write nothing on a plain test run
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	path := filepath.Join(dir, "tui.png")
	if err := snapshot.WritePNG(path, frame, cols, rows); err != nil {
		t.Fatalf("WritePNG: %v", err)
	}
	t.Logf("wrote %dx%d README screenshot to %s", b.Dx(), b.Dy(), path)
}
