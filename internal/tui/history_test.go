package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// TestPreviousSessionsReopen drives the menu -> session browser -> reopen flow
// (spec §18.6): ctrl+r opens the browser and loads the list, ↓ moves the cursor,
// and `o` reopens the selected session via ResumeSession, entering the session
// view. (Enter now drills into the transcript; see TestSessionBrowserTranscript.)
func TestPreviousSessionsReopen(t *testing.T) {
	f := newFakeClient()
	f.history = []*v1.SessionSummary{
		{SessionId: "s_newer", Mode: "work", Status: "idle", Title: "build the thing", LastActivity: "2024-01-02T10:00:00Z"},
		{SessionId: "s_older", Mode: "chat", Status: "stopped", Title: "ask questions", LastActivity: "2024-01-01T10:00:00Z"},
	}
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	if m.state != stateMenu {
		t.Fatalf("initial state=%v, want stateMenu", m.state)
	}

	// ctrl+r opens the session browser and loads history.
	m = drive(t, m, "ctrl+r")
	if m.state != stateHistory {
		t.Fatalf("after ctrl+r state=%v, want stateHistory", m.state)
	}
	if len(m.history) != 2 {
		t.Fatalf("history len=%d, want 2", len(m.history))
	}

	// Navigate to the second row and reopen it with `o`.
	m = drive(t, m, "down")
	if m.historyCursor != 1 {
		t.Fatalf("historyCursor=%d, want 1", m.historyCursor)
	}
	m = drive(t, m, "o")

	if f.lastReopened != "s_older" {
		t.Fatalf("reopened %q, want s_older", f.lastReopened)
	}
	if m.sessionID != "s_older" {
		t.Fatalf("sessionID=%q, want s_older", m.sessionID)
	}
	if m.mode != "chat" {
		t.Fatalf("mode=%q, want chat", m.mode)
	}
	if m.state != stateSession {
		t.Fatalf("state=%v, want stateSession", m.state)
	}
}

// TestSessionBrowserTranscript drives the session browser transcript drill-in
// (spec §18.6): Enter on a row fetches the transcript via GetSessionTranscript
// and loads it into the event-rendering pipeline (read-only), Esc backs out to
// the list, and `o` from the transcript reopens the session.
func TestSessionBrowserTranscript(t *testing.T) {
	f := newFakeClient()
	f.history = []*v1.SessionSummary{
		{SessionId: "s1", Mode: "work", Status: "idle", Title: "do the thing", LastActivity: "2024-01-02T10:00:00Z"},
	}
	f.transcript = []*v1.Event{
		{Seq: 1, Type: "user_input", Actor: "user", DataJson: `{"text":"go"}`},
		{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"on it"}`},
	}
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = drive(t, m, "ctrl+r")
	if m.state != stateHistory {
		t.Fatalf("state=%v, want stateHistory", m.state)
	}

	// Enter drills into the read-only transcript (no reopen).
	m = drive(t, m, "enter")
	if !m.historyTranscript {
		t.Fatal("enter should open the transcript drill-in")
	}
	if f.lastTransID != "s1" {
		t.Fatalf("GetSessionTranscript id=%q, want s1", f.lastTransID)
	}
	if f.lastReopened != "" {
		t.Fatalf("transcript drill-in must not reopen the session (lastReopened=%q)", f.lastReopened)
	}
	if len(m.evs) != 2 {
		t.Fatalf("transcript loaded %d events into the pipeline, want 2", len(m.evs))
	}
	// The transcript renders via the shared event components.
	view := m.transcriptView()
	if !strings.Contains(view, "transcript") {
		t.Fatalf("transcriptView missing title:\n%s", view)
	}

	// Esc backs out to the list and clears the transient transcript state.
	m = drive(t, m, "esc")
	if m.historyTranscript {
		t.Fatal("esc should leave the transcript drill-in")
	}
	if m.state != stateHistory {
		t.Fatalf("after esc state=%v, want stateHistory (back to list)", m.state)
	}
	if len(m.evs) != 0 {
		t.Fatalf("esc should clear transient transcript events, got %d", len(m.evs))
	}

	// Re-enter the transcript and reopen via `o`.
	m = drive(t, m, "enter")
	m = drive(t, m, "o")
	if f.lastReopened != "s1" {
		t.Fatalf("reopen from transcript: lastReopened=%q, want s1", f.lastReopened)
	}
	if m.state != stateSession || m.sessionID != "s1" {
		t.Fatalf("after reopen state=%v sessionID=%q, want stateSession/s1", m.state, m.sessionID)
	}
}

// TestSessionBrowserModalTranscriptNav verifies task 0119: the session-browser
// modal transcript (opened OVER a live session) supports line-based `/` search
// (n/N wrap, esc-clear) and {}()<>[] jump-to-event keys, and NONE of it disturbs
// the live session behind the modal (m.evs/m.vp/m.searching/m.searchQuery).
func TestSessionBrowserModalTranscriptNav(t *testing.T) {
	f := newFakeClient()
	f.history = []*v1.SessionSummary{
		{SessionId: "s_hist", Mode: "chat", Status: "idle", Title: "old chat", LastActivity: "2024-01-01T10:00:00Z"},
	}
	f.transcript = []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"intro alpha line"}`},
		{Seq: 2, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"pick something"}`},
		{Seq: 3, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"middle beta content"}`},
		{Seq: 4, Type: "review_submitted", Actor: "reviewer", DataJson: `{"model":"claude","verdict":"approve","summary":"looks good"}`},
		{Seq: 5, Type: "commit_made", Actor: "coordinator", DataJson: `{"sha":"abc123","message":"do the thing"}`},
		{Seq: 6, Type: "session_error", Actor: "coordinator", DataJson: `{"msg":"boom failure"}`},
		{Seq: 7, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"tail alpha zebra"}`},
	}
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s_live", mode: "work", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.appendEvent(&v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"live one"}`})
	m.appendEvent(&v1.Event{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"live two"}`})
	m.rebuild()
	// Seed a kept LIVE-session search + selection to prove they survive the modal.
	m.searchQuery = "live"
	m.selected = 1
	liveOffset := m.vp.YOffset()

	assertLiveIntact := func(where string) {
		t.Helper()
		if len(m.evs) != 2 {
			t.Fatalf("%s: live evs = %d, want 2", where, len(m.evs))
		}
		if m.searchQuery != "live" || m.searching {
			t.Fatalf("%s: live search clobbered (query=%q searching=%v)", where, m.searchQuery, m.searching)
		}
		if m.selected != 1 {
			t.Fatalf("%s: live selection clobbered (selected=%d, want 1)", where, m.selected)
		}
		if m.vp.YOffset() != liveOffset {
			t.Fatalf("%s: live viewport offset moved (%d, want %d)", where, m.vp.YOffset(), liveOffset)
		}
	}

	// Open the modal and drill into the transcript.
	m = drive(t, m, "ctrl+r")
	m = drive(t, m, "enter")
	if !m.histModalTranscript {
		t.Fatal("enter should drill into the modal transcript")
	}
	if len(m.histModalLines) == 0 {
		t.Fatal("modal transcript should capture rendered lines")
	}
	if len(m.histModalEventLines) != 7 {
		t.Fatalf("modal transcript should record 7 event start lines, got %d", len(m.histModalEventLines))
	}
	if m.histModalCurLine != -1 {
		t.Fatalf("a fresh transcript has no cursor line (got %d)", m.histModalCurLine)
	}
	assertLiveIntact("after loading the modal transcript")

	lineForType := func(typ string) int {
		for _, el := range m.histModalEventLines {
			if el.typ == typ {
				return el.line
			}
		}
		return -2
	}
	curText := func() string { return ansi.Strip(m.histModalLines[m.histModalCurLine]) }

	// Jump keys land on the recorded start line of the right event type.
	m = drive(t, m, "}") // forward to question_asked
	if m.histModalCurLine != lineForType("question_asked") {
		t.Fatalf("} should jump to question_asked (line=%d, want %d)", m.histModalCurLine, lineForType("question_asked"))
	}
	m = drive(t, m, ")") // forward to review_submitted
	if m.histModalCurLine != lineForType("review_submitted") {
		t.Fatalf(") should jump to review_submitted (line=%d, want %d)", m.histModalCurLine, lineForType("review_submitted"))
	}
	m = drive(t, m, ">") // forward to commit_made
	if m.histModalCurLine != lineForType("commit_made") {
		t.Fatalf("> should jump to commit_made (line=%d, want %d)", m.histModalCurLine, lineForType("commit_made"))
	}
	m = drive(t, m, "]") // forward to session_error
	if m.histModalCurLine != lineForType("session_error") {
		t.Fatalf("] should jump to session_error (line=%d, want %d)", m.histModalCurLine, lineForType("session_error"))
	}
	// No-wrap: another forward session_error jump is a no-op.
	atError := m.histModalCurLine
	m = drive(t, m, "]")
	if m.histModalCurLine != atError {
		t.Fatalf("] with no further error should be a no-op (line=%d, want %d)", m.histModalCurLine, atError)
	}
	// Backward jump to the (earlier) question_asked.
	m = drive(t, m, "{")
	if m.histModalCurLine != lineForType("question_asked") {
		t.Fatalf("{ should jump back to question_asked (line=%d)", m.histModalCurLine)
	}
	assertLiveIntact("after jump keys")

	// `/` search: "alpha" matches exactly two lines (intro + tail).
	var matchLines []int
	for i, ln := range m.histModalLines {
		if strings.Contains(strings.ToLower(ansi.Strip(ln)), "alpha") {
			matchLines = append(matchLines, i)
		}
	}
	if len(matchLines) != 2 {
		t.Fatalf("expected 2 lines containing alpha, got %v", matchLines)
	}
	inMatches := func(l int) bool { return l == matchLines[0] || l == matchLines[1] }

	m = drive(t, m, "/")
	if !m.histModalSearching {
		t.Fatal("`/` should start the modal search")
	}
	for _, r := range "alpha" {
		m = drive(t, m, string(r))
	}
	first := m.histModalCurLine
	if !inMatches(first) || !strings.Contains(curText(), "alpha") {
		t.Fatalf("typing should jump to a matching line (line=%d text=%q)", first, curText())
	}
	m = drive(t, m, "enter") // keep the query for n/N
	if m.histModalSearching {
		t.Fatal("enter should confirm and stop owning input")
	}
	m = drive(t, m, "n")
	second := m.histModalCurLine
	if second == first || !inMatches(second) {
		t.Fatalf("n should advance to the other match (first=%d second=%d)", first, second)
	}
	m = drive(t, m, "n")
	if m.histModalCurLine != first {
		t.Fatalf("n should wrap back to the first match (got %d, want %d)", m.histModalCurLine, first)
	}
	m = drive(t, m, "N")
	if m.histModalCurLine != second {
		t.Fatalf("N should wrap back to the other match (got %d, want %d)", m.histModalCurLine, second)
	}
	assertLiveIntact("after search n/N")

	// esc with a kept query clears it but stays in the transcript.
	m = drive(t, m, "esc")
	if m.histModalQuery != "" {
		t.Fatalf("esc should clear the kept query (got %q)", m.histModalQuery)
	}
	if !m.histModalTranscript {
		t.Fatal("esc with an active query should NOT back out of the transcript")
	}
	// A second esc backs out to the list.
	m = drive(t, m, "esc")
	if m.histModalTranscript {
		t.Fatal("a second esc should back out of the transcript to the list")
	}
	if !m.histModal {
		t.Fatal("backing out of the transcript should stay in the modal list")
	}
	if len(m.histModalLines) != 0 {
		t.Fatal("backing out should reset the modal transcript nav state")
	}
	assertLiveIntact("after backing out of the transcript")

	// esc-cancel while typing: re-enter the transcript, type, then esc cancels.
	m = drive(t, m, "enter")
	m = drive(t, m, "/")
	m = drive(t, m, "z")
	m = drive(t, m, "esc")
	if m.histModalSearching || m.histModalQuery != "" {
		t.Fatalf("esc should cancel search entry (searching=%v query=%q)", m.histModalSearching, m.histModalQuery)
	}
	if !m.histModalTranscript {
		t.Fatal("esc-cancel should stay in the transcript")
	}
	assertLiveIntact("after esc-cancel while typing")

	// Close the whole modal; the live session behind it is fully intact.
	m = drive(t, m, "esc") // transcript → list
	m = drive(t, m, "esc") // list → close
	if m.histModal {
		t.Fatal("esc from the list should close the modal")
	}
	assertLiveIntact("after closing the modal")
}

// TestHistoryRowsPrefixFocusTasks: the session browser prefixes each row's title
// with the backlog task(s) the session worked, so the list shows at a glance
// which task each session was on; sessions with no focus are unprefixed.
func TestHistoryRowsPrefixFocusTasks(t *testing.T) {
	m := model{w: 100, history: []*v1.SessionSummary{
		{SessionId: "sess-a", Title: "make the tests pass", Mode: "work", Status: "done", FocusTasks: []string{"0007", "0009"}},
		{SessionId: "sess-b", Title: "just a chat", Mode: "chat", Status: "idle"},
	}}
	rows := m.historyRows()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if !strings.Contains(rows[0].text, "[0007,0009] make the tests pass") {
		t.Fatalf("focused session row should prefix its tasks: %q", rows[0].text)
	}
	if strings.Contains(rows[1].text, "[") {
		t.Fatalf("unfocused session row must not carry a task prefix: %q", rows[1].text)
	}
}

func TestTranscriptSearchAndEsc(t *testing.T) {
	m := searchEvsModel(t, []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"alpha one"}`},
		{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"gamma alpha two"}`},
	})
	m.state = stateHistory
	m.historyTranscript = true
	m.historyTransID = "sess1"

	m = press(m, "/")
	if !m.searching {
		t.Fatal("`/` did not enter search in the transcript")
	}
	m = typeSearch(m, "alpha")
	if m.selected != 0 {
		t.Fatalf("transcript search selection = %d, want 0", m.selected)
	}
	m = press(m, "enter")
	m = press(m, "n")
	if m.selected != 1 {
		t.Fatalf("transcript n = %d, want 1", m.selected)
	}
	// First esc clears the search but stays in the transcript.
	m = press(m, "esc")
	if m.searchQuery != "" {
		t.Fatalf("esc did not clear the transcript search: %q", m.searchQuery)
	}
	if !m.historyTranscript {
		t.Fatal("esc with active search should NOT back out of the transcript")
	}
	// Second esc backs out to the list.
	m = press(m, "esc")
	if m.historyTranscript {
		t.Fatal("second esc should back out of the transcript")
	}
}
