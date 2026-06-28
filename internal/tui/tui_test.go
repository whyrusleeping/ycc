package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// eventAt maps a clicked content row back to the event whose block contains it —
// the core of click-to-expand.
func TestEventAt(t *testing.T) {
	m := &model{eventStart: []int{0, 3, 5, 9}}
	cases := []struct{ row, want int }{
		{-1, -1}, {0, 0}, {2, 0}, {3, 1}, {4, 1}, {5, 2}, {8, 2}, {9, 3}, {100, 3},
	}
	for _, c := range cases {
		if got := m.eventAt(c.row); got != c.want {
			t.Errorf("eventAt(%d) = %d, want %d", c.row, got, c.want)
		}
	}
}

func TestDiffDetectionAndColorize(t *testing.T) {
	diff := "diff --git a/x b/x\n@@ -1 +1 @@\n-old line\n+new line\n unchanged"
	if !looksDiff(diff) {
		t.Fatal("looksDiff should detect a git diff")
	}
	out := colorizeDiff(diff)
	for _, want := range []string{"old line", "new line", "unchanged", "@@ -1 +1 @@"} {
		if !strings.Contains(out, want) {
			t.Fatalf("colorizeDiff dropped %q:\n%s", want, out)
		}
	}
	if looksDiff("just some text\nno diff here") {
		t.Fatal("looksDiff false positive")
	}
}

func TestCatNDimming(t *testing.T) {
	src := "     1\tpackage main\n     2\tfunc main() {}"
	if !looksCatN(src) {
		t.Fatal("looksCatN should detect cat -n output")
	}
	out := dimLineNumbers(src)
	if !strings.Contains(out, "package main") || !strings.Contains(out, "func main() {}") {
		t.Fatalf("dimLineNumbers dropped code:\n%s", out)
	}
}

func TestPrettyArgs(t *testing.T) {
	out := prettyArgs(`{"file_path":"a.go","content":"x"}`)
	if !strings.Contains(out, "\n") || !strings.Contains(out, "file_path") {
		t.Fatalf("prettyArgs should indent JSON:\n%s", out)
	}
	if prettyArgs("not json") != "not json" {
		t.Fatal("prettyArgs should pass through non-JSON")
	}
}

func TestDetailLineToolCall(t *testing.T) {
	ev := &v1.Event{Type: "tool_call", DataJson: `{"name":"Read","args":"{\"file_path\":\"x\"}"}`}
	if d := detailLine(ev); !strings.HasPrefix(d, "Read(") {
		t.Fatalf("detailLine = %q", d)
	}
}

// The markdown renderer must build with a fixed style (no terminal query, which
// would block under Bubble Tea) and render content.
func TestRendererBuildsAndRenders(t *testing.T) {
	m := &model{w: 80}
	m.makeRenderer()
	if m.glam == nil {
		t.Fatal("renderer was not created")
	}
	out := m.markdown("# Title\n\nSome **bold** and `code`.")
	if !strings.Contains(out, "Title") {
		t.Fatalf("markdown render missing content: %q", out)
	}
}

func TestAutoExpand(t *testing.T) {
	if !autoExpand("session_idle") || !autoExpand("question_asked") {
		t.Fatal("session_idle and question_asked should auto-expand")
	}
	if autoExpand("tool_call") {
		t.Fatal("tool_call should not auto-expand")
	}
	// Thinking events should stay collapsed by default so they don't clutter.
	if autoExpand("thinking") {
		t.Fatal("thinking should not auto-expand")
	}
}

// cycle walks the thinking-level list in both directions and wraps around at the
// ends — the behavior the overlay's ←/→ keys rely on.
func TestCycleThinkLevels(t *testing.T) {
	if got := cycle(thinkLevels, "high", 1); got != "xhigh" {
		t.Fatalf("high +1 = %q, want xhigh", got)
	}
	if got := cycle(thinkLevels, "high", -1); got != "medium" {
		t.Fatalf("high -1 = %q, want medium", got)
	}
	if got := cycle(thinkLevels, "max", 1); got != "off" {
		t.Fatalf("max +1 = %q, want off (wrap)", got)
	}
	if got := cycle(thinkLevels, "off", -1); got != "max" {
		t.Fatalf("off -1 = %q, want max (wrap)", got)
	}
	// thinkLevels covers exactly the levels the session layer accepts.
	want := []string{"off", "low", "medium", "high", "xhigh", "max"}
	if strings.Join(thinkLevels, ",") != strings.Join(want, ",") {
		t.Fatalf("thinkLevels = %v, want %v", thinkLevels, want)
	}
}

// A thinking event renders a one-line "(reasoning)" detail and an expandable
// body carrying the reasoning summary.
func TestThinkingRendering(t *testing.T) {
	ev := &v1.Event{Type: "thinking", DataJson: `{"text":"first I will read the file","blocks":1}`}
	if d := detailLine(ev); !strings.Contains(d, "reasoning") || !strings.Contains(d, "read the file") {
		t.Fatalf("detailLine = %q", d)
	}
	m := &model{w: 80}
	body := m.renderBody(ev)
	if !strings.Contains(body, "read the file") {
		t.Fatalf("renderBody = %q", body)
	}
	// An empty reasoning summary produces no body (nothing to expand).
	empty := &v1.Event{Type: "thinking", DataJson: `{"text":""}`}
	if b := m.renderBody(empty); strings.TrimSpace(b) != "" {
		t.Fatalf("empty thinking body = %q", b)
	}
}

// needsOnboarding flags an un-onboarded workspace (no real spec.md + no backlog
// tasks) so the home menu can surface onboarding prominently (spec §19.2).
func TestNeedsOnboarding(t *testing.T) {
	t.Run("fresh empty dir", func(t *testing.T) {
		if !needsOnboarding(t.TempDir()) {
			t.Fatal("empty workspace should need onboarding")
		}
	})
	t.Run("non-trivial spec", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "spec.md", "# Spec\n\n## Goals\nship it\n")
		if needsOnboarding(ws) {
			t.Fatal("workspace with a real spec should not need onboarding")
		}
	})
	t.Run("backlog task but no spec", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "backlog/0001-foo.md", "# task\n")
		if needsOnboarding(ws) {
			t.Fatal("workspace with a backlog task should not need onboarding")
		}
	})
	t.Run("trivially empty spec, no backlog", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "spec.md", "# Spec\n")
		if !needsOnboarding(ws) {
			t.Fatal("trivially-empty spec + no backlog should need onboarding")
		}
	})
	t.Run("only generated index, no tasks", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "backlog/backlog.md", "# Backlog\n")
		if !needsOnboarding(ws) {
			t.Fatal("generated backlog index without tasks should still need onboarding")
		}
	})
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The status header must not latch on "error": after a session_error sets the
// status, a subsequent model_turn (forward progress) clears it back to running
// (task 0051).
func TestAppendEventClearsLatchedError(t *testing.T) {
	m := &model{w: 80, follow: true}
	m.appendEvent(&v1.Event{Type: "session_error", DataJson: `{"msg":"boom"}`})
	if m.status != "error" {
		t.Fatalf("after session_error status = %q, want error", m.status)
	}
	m.appendEvent(&v1.Event{Type: "model_turn", DataJson: `{"text":"recovered"}`})
	if m.status != "running" {
		t.Fatalf("after model_turn status = %q, want running", m.status)
	}
}

// The session view must fit exactly within the terminal: every rendered line must
// be no wider than the terminal (so nothing wraps to a second physical row) and
// the total number of lines must equal the terminal height. A wrapping footer or
// header pushes the frame down a row, which is what hides the agent's last output
// line behind the input box (task 0052).
func TestSessionViewFitsTerminal(t *testing.T) {
	sizes := []struct{ w, h int }{{80, 24}, {60, 20}}
	for _, sz := range sizes {
		m := model{
			state: stateSession, status: "running", mode: "implement",
			sessionID: "sess12345678abcdef", follow: true,
			expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		}
		updated, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		m = updated.(model)

		// Fill well past the viewport so the frame is full and GotoBottom is active.
		for i := 0; i < 40; i++ {
			m.appendEvent(&v1.Event{
				Seq: int64(i), Type: "model_turn", Actor: "coordinator",
				DataJson: fmt.Sprintf(`{"text":"this is a fairly long output line number %d that is meant to wrap inside the body region but must never overflow the terminal width"}`, i),
			})
		}
		// The agent's final output (multi-line), the line that was being clipped.
		m.appendEvent(&v1.Event{
			Seq: 100, Type: "session_idle", Actor: "coordinator",
			DataJson: `{"report":"final report line one\nfinal report line two\nthis is the last visible line of the final output"}`,
		})

		view := m.sessionView()
		lines := strings.Split(view, "\n")
		if len(lines) != sz.h {
			t.Fatalf("%dx%d: sessionView produced %d lines, want %d", sz.w, sz.h, len(lines), sz.h)
		}
		for i, ln := range lines {
			if w := lipgloss.Width(ln); w > sz.w {
				t.Fatalf("%dx%d: line %d width %d exceeds terminal width %d: %q", sz.w, sz.h, i, w, sz.w, ln)
			}
		}
	}
}
