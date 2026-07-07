package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// newSelModel builds a minimal model with a live viewport and known content.
func newSelModel(t *testing.T, content string, w, h int) model {
	t.Helper()
	m := model{ready: true}
	m.vp = viewport.New(viewport.WithWidth(w), viewport.WithHeight(h))
	m.vpContent = content
	m.vp.SetContent(content)
	return m
}

// TestSelectionTextSingleLine drags across part of one line and expects the
// covered cells (inclusive of the cell under the pointer).
func TestSelectionTextSingleLine(t *testing.T) {
	m := newSelModel(t, "hello world\nsecond line", 40, 10)
	// Press at row 0 col 6 ("w"), drag to col 10 ("d").
	m.selMouseDown(6, headerHeight+0)
	m.selMouseMotion(10, headerHeight+0)
	text, dragged := m.selMouseUp()
	if !dragged {
		t.Fatalf("expected a drag")
	}
	if text != "world" {
		t.Fatalf("selection = %q, want %q", text, "world")
	}
}

// TestSelectionTextMultiLine covers first-line tail, full middle line, and
// last-line head — with trailing padding trimmed and ANSI stripped.
func TestSelectionTextMultiLine(t *testing.T) {
	styled := "one \x1b[31mred\x1b[0m tail  \nmiddle line\nlast row here"
	m := newSelModel(t, styled, 40, 10)
	m.selMouseDown(4, headerHeight+0) // start at "red"
	m.selMouseMotion(3, headerHeight+2)
	text, dragged := m.selMouseUp()
	if !dragged {
		t.Fatalf("expected a drag")
	}
	want := "red tail\nmiddle line\nlast"
	if text != want {
		t.Fatalf("selection = %q, want %q", text, want)
	}
}

// TestSelectionTextBackwards drags upward/leftward; bounds must normalize.
func TestSelectionTextBackwards(t *testing.T) {
	m := newSelModel(t, "alpha\nbravo\ncharlie", 40, 10)
	m.selMouseDown(2, headerHeight+2) // "a" in charlie (row 2 col 2)
	m.selMouseMotion(1, headerHeight+1)
	text, _ := m.selMouseUp()
	want := "ravo\ncha"
	if text != want {
		t.Fatalf("selection = %q, want %q", text, want)
	}
}

// TestSelectionClickIsNotDrag: press+release without motion must report no
// drag so the caller applies plain-click behaviour.
func TestSelectionClickIsNotDrag(t *testing.T) {
	m := newSelModel(t, "alpha\nbravo", 40, 10)
	m.selMouseDown(3, headerHeight+1)
	text, dragged := m.selMouseUp()
	if dragged || text != "" {
		t.Fatalf("click reported as drag (text=%q)", text)
	}
	if m.selDrag || m.selRegion {
		t.Fatalf("selection state not cleared after release")
	}
}

// TestSelectionScrolledViewport verifies coordinates are content-relative:
// with the viewport scrolled, screen row 0 of the body maps to content row
// YOffset.
func TestSelectionScrolledViewport(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = strings.Repeat("x", i+1)
	}
	m := newSelModel(t, strings.Join(lines, "\n"), 40, 5)
	m.vp.SetYOffset(10)
	m.selMouseDown(0, headerHeight+0) // content row 10
	m.selMouseMotion(5, headerHeight+0)
	text, _ := m.selMouseUp()
	if text != "xxxxxx" {
		t.Fatalf("selection = %q, want %q", text, "xxxxxx")
	}
}

// TestOverlaySelectionPreservesText: the highlight overlay changes styling
// only — the plain text of the body must be unchanged, and the selected span
// must be wrapped in reverse video.
func TestOverlaySelectionPreservesText(t *testing.T) {
	m := newSelModel(t, "hello world\nsecond line\nthird", 40, 10)
	m.selMouseDown(6, headerHeight+0)
	m.selMouseMotion(2, headerHeight+1)
	if !m.selRegion {
		t.Fatalf("expected an active selection region")
	}
	body := m.vp.View()
	got := m.overlaySelection(body)
	if ansi.Strip(got) != ansi.Strip(body) {
		t.Fatalf("overlay changed text:\n got %q\nwant %q", ansi.Strip(got), ansi.Strip(body))
	}
	if !strings.Contains(got, "\x1b[7m") {
		t.Fatalf("overlay did not apply reverse video: %q", got)
	}
	// Without an active region the overlay is a no-op.
	m.selRegion = false
	if out := m.overlaySelection(body); out != body {
		t.Fatalf("overlay not a no-op without a selection")
	}
}

// TestSelectionEdgeAutoScroll: dragging above the viewport top scrolls up and
// keeps the head glued to the top visible row.
func TestSelectionEdgeAutoScroll(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "row"
	}
	m := newSelModel(t, strings.Join(lines, "\n"), 40, 5)
	m.vp.SetYOffset(10)
	m.selMouseDown(1, headerHeight+2) // content row 12
	m.selMouseMotion(1, 0)            // above the header: scroll up one line
	if m.vp.YOffset() != 9 {
		t.Fatalf("YOffset = %d, want 9", m.vp.YOffset())
	}
	if m.selHeadRow != 9 {
		t.Fatalf("selHeadRow = %d, want 9", m.selHeadRow)
	}
}

// newSelSessionModel builds a live-session model with a couple of events, sized
// so mouse messages route through updateSession.
func newSelSessionModel(t *testing.T) model {
	t.Helper()
	m := model{
		state: stateSession, status: "running", follow: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.appendEvent(&v1.Event{Seq: 1, Type: "user_input", Actor: "user", DataJson: `{"text":"go"}`})
	m.appendEvent(&v1.Event{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"hello there this is a turn"}`})
	m.rebuild()
	return m
}

// TestClickTogglesOnRelease: a left press + release without motion applies the
// old click behaviour (select + expand the row under the pointer) — on the
// RELEASE, so it stays distinguishable from a drag.
func TestClickTogglesOnRelease(t *testing.T) {
	m := newSelSessionModel(t)
	row := m.eventStart[1] // the model_turn's first content line
	y := headerHeight + row - m.vp.YOffset()

	updated, _ := m.Update(tea.MouseClickMsg{X: 3, Y: y, Button: tea.MouseLeft})
	m = updated.(model)
	if len(m.expanded) != 0 {
		t.Fatalf("press alone should not toggle (expanded=%v)", m.expanded)
	}
	// Legacy X10 encoding reports releases as Button==MouseNone; the click must
	// still land.
	updated, _ = m.Update(tea.MouseReleaseMsg{X: 3, Y: y, Button: tea.MouseNone})
	m = updated.(model)
	if m.selected != 1 {
		t.Fatalf("release should select the clicked row: selected=%d, want 1", m.selected)
	}
	if !m.expanded[2] {
		t.Fatalf("clicked row (seq 2) not expanded")
	}
}

// TestDragCopiesToClipboard: press + motion + release selects a region instead
// of toggling, and the release emits a SetClipboard (OSC 52) command.
func TestDragCopiesToClipboard(t *testing.T) {
	m := newSelSessionModel(t)
	row := m.eventStart[1]
	y := headerHeight + row - m.vp.YOffset()

	updated, _ := m.Update(tea.MouseClickMsg{X: 0, Y: y, Button: tea.MouseLeft})
	m = updated.(model)
	updated, _ = m.Update(tea.MouseMotionMsg{X: 20, Y: y, Button: tea.MouseLeft})
	m = updated.(model)
	if !m.selRegion {
		t.Fatalf("motion while pressed should open a selection region")
	}
	updated, cmd := m.Update(tea.MouseReleaseMsg{X: 20, Y: y, Button: tea.MouseLeft})
	m = updated.(model)
	if len(m.expanded) != 0 {
		t.Fatalf("a drag must not toggle rows (expanded=%v)", m.expanded)
	}
	if cmd == nil {
		t.Fatalf("drag release should emit a clipboard command")
	}
	if !containsClipboardCmd(cmd()) {
		t.Fatalf("drag release cmd does not include a SetClipboard message")
	}
}

// containsClipboardCmd walks a (possibly batched) command's messages looking
// for bubbletea's private setClipboardMsg.
func containsClipboardCmd(msg tea.Msg) bool {
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil && containsClipboardCmd(c()) {
				return true
			}
		}
		return false
	}
	return strings.Contains(fmt.Sprintf("%T", msg), "setClipboardMsg")
}
