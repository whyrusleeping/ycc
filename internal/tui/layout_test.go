package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// TestSpinnerInInputRow verifies the activity spinner moved from the top status
// bar to the bottom input row (task 0076): while running, the input row shows a
// spinner frame and the status bar shows only the static dot.
func TestSpinnerInInputRow(t *testing.T) {
	m := model{
		state: stateSession, status: "running", mode: "implement",
		sessionID: "sess12345678abcdef", follow: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		spin: spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	glyph := m.spin.View()
	if glyph == "" {
		t.Fatal("spinner produced an empty frame")
	}

	if row := m.inputRow(); !strings.Contains(row, glyph) {
		t.Fatalf("inputRow should contain spinner frame %q while running:\n%s", glyph, row)
	}
	if bar := m.statusBar(); strings.Contains(bar, glyph) {
		t.Fatalf("status bar should not contain spinner frame %q (spinner moved to input): %q", glyph, bar)
	}
	if bar := m.statusBar(); !strings.Contains(bar, "●") {
		t.Fatalf("status bar should contain the static dot ●: %q", bar)
	}

	// On idle the gutter is blank: the input row no longer carries a spinner.
	m.status = "idle"
	if row := m.inputRow(); strings.Contains(row, glyph) {
		t.Fatalf("inputRow should not animate while idle: %q", row)
	}
}

// TestDropMouseFragment verifies the defensive filter that swallows leaked
// SGR mouse-report bytes (a hardening kept across the bubbletea v2 upgrade)
// without eating real typing. See model.dropMouseFragment.
func TestDropMouseFragment(t *testing.T) {
	runes := func(s string) tea.KeyMsg {
		return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	}
	altBracket := tea.KeyPressMsg{Code: '[', Text: "[", Mod: tea.ModAlt}

	// With recent mouse activity, mouse-report fragments are dropped.
	recent := model{lastMouse: time.Now()}
	for _, k := range []tea.KeyMsg{runes("<65;80;12M"), runes("35;86;14"), runes("65;80"), altBracket} {
		if !recent.dropMouseFragment(k) {
			t.Errorf("expected fragment %q to be dropped", k.String())
		}
	}
	// Real typing is never dropped, even right after scrolling.
	for _, k := range []tea.KeyMsg{runes("hello"), runes("a"), runes("<3"), runes("5"), runes(";")} {
		if recent.dropMouseFragment(k) {
			t.Errorf("expected real input %q to be kept", k.String())
		}
	}
	// Without recent mouse activity, nothing is dropped.
	stale := model{lastMouse: time.Now().Add(-time.Second)}
	if stale.dropMouseFragment(runes("<65;80;12M")) {
		t.Error("expected no drop when no recent mouse activity")
	}
}

// TestChatInputWordMotion verifies Ctrl+Left/Ctrl+Right perform word-wise
// cursor movement in the multi-line chat-input textarea (task 0102). The
// bubbles v2 textarea binds word motions to alt-arrows by default; newChatInput
// additionally binds ctrl+left/ctrl+right (matching the single-line textinput).
func TestChatInputWordMotion(t *testing.T) {
	ta := newChatInput("")
	ta.Focus()
	ta.SetWidth(80) // wide enough that the value never soft-wraps
	// "hello world foo": hello=[0,4], space 5, world=[6,10], space 11, foo=[12,14]
	ta.SetValue("hello world foo")
	ta.CursorEnd()

	col := func() int {
		li := ta.LineInfo()
		return li.StartColumn + li.ColumnOffset
	}
	ctrlLeft := tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl}
	ctrlRight := tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}

	// Ctrl+Left steps back to the start of each previous word.
	for _, want := range []int{12, 6, 0} {
		ta, _ = ta.Update(ctrlLeft)
		if got := col(); got != want {
			t.Fatalf("ctrl+left cursor col = %d, want %d", got, want)
		}
	}
	// Extra Ctrl+Left at the start of the buffer must not crash or move.
	ta, _ = ta.Update(ctrlLeft)
	if got := col(); got != 0 {
		t.Fatalf("ctrl+left at buffer start moved cursor to %d, want 0", got)
	}

	// Ctrl+Right steps forward to each next word boundary (end of word).
	for _, want := range []int{5, 11, 15} {
		ta, _ = ta.Update(ctrlRight)
		if got := col(); got != want {
			t.Fatalf("ctrl+right cursor col = %d, want %d", got, want)
		}
	}
	// Extra Ctrl+Right at the end of the buffer must not crash or move.
	ta, _ = ta.Update(ctrlRight)
	if got := col(); got != 15 {
		t.Fatalf("ctrl+right at buffer end moved cursor to %d, want 15", got)
	}

	// Word motion left the text untouched.
	if got := ta.Value(); got != "hello world foo" {
		t.Fatalf("word motion mutated value: %q", got)
	}
}

// TestFootersMentionHelp verifies the menu and default session footers advertise
// the help key (task 0111).
func TestFootersMentionHelp(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = updated.(model)
	if !strings.Contains(m.menuView(), "? help") {
		t.Fatalf("menu footer should mention the help key:\n%s", m.menuView())
	}

	s := newSessionTextareaModel(t)
	updated, _ = s.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	s = updated.(model)
	if !strings.Contains(s.sessionView(), "? help") {
		t.Fatalf("session footer should mention the help key:\n%s", s.sessionView())
	}
}
