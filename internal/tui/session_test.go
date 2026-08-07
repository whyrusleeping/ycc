package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/whyrusleeping/ycc/internal/clientconfig"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

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

// TestReopenClearsStaleQuestion guards the reopen-replay variant of the dead
// input regression: a session whose log ends with an unanswered question_asked
// (e.g. it was stopped while blocked on ask_user) is repaired on reopen — the
// daemon gives the dangling ask_user call a synthetic tool result, so the
// question is no longer answerable. The session_reopened marker replayed after
// it must dismiss the stale picker/wizard and re-focus the textarea instead of
// leaving the TUI stuck on a question that can never be answered.
func TestReopenClearsStaleQuestion(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f) // replayed log ends with an options question_asked
	if m.input.Focused() {
		t.Fatal("precondition: the replayed picker question should have blurred the textarea")
	}

	m.appendEvent(&v1.Event{Seq: 2, Type: "session_reopened", Actor: "system"})
	if m.picking || m.pending != "" || m.pickerOpts != nil {
		t.Fatalf("session_reopened should drop the stale picker (picking=%v pending=%q opts=%v)",
			m.picking, m.pending, m.pickerOpts)
	}
	if !m.input.Focused() {
		t.Fatal("session_reopened must re-focus the textarea")
	}
}

func TestSessionInputEnterSendsAndClears(t *testing.T) {
	m := newSessionTextareaModel(t)
	m = typeText(t, m, "hello agent")
	if m.input.Value() != "hello agent" {
		t.Fatalf("setup: value = %q, want %q", m.input.Value(), "hello agent")
	}
	updated, cmd := m.Update(keyMsg("enter"))
	m = updated.(model)
	if m.input.Value() != "" {
		t.Fatalf("enter did not clear input: %q", m.input.Value())
	}
	if cmd == nil {
		t.Fatalf("enter on non-empty input should issue a send command")
	}
}

func TestSessionInputShiftEnterInsertsNewline(t *testing.T) {
	m := newSessionTextareaModel(t)
	m = typeText(t, m, "ab")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m = updated.(model)
	m = typeText(t, m, "cd")
	if m.input.Value() != "ab\ncd" {
		t.Fatalf("shift+enter: value = %q, want %q", m.input.Value(), "ab\ncd")
	}
	if m.input.LineCount() != 2 {
		t.Fatalf("shift+enter: LineCount = %d, want 2", m.input.LineCount())
	}
}

func TestSessionInputCtrlJInsertsNewline(t *testing.T) {
	m := newSessionTextareaModel(t)
	m = typeText(t, m, "ab")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	m = updated.(model)
	m = typeText(t, m, "cd")
	if m.input.Value() != "ab\ncd" {
		t.Fatalf("ctrl+j: value = %q, want %q", m.input.Value(), "ab\ncd")
	}
	if m.input.LineCount() != 2 {
		t.Fatalf("ctrl+j: LineCount = %d, want 2", m.input.LineCount())
	}
}

// While the session is still running, `q` is not a menu-exit — it types into the
// input like any other character and leaves the session untouched.
func TestSessionQTypesWhileRunning(t *testing.T) {
	m := newSessionTextareaModel(t) // status defaults to "running"
	m.sessionID = "s1"
	fc := newFakeClient()
	m.client = fc
	m.ctx = context.Background()

	if m.sessionFinished() {
		t.Fatal("setup: a running session must not be considered finished")
	}
	updated, _ := m.Update(keyMsg("q"))
	m = updated.(model)
	if m.state != stateSession {
		t.Fatalf("q while running: state = %v, want stateSession", m.state)
	}
	if m.input.Value() != "q" {
		t.Fatalf("q while running should type into the input, got %q", m.input.Value())
	}
	if fc.stopCount != 0 {
		t.Fatalf("q while running must not issue StopSession, got %d calls", fc.stopCount)
	}
}

// Even on a finished session, `q` types normally when the input is non-empty so
// it never hijacks composition mid-message.
func TestSessionQTypesWhenInputNonEmpty(t *testing.T) {
	m := newSessionTextareaModel(t)
	m.status = "idle"
	m.sessionID = "s1"
	fc := newFakeClient()
	m.client = fc
	m.ctx = context.Background()
	m = typeText(t, m, "hi")

	updated, _ := m.Update(keyMsg("q"))
	m = updated.(model)
	if m.state != stateSession {
		t.Fatalf("q with non-empty input: state = %v, want stateSession", m.state)
	}
	if m.input.Value() != "hiq" {
		t.Fatalf("q with non-empty input should type, got %q", m.input.Value())
	}
	if fc.stopCount != 0 {
		t.Fatalf("q with non-empty input must not issue StopSession, got %d calls", fc.stopCount)
	}
}

// A looping (idle) session belongs to the work-loop driver, which owns the
// idle→stop→advance transition. The `q` binding must NOT hijack it back to menu.
func TestSessionQIgnoredWhileLooping(t *testing.T) {
	m := newSessionTextareaModel(t)
	m.status = "idle"
	m.looping = true
	m.mode = "work"
	m.sessionID = "s1"
	fc := newFakeClient()
	m.client = fc
	m.ctx = context.Background()

	if m.sessionFinished() {
		t.Fatal("a looping session must never be considered finished (loop owns it)")
	}
	updated, _ := m.Update(keyMsg("q"))
	m = updated.(model)
	if m.state != stateSession {
		t.Fatalf("q while looping: state = %v, want stateSession", m.state)
	}
}

// The footer surfaces the "return to menu" affordance only once the session is
// finished — never while it is still running.
func TestSessionViewFinishedHint(t *testing.T) {
	m := newSessionTextareaModel(t)

	m.status = "running"
	if strings.Contains(m.sessionView(), "return to menu") {
		t.Fatalf("running session must not show the return-to-menu hint:\n%s", m.sessionView())
	}

	m.status = "idle"
	view := m.sessionView()
	if !strings.Contains(view, "session finished") || !strings.Contains(view, "q return to menu") {
		t.Fatalf("finished session should show the return-to-menu hint, got:\n%s", view)
	}
}

// TestInterruptKeyHintReflectsEnhancement checks the footer advertises ctrl+x
// (the universal chord) until the terminal reports kitty keyboard disambiguation,
// after which it shows ctrl+i (byte-identical to Tab, so only usable there).
func TestInterruptKeyHintReflectsEnhancement(t *testing.T) {
	m := newSessionTextareaModel(t)
	// Widen the terminal so the footer isn't clamped/truncated before the hint.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 24})
	m = updated.(model)
	if got := m.interruptKeyHint(); got != "ctrl+x" {
		t.Fatalf("without enhancement: hint = %q, want %q", got, "ctrl+x")
	}
	if v := m.render(); !strings.Contains(v, "ctrl+x interrupt") {
		t.Fatalf("footer should advertise ctrl+x interrupt without enhancement:\n%s", v)
	}

	updated, _ = m.Update(tea.KeyboardEnhancementsMsg{Flags: 1})
	m = updated.(model)
	if got := m.interruptKeyHint(); got != "ctrl+i" {
		t.Fatalf("with enhancement: hint = %q, want %q", got, "ctrl+i")
	}
	if v := m.render(); !strings.Contains(v, "ctrl+i interrupt") {
		t.Fatalf("footer should advertise ctrl+i interrupt with enhancement:\n%s", v)
	}
}

// TestSessionCtrlXInterruptsWithoutEditingInput ensures ctrl+x triggers the
// interrupt path on every terminal and does not leak into the session textarea.
func TestSessionCtrlXInterruptsWithoutEditingInput(t *testing.T) {
	m := newSessionTextareaModel(t)
	m = typeText(t, m, "steer me")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m = updated.(model)
	if cmd == nil {
		t.Fatalf("ctrl+x while running should issue an interrupt command")
	}
	if m.input.Value() != "steer me" {
		t.Fatalf("ctrl+x should not edit the input: value = %q", m.input.Value())
	}
}

func TestSessionInputHeightCapsWithNewlines(t *testing.T) {
	m := newSessionTextareaModel(t)
	for i := 0; i < maxInputRows+3; i++ {
		m = typeText(t, m, "x")
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
		m = updated.(model)
	}
	if m.input.Height() != maxInputRows {
		t.Fatalf("height with many newlines = %d, want %d", m.input.Height(), maxInputRows)
	}
}

func TestSessionInputGrowsOnSoftWrap(t *testing.T) {
	m := newSessionTextareaModel(t)
	m = typeText(t, m, strings.Repeat("a", 200))
	if m.input.Height() <= 1 {
		t.Fatalf("soft-wrapped long line height = %d, want > 1", m.input.Height())
	}
	m2 := newSessionTextareaModel(t)
	m2 = typeText(t, m2, strings.Repeat("a", 600))
	if m2.input.Height() != maxInputRows {
		t.Fatalf("very long wrapped line height = %d, want %d (capped)", m2.input.Height(), maxInputRows)
	}
}

func TestSessionInputRelayoutFitsTerminal(t *testing.T) {
	m := newSessionTextareaModel(t)
	m = typeText(t, m, strings.Repeat("a", 600))
	if got := len(strings.Split(m.sessionView(), "\n")); got != 24 {
		t.Fatalf("sessionView produced %d lines, want 24", got)
	}
}

// TestTransientErrorKeepsSessionUsable verifies that a failed RPC on a live,
// connected session surfaces as an inline status-bar flash while the session
// view keeps rendering its events and accepting input — never the full-screen
// fatal error (task 0104).
func TestTransientErrorKeepsSessionUsable(t *testing.T) {
	m := model{
		state: stateSession, status: "running", follow: true, connected: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		thinkLevels: map[string]string{"coordinator": "high"},
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.input.Focus()
	m.appendEvent(&v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"hello world"}`})
	m.rebuild()

	// A failed SendInput/etc. produces errMsg. Because the client is connected
	// this must NOT set the fatal error.
	updated, _ = m.Update(errMsg{err: fmt.Errorf("send failed: boom")})
	m = updated.(model)
	if m.err != nil {
		t.Fatalf("transient errMsg set fatal m.err = %v, want nil", m.err)
	}
	if m.flashErr == "" {
		t.Fatalf("transient errMsg did not set flashErr")
	}

	view := m.render()
	if strings.Contains(view, "r to retry") || strings.Contains(view, "ctrl+c to quit") {
		t.Fatalf("transient error rendered the fatal screen:\n%s", view)
	}
	if !strings.Contains(view, "hello world") {
		t.Fatalf("session events no longer render after transient error:\n%s", view)
	}
	if !strings.Contains(view, "send failed: boom") {
		t.Fatalf("status bar does not surface the inline error:\n%s", view)
	}

	// The input still accepts text.
	updated, _ = m.Update(keyMsg("x"))
	m = updated.(model)
	if !strings.Contains(m.input.Value(), "x") {
		t.Fatalf("input did not accept text after transient error: value=%q", m.input.Value())
	}

	// The clear tick dismisses the flash (a stale tick with an old seq would not).
	updated, _ = m.Update(flashClearMsg{seq: m.flashSeq})
	m = updated.(model)
	if m.flashErr != "" {
		t.Fatalf("flash did not clear on the matching tick: %q", m.flashErr)
	}
}

// TestTransientErrorClearsOnSuccess verifies the inline flash clears when the
// next successful RPC result arrives (task 0104).
func TestTransientErrorClearsOnSuccess(t *testing.T) {
	m := model{
		state: stateSession, status: "running", connected: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		thinkLevels: map[string]string{},
	}
	updated, _ := m.Update(errMsg{err: fmt.Errorf("hiccup")})
	m = updated.(model)
	if m.flashErr == "" {
		t.Fatalf("errMsg did not arm flash")
	}
	// A successful backlog fetch result clears the flash.
	updated, _ = m.Update(backlogMsg{tasks: nil})
	m = updated.(model)
	if m.flashErr != "" {
		t.Fatalf("flash did not clear on a successful RPC result: %q", m.flashErr)
	}
}

// TestFatalStartupErrorRendersRetry verifies that an RPC failure before the
// client has ever reached the daemon is fatal, renders the full-screen error,
// offers a retry, and that "r" re-runs Init while leaving quit intact (task
// 0104).
func TestFatalStartupErrorRendersRetry(t *testing.T) {
	m := model{
		state: stateMenu, connected: false,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		thinkLevels: map[string]string{},
	}
	updated, _ := m.Update(errMsg{err: fmt.Errorf("dial daemon: connection refused")})
	m = updated.(model)
	if m.err == nil {
		t.Fatalf("startup errMsg (not connected) did not set fatal m.err")
	}
	view := m.render()
	if !strings.Contains(view, "connection refused") {
		t.Fatalf("fatal screen missing error text:\n%s", view)
	}
	if !strings.Contains(view, "retry") {
		t.Fatalf("fatal screen missing retry affordance:\n%s", view)
	}

	// "r" clears the fatal error and re-runs the Init fetches.
	updated, cmd := m.Update(keyMsg("r"))
	m = updated.(model)
	if m.err != nil {
		t.Fatalf("retry did not clear fatal m.err = %v", m.err)
	}
	if cmd == nil {
		t.Fatalf("retry did not return a command to re-run Init")
	}
}

// TestMaybeNotify covers the terminal-bell / OSC 9 notification gate (task
// 0108): bell/desktop emitted for genuinely-new trigger events when enabled,
// suppressed for replayed events, disabled prefs, non-trigger types, and for
// session_idle while looping.
func TestMaybeNotify(t *testing.T) {
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	after := base.Add(time.Second)   // newer than notifyAfter → genuine
	before := base.Add(-time.Second) // older → replay

	// swap notifyOut and restore.
	orig := notifyOut
	defer func() { notifyOut = orig }()
	run := func(m *model, ev *v1.Event) string {
		var buf strings.Builder
		notifyOut = &buf
		m.maybeNotify(ev)
		return buf.String()
	}

	newModel := func(bell, desktop, looping bool) *model {
		return &model{
			prefs:       clientconfig.Prefs{NotifyBell: bell, NotifyDesktop: desktop},
			notifyAfter: base,
			looping:     looping,
		}
	}
	ev := func(typ, ts, dataJSON string) *v1.Event {
		return &v1.Event{Type: typ, Ts: ts, DataJson: dataJSON}
	}

	// Bell on, question after subscribe → BEL.
	if got := run(newModel(true, false, false), ev("question_asked", after.Format(time.RFC3339), `{"question":"proceed?"}`)); got != "\a" {
		t.Fatalf("bell on question_asked = %q, want BEL", got)
	}
	// Desktop on (bell off) → OSC 9 with question text, no BEL.
	if got := run(newModel(false, true, false), ev("question_asked", after.Format(time.RFC3339), `{"question":"proceed?"}`)); got != "\x1b]9;ycc: proceed?\x07" {
		t.Fatalf("desktop on question_asked = %q", got)
	}
	// Both on → BEL then OSC 9, single write.
	if got := run(newModel(true, true, false), ev("question_asked", after.Format(time.RFC3339), `{"question":"proceed?"}`)); got != "\a\x1b]9;ycc: proceed?\x07" {
		t.Fatalf("both on = %q", got)
	}
	// Replayed event (ts before notifyAfter) → nothing.
	if got := run(newModel(true, true, false), ev("question_asked", before.Format(time.RFC3339), `{"question":"proceed?"}`)); got != "" {
		t.Fatalf("replayed event should be silent, got %q", got)
	}
	// Both prefs off → nothing.
	if got := run(newModel(false, false, false), ev("question_asked", after.Format(time.RFC3339), "")); got != "" {
		t.Fatalf("prefs off should be silent, got %q", got)
	}
	// Non-trigger type → nothing.
	if got := run(newModel(true, true, false), ev("model_turn", after.Format(time.RFC3339), "")); got != "" {
		t.Fatalf("non-trigger type should be silent, got %q", got)
	}
	// Looping suppresses session_idle bell.
	if got := run(newModel(true, false, true), ev("session_idle", after.Format(time.RFC3339), "")); got != "" {
		t.Fatalf("looping session_idle should be silent, got %q", got)
	}
	// Looping does NOT suppress session_error.
	if got := run(newModel(true, false, true), ev("session_error", after.Format(time.RFC3339), "")); got != "\a" {
		t.Fatalf("looping session_error should ring, got %q", got)
	}
	// question_asked with no question text → generic desktop label.
	if got := run(newModel(false, true, false), ev("question_asked", after.Format(time.RFC3339), "")); got != "\x1b]9;ycc: question waiting\x07" {
		t.Fatalf("empty question desktop = %q", got)
	}
	// Auto-answered question (unattended execution) → silent.
	if got := run(newModel(true, true, false), ev("question_asked", after.Format(time.RFC3339), `{"question":"proceed?","auto":true}`)); got != "" {
		t.Fatalf("auto:true question should be silent, got %q", got)
	}
	// Batch (multi-question) ask carries prompts under "questions"; desktop text
	// uses the first prompt.
	if got := run(newModel(false, true, false), ev("question_asked", after.Format(time.RFC3339), `{"questions":[{"question":"first?"},{"question":"second?"}]}`)); got != "\x1b]9;ycc: first?\x07" {
		t.Fatalf("batch questions desktop = %q", got)
	}
	// Unparseable timestamp → nothing (can't tell replay from live).
	if got := run(newModel(true, true, false), ev("question_asked", "not-a-time", "")); got != "" {
		t.Fatalf("bad ts should be silent, got %q", got)
	}
}

// TestSanitizeNotifyRuneBoundary verifies truncation happens on a rune boundary
// so a multibyte rune is never split (task 0108 polish).
func TestSanitizeNotifyRuneBoundary(t *testing.T) {
	// 130 multibyte runes (é is 2 bytes) — must truncate to 120 whole runes.
	in := strings.Repeat("é", 130)
	got := sanitizeNotify(in)
	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a rune: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 120 {
		t.Fatalf("rune count after truncation = %d, want 120", n)
	}
}
