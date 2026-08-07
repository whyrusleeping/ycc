package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// dataField must surface JSON booleans as "true"/"false" so checks like the
// tool_result error routing (dataField(ev,"error") == "true") work — the engine
// emits "error" as a JSON bool.
func TestDataFieldBool(t *testing.T) {
	if got := dataField(&v1.Event{DataJson: `{"error":true}`}, "error"); got != "true" {
		t.Fatalf("dataField bool true = %q, want \"true\"", got)
	}
	if got := dataField(&v1.Event{DataJson: `{"error":false}`}, "error"); got != "false" {
		t.Fatalf("dataField bool false = %q, want \"false\"", got)
	}
}

func TestLexerNameForPath(t *testing.T) {
	cases := []struct {
		path    string
		want    string // exact name, or "" for empty
		contain string // substring expectation when want is ""
	}{
		{"main.go", "", "Go"},
		{"sub/dir/x.py", "Python", ""},
		{"a.ts", "TypeScript", ""},
		{"noext", "", ""},
		{"weird.zzzzz", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		got := lexerNameForPath(c.path)
		switch {
		case c.want != "":
			if got != c.want {
				t.Errorf("lexerNameForPath(%q) = %q, want %q", c.path, got, c.want)
			}
		case c.contain != "":
			if !strings.Contains(got, c.contain) {
				t.Errorf("lexerNameForPath(%q) = %q, want containing %q", c.path, got, c.contain)
			}
		default:
			if got != "" {
				t.Errorf("lexerNameForPath(%q) = %q, want \"\"", c.path, got)
			}
		}
	}
}

func TestLexerNameForCommand(t *testing.T) {
	if got := lexerNameForCommand("rg -g '*.go' foo"); !strings.Contains(got, "Go") {
		t.Errorf("rg -g '*.go' => %q, want Go", got)
	}
	if got := lexerNameForCommand("rg --type py foo"); got != "Python" {
		t.Errorf("rg --type py => %q, want Python", got)
	}
	if got := lexerNameForCommand("rg -t go foo src/"); !strings.Contains(got, "Go") {
		t.Errorf("rg -t go => %q, want Go", got)
	}
	if got := lexerNameForCommand("rg --glob=*.py foo"); got != "Python" {
		t.Errorf("rg --glob=*.py => %q, want Python", got)
	}
	// Ambiguous: a Go type AND a Python glob.
	if got := lexerNameForCommand("rg -t go -g '*.py' foo"); got != "" {
		t.Errorf("ambiguous mixed => %q, want \"\"", got)
	}
	// No restriction at all.
	if got := lexerNameForCommand("rg foo"); got != "" {
		t.Errorf("rg foo => %q, want \"\"", got)
	}
	if got := lexerNameForCommand("grep -rn foo ."); got != "" {
		t.Errorf("grep -rn => %q, want \"\"", got)
	}
	// Negated glob alone is ignored (not a positive restriction).
	if got := lexerNameForCommand("rg -g '!*.go' foo"); got != "" {
		t.Errorf("negated glob => %q, want \"\"", got)
	}
}

func TestLexerNameForGrepPaths(t *testing.T) {
	uniform := "internal/a.go:10:func A() {}\ninternal/b.go:3:func B() {}"
	if got := lexerNameForGrepPaths(uniform); !strings.Contains(got, "Go") {
		t.Errorf("uniform .go => %q, want Go", got)
	}
	mixed := "a.go:1:x\nb.py:2:y"
	if got := lexerNameForGrepPaths(mixed); got != "" {
		t.Errorf("mixed => %q, want \"\"", got)
	}
	none := "just some text\nno prefixes here"
	if got := lexerNameForGrepPaths(none); got != "" {
		t.Errorf("no prefixes => %q, want \"\"", got)
	}
	// Column-numbered prefixes are also recognized.
	withCol := "a.go:10:5:func A() {}\nb.go:1:1:package main"
	if got := lexerNameForGrepPaths(withCol); !strings.Contains(got, "Go") {
		t.Errorf("with col => %q, want Go", got)
	}
}

func TestHighlightCodeFallbacks(t *testing.T) {
	const code = "func main() {}"
	if got := highlightCode(code, ""); got != code {
		t.Errorf("empty lexer should return input unchanged, got %q", got)
	}
	if got := highlightCode(code, "no-such-lexer-xyz"); got != code {
		t.Errorf("unknown lexer should return input unchanged, got %q", got)
	}
}

func TestHighlightCatNNeverDrops(t *testing.T) {
	src := "     1\tpackage main\n     2\tfunc main() {}"
	out := highlightCatN(src, "Go")
	if !strings.Contains(stripANSI(out), "package main") || !strings.Contains(stripANSI(out), "func main() {}") {
		t.Fatalf("highlightCatN dropped code:\n%q", out)
	}
	// Line count must be preserved.
	if got, want := len(strings.Split(out, "\n")), 2; got != want {
		t.Fatalf("highlightCatN line count = %d, want %d", got, want)
	}
	// With no lexer it behaves like dimLineNumbers.
	if got := highlightCatN(src, ""); got != dimLineNumbers(src) {
		t.Fatalf("highlightCatN with no lexer should equal dimLineNumbers")
	}
}

func TestHighlightGrepNeverDrops(t *testing.T) {
	src := "internal/x.go:10:func Foo() {}"
	out := highlightGrep(src, "Go")
	plain := stripANSI(out)
	if !strings.Contains(plain, "func Foo() {}") {
		t.Fatalf("highlightGrep dropped match text:\n%q", out)
	}
	if !strings.Contains(plain, "internal/x.go:10:") {
		t.Fatalf("highlightGrep dropped path prefix:\n%q", out)
	}
	// Non-prefixed lines pass through; with no lexer the input is unchanged.
	if got := highlightGrep(src, ""); got != src {
		t.Fatalf("highlightGrep with no lexer should return input unchanged")
	}
}

// fmtDurMS renders sub-second durations in milliseconds and longer ones as
// one-decimal seconds.
func TestFmtDurMS(t *testing.T) {
	cases := map[int64]string{
		0:    "0ms",
		340:  "340ms",
		999:  "999ms",
		1000: "1.0s",
		1200: "1.2s",
		1250: "1.2s",
		9999: "10.0s",
	}
	for ms, want := range cases {
		if got := fmtDurMS(ms); got != want {
			t.Errorf("fmtDurMS(%d) = %q, want %q", ms, got, want)
		}
	}
}

// Collapsed model_turn and tool_result rows append a compact duration suffix
// when duration_ms is positive, and omit it otherwise.
func TestDetailLineDuration(t *testing.T) {
	mt := &v1.Event{Type: "model_turn", DataJson: `{"text":"done","duration_ms":1200}`}
	if d := detailLine(mt); !strings.Contains(d, "1.2s") || !strings.Contains(d, "done") {
		t.Fatalf("model_turn detailLine = %q, want text + 1.2s", d)
	}
	tr := &v1.Event{Type: "tool_result", DataJson: `{"result":"ok","duration_ms":340}`}
	if d := detailLine(tr); !strings.Contains(d, "340ms") || !strings.Contains(d, "ok") {
		t.Fatalf("tool_result detailLine = %q, want result + 340ms", d)
	}
	// No duration field -> no suffix.
	noDur := &v1.Event{Type: "model_turn", DataJson: `{"text":"done"}`}
	if d := detailLine(noDur); strings.Contains(d, "ms") || strings.Contains(d, "s ") {
		t.Fatalf("model_turn without duration should have no suffix: %q", d)
	}
	// Zero duration -> no suffix.
	zeroDur := &v1.Event{Type: "tool_result", DataJson: `{"result":"ok","duration_ms":0}`}
	if d := detailLine(zeroDur); d != "ok" {
		t.Fatalf("zero duration should add no suffix: %q", d)
	}
}

// TestSessionBrowseParity verifies task 0112: the browse selector (ctrl+o) and
// the read-only session browser modal (ctrl+r / browse → sessions) are reachable
// from within a live session, browsing there never disturbs (or reopens over) the
// live session, and esc unwinds transcript → list → session.
func TestSessionBrowseParity(t *testing.T) {
	f := newFakeClient()
	f.history = []*v1.SessionSummary{
		{SessionId: "s_hist", Mode: "chat", Status: "idle", Title: "old chat", LastActivity: "2024-01-01T10:00:00Z"},
	}
	f.transcript = []*v1.Event{
		{Seq: 10, Type: "user_input", Actor: "user", DataJson: `{"text":"replayed"}`},
		{Seq: 11, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"replayed reply"}`},
	}
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s_live", mode: "work", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	// Give the live session some events so we can detect clobbering.
	m.appendEvent(&v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"live one"}`})
	m.appendEvent(&v1.Event{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"live two"}`})
	m.rebuild()
	liveDelivered := len(m.deliveredSeqs)

	// ctrl+o opens the browse selector from a session; esc returns to the session.
	m = drive(t, m, "ctrl+o")
	if !m.browse {
		t.Fatal("ctrl+o should open the browse selector from a session")
	}
	m = drive(t, m, "esc")
	if m.browse {
		t.Fatal("esc should dismiss the browse selector")
	}
	if m.state != stateSession {
		t.Fatalf("esc from the browse selector should return to the session (state=%v)", m.state)
	}
	if len(m.evs) != 2 {
		t.Fatalf("browse selector must not touch the live session events (evs=%d, want 2)", len(m.evs))
	}

	// browse → sessions from a session opens the read-only modal, NOT stateHistory.
	m = drive(t, m, "ctrl+o")
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	m = drive(t, m, "enter") // "sessions" is the third browse target
	if m.state != stateSession {
		t.Fatalf("browse → sessions from a session must stay stateSession (state=%v)", m.state)
	}
	if !m.histModal {
		t.Fatal("browse → sessions from a session should open the session browser modal")
	}
	if len(m.history) != 1 {
		t.Fatalf("session browser modal should load history (len=%d, want 1)", len(m.history))
	}
	// esc closes the modal back to the live session.
	m = drive(t, m, "esc")
	if m.histModal {
		t.Fatal("esc should close the session browser modal")
	}

	// ctrl+r from a session opens the same read-only modal directly.
	m = drive(t, m, "ctrl+r")
	if !m.histModal || m.state != stateSession {
		t.Fatalf("ctrl+r from a session should open the modal over the session (histModal=%v state=%v)", m.histModal, m.state)
	}

	// enter loads the transcript into the modal viewport WITHOUT clobbering the
	// live session's event pipeline.
	m = drive(t, m, "enter")
	if !m.histModalTranscript {
		t.Fatal("enter should drill into the read-only transcript modal")
	}
	if f.lastTransID != "s_hist" {
		t.Fatalf("GetSessionTranscript id=%q, want s_hist", f.lastTransID)
	}
	if len(m.evs) != 2 {
		t.Fatalf("modal transcript must not clobber live session evs (evs=%d, want 2)", len(m.evs))
	}
	if len(m.deliveredSeqs) != liveDelivered {
		t.Fatalf("modal transcript must not clobber live deliveredSeqs (%d, want %d)", len(m.deliveredSeqs), liveDelivered)
	}
	// The modal transcript renders the replayed events into its own viewport.
	if view := m.histModalView(); !strings.Contains(view, "transcript") {
		t.Fatalf("histModalView (transcript) missing title:\n%s", view)
	}

	// `o` in the modal transcript is a no-op (no reopen-over-live-session footgun).
	m = drive(t, m, "o")
	if f.lastReopened != "" {
		t.Fatalf("`o` in the modal transcript must not reopen (lastReopened=%q)", f.lastReopened)
	}
	if !m.histModalTranscript {
		t.Fatal("`o` in the modal transcript should be a no-op (still on the transcript)")
	}

	// esc unwinds: transcript → list.
	m = drive(t, m, "esc")
	if m.histModalTranscript {
		t.Fatal("esc should leave the transcript back to the list")
	}
	if !m.histModal {
		t.Fatal("esc from the transcript should return to the list, not close the modal")
	}

	// `o` in the modal list is also a no-op.
	m = drive(t, m, "o")
	if f.lastReopened != "" {
		t.Fatalf("`o` in the modal list must not reopen (lastReopened=%q)", f.lastReopened)
	}
	if !m.histModal {
		t.Fatal("`o` in the modal list should be a no-op (still open)")
	}

	// esc from the list closes the modal, back to the live session intact.
	m = drive(t, m, "esc")
	if m.histModal {
		t.Fatal("esc should close the session browser modal")
	}
	if m.state != stateSession || m.sessionID != "s_live" {
		t.Fatalf("after closing the modal, back to the live session (state=%v id=%q)", m.state, m.sessionID)
	}
	if len(m.evs) != 2 {
		t.Fatalf("live session events must be intact after browsing (evs=%d, want 2)", len(m.evs))
	}
}

// TestQuitGuardOneShotRunning covers task 0109: on a one-shot daemon with a live
// running session, the first ctrl+c arms the guard (no quit) and shows a warning;
// a second ctrl+c quits.
func TestQuitGuardOneShotRunning(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false) // one-shot
	m.w, m.h = 80, 24
	m.state, m.status, m.sessionID = stateSession, "running", "sess-1"

	updated, cmd := m.Update(keyMsg("ctrl+c"))
	m = updated.(model)
	if isQuit(cmd) {
		t.Fatal("first ctrl+c on a running one-shot session should NOT quit")
	}
	if !m.quitArmed {
		t.Fatal("first ctrl+c should arm the quit guard")
	}
	if view := m.render(); !strings.Contains(view, quitGuardHint) {
		t.Fatalf("armed view should show the quit-guard warning; got:\n%s", view)
	}

	_, cmd = m.Update(keyMsg("ctrl+c"))
	if !isQuit(cmd) {
		t.Fatal("second ctrl+c should quit")
	}
}

// TestQuitGuardIdleImmediate: no live work → ctrl+c quits at once.
func TestQuitGuardIdleImmediate(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.state, m.status, m.sessionID = stateSession, "idle", "sess-1"
	if _, cmd := m.Update(keyMsg("ctrl+c")); !isQuit(cmd) {
		t.Fatal("ctrl+c on an idle session should quit immediately")
	}
}

// TestQuitGuardPersistentImmediate: on a persistent daemon the work survives, so
// quit stays immediate even while running.
func TestQuitGuardPersistentImmediate(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, true) // persistent
	m.state, m.status, m.sessionID = stateSession, "running", "sess-1"
	if _, cmd := m.Update(keyMsg("ctrl+c")); !isQuit(cmd) {
		t.Fatal("ctrl+c on a persistent daemon should quit immediately")
	}
}

// TestQuitGuardDisarm: a matching quitDisarmMsg clears the armed state (so the
// next ctrl+c re-arms instead of quitting), while a stale seq is ignored.
func TestQuitGuardDisarm(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.state, m.status, m.sessionID = stateSession, "running", "sess-1"

	updated, _ := m.Update(keyMsg("ctrl+c"))
	m = updated.(model)
	if !m.quitArmed {
		t.Fatal("ctrl+c should arm the guard")
	}
	seq := m.quitSeq

	// Stale seq: ignored.
	updated, _ = m.Update(quitDisarmMsg{seq: seq - 1})
	m = updated.(model)
	if !m.quitArmed {
		t.Fatal("stale quitDisarmMsg must not disarm the guard")
	}

	// Matching seq: disarms.
	updated, _ = m.Update(quitDisarmMsg{seq: seq})
	m = updated.(model)
	if m.quitArmed {
		t.Fatal("matching quitDisarmMsg should disarm the guard")
	}

	// Next ctrl+c re-arms (does not quit).
	_, cmd := m.Update(keyMsg("ctrl+c"))
	if isQuit(cmd) {
		t.Fatal("after disarm, next ctrl+c should re-arm, not quit")
	}
}

// TestHelpModalOpensAndCloses verifies `?` opens the keybinding help modal over
// the menu (empty prompt), the card shows the title and several section
// headings, and esc closes it back to the menu (task 0111).
func TestHelpModalOpensAndCloses(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	// Tall enough for the first four catalog sections to render without scrolling;
	// the catalog grows over time (task 0127 added a session binding, drag-select
	// another), so keep headroom above the "question picker" row.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 46})
	m = updated.(model)

	m = drive(t, m, "?")
	if !m.helpOpen {
		t.Fatal("`?` on the menu with an empty prompt should open the help modal")
	}
	view := m.render()
	if !strings.Contains(view, "keybindings") {
		t.Fatalf("help view missing the title:\n%s", view)
	}
	for _, want := range []string{"global", "home menu", "session", "question picker"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help view missing section %q:\n%s", want, view)
		}
	}
	if !strings.ContainsAny(view, "╭╰│╮╯") {
		t.Fatalf("help modal does not render a rounded card:\n%s", view)
	}
	for i, ln := range strings.Split(view, "\n") {
		if w := lipgloss.Width(ln); w > 80 {
			t.Fatalf("help view line %d width %d exceeds terminal width 80: %q", i, w, ln)
		}
	}

	m = drive(t, m, "esc")
	if m.helpOpen {
		t.Fatal("esc should close the help modal")
	}
	if m.state != stateMenu {
		t.Fatalf("closing help should return to the menu, state = %v", m.state)
	}
}

// TestHelpKeyTypesIntoNonEmptyInput verifies `?` types a literal '?' rather than
// opening the modal when the focused input is non-empty — on both the menu and a
// session (task 0111).
func TestHelpKeyTypesIntoNonEmptyInput(t *testing.T) {
	// Menu: prompt has text, so `?` types.
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m = updated.(model)
	m = typeText(t, m, "write a")
	m = typeText(t, m, "?")
	if m.helpOpen {
		t.Fatal("`?` should not open help while the menu prompt is non-empty")
	}
	if got := m.prompt.Value(); got != "write a?" {
		t.Fatalf("`?` did not type into the prompt: value = %q", got)
	}

	// Session: input has text, so `?` types.
	s := newSessionTextareaModel(t)
	s = typeText(t, s, "fix the")
	s = typeText(t, s, "?")
	if s.helpOpen {
		t.Fatal("`?` should not open help while the session input is non-empty")
	}
	if got := s.input.Value(); got != "fix the?" {
		t.Fatalf("`?` did not type into the session input: value = %q", got)
	}
}

// TestHelpCtrlUnderscoreOpensUnconditionally verifies ctrl+_ opens help even with
// a non-empty session input, and that `?` opens help from the question picker
// (no free-text input focused there) — task 0111.
func TestHelpCtrlUnderscoreOpensUnconditionally(t *testing.T) {
	s := newSessionTextareaModel(t)
	s = typeText(t, s, "some text")
	s = drive(t, s, "ctrl+_")
	if !s.helpOpen {
		t.Fatal("ctrl+_ should open help even with a non-empty session input")
	}

	// Picking state: `?` opens help.
	p := newSessionTextareaModel(t)
	p.picking = true
	p.pickerOpts = []string{"a", "b"}
	p = drive(t, p, "?")
	if !p.helpOpen {
		t.Fatal("`?` should open help from the question picker")
	}
}
