package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestSearchableTextMatching(t *testing.T) {
	m := searchEvsModel(t, []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"hello ALPHA world"}`},
		{Seq: 2, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read"}`},
		{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"secret payload"}`},
	})
	// Case-insensitive headline match.
	if !m.matchesQuery(0, "alpha") {
		t.Fatalf("expected case-insensitive headline match for %q", m.searchableText(0))
	}
	// A folded tool_result (hidden row) never matches on its own.
	if !m.hiddenRow(2) {
		t.Fatal("setup: tool_result should be folded (hidden) into its call")
	}
	if m.searchableText(2) != "" {
		t.Fatalf("hidden folded result should have empty searchable text, got %q", m.searchableText(2))
	}
	if m.matchesQuery(2, "secret") {
		t.Fatal("hidden folded result should not match a query")
	}

	// Body text matches only when the row is expanded.
	body := searchEvsModel(t, []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"# heading\n\nzebra body content here"}`},
	})
	body.expanded[1] = true
	body.bodyCache = map[int]string{}
	if !body.matchesQuery(0, "zebra") {
		t.Fatalf("expanded body should match; text=%q", body.searchableText(0))
	}
}

func TestSessionSearchJumpsAndCycles(t *testing.T) {
	m := searchEvsModel(t, []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"alpha one"}`},
		{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"beta two"}`},
		{Seq: 3, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"gamma ALPHA three"}`},
	})
	// Enter search and type the query: selection jumps to the first match.
	m = press(m, "/")
	if !m.searching {
		t.Fatal("`/` did not enter search mode")
	}
	m = typeSearch(m, "alpha")
	if m.selected != 0 {
		t.Fatalf("after typing, selection = %d, want 0", m.selected)
	}
	// Confirm keeps the query active for n/N.
	m = press(m, "enter")
	if m.searching {
		t.Fatal("enter should confirm and exit search entry")
	}
	if m.searchQuery != "alpha" {
		t.Fatalf("query = %q, want alpha", m.searchQuery)
	}
	// n cycles to the next match, then wraps.
	m = press(m, "n")
	if m.selected != 2 {
		t.Fatalf("n: selection = %d, want 2", m.selected)
	}
	m = press(m, "n")
	if m.selected != 0 {
		t.Fatalf("n wrap: selection = %d, want 0", m.selected)
	}
	// N cycles backward with wrap.
	m = press(m, "N")
	if m.selected != 2 {
		t.Fatalf("N: selection = %d, want 2", m.selected)
	}
	// esc clears the search and re-focuses the input for normal typing.
	m = press(m, "esc")
	if m.searching || m.searchQuery != "" {
		t.Fatalf("esc did not clear search: searching=%v query=%q", m.searching, m.searchQuery)
	}
	m = typeText(t, m, "n")
	if m.input.Value() != "n" {
		t.Fatalf("after esc, typing should reach the textarea, got %q", m.input.Value())
	}
}

func TestSearchDoesNotHijackNonEmptyInput(t *testing.T) {
	m := searchEvsModel(t, []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"alpha"}`},
	})
	m = typeText(t, m, "hello")
	// `/` with non-empty input types a slash rather than opening search.
	m = press(m, "/")
	if m.searching {
		t.Fatal("`/` opened search despite non-empty input")
	}
	if m.input.Value() != "hello/" {
		t.Fatalf("`/` should type into the textarea, got %q", m.input.Value())
	}
	// n with an active query but non-empty input types rather than cycling.
	m.searchQuery = "alpha"
	m = press(m, "n")
	if m.input.Value() != "hello/n" {
		t.Fatalf("n should type into the textarea, got %q", m.input.Value())
	}
}

func TestJumpToEventKeys(t *testing.T) {
	m := searchEvsModel(t, []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"a"}`},
		{Seq: 2, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"why?"}`},
		{Seq: 3, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"b"}`},
		{Seq: 4, Type: "review_submitted", Actor: "reviewer", DataJson: `{"verdict":"approve","summary":"lgtm"}`},
		{Seq: 5, Type: "commit_made", Actor: "coordinator", DataJson: `{"sha":"abc","message":"done"}`},
		{Seq: 6, Type: "session_error", Actor: "coordinator", DataJson: `{"msg":"boom"}`},
		{Seq: 7, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"next?"}`},
	})
	m = press(m, "}") // next question
	if m.selected != 1 {
		t.Fatalf("} to first question = %d, want 1", m.selected)
	}
	m = press(m, "}") // next question
	if m.selected != 6 {
		t.Fatalf("} to next question = %d, want 6", m.selected)
	}
	m = press(m, "{") // prev question
	if m.selected != 1 {
		t.Fatalf("{ to prev question = %d, want 1", m.selected)
	}
	m = press(m, ")") // next review
	if m.selected != 3 {
		t.Fatalf(") to review = %d, want 3", m.selected)
	}
	m = press(m, ">") // next commit
	if m.selected != 4 {
		t.Fatalf("> to commit = %d, want 4", m.selected)
	}
	m = press(m, "]") // next error
	if m.selected != 5 {
		t.Fatalf("] to error = %d, want 5", m.selected)
	}
	m = press(m, "[") // prev error: none before index 5 -> no-op
	if m.selected != 5 {
		t.Fatalf("[ with no earlier error should be a no-op, got %d", m.selected)
	}
}

func TestSessionViewFitsWhileSearching(t *testing.T) {
	m := searchEvsModel(t, nil)
	for i := 0; i < 40; i++ {
		m.appendEvent(&v1.Event{
			Seq: int64(i), Type: "model_turn", Actor: "coordinator",
			DataJson: fmt.Sprintf(`{"text":"long output line number %d that wraps inside the body region"}`, i),
		})
	}
	m.rebuild()
	m = press(m, "/")
	m = typeSearch(m, "line")
	view := m.sessionView()
	lines := strings.Split(view, "\n")
	if len(lines) != 24 {
		t.Fatalf("searching sessionView produced %d lines, want 24", len(lines))
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w > 80 {
			t.Fatalf("line %d width %d exceeds terminal width 80: %q", i, w, ln)
		}
	}
}

func TestYankText(t *testing.T) {
	m := model{w: 100, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}

	commit := &v1.Event{Seq: 1, Type: "commit_made", Actor: "coordinator", DataJson: `{"sha":"abc123def","message":"do the thing"}`}
	if got := m.yankText(commit); got != "abc123def" {
		t.Fatalf("yankText(commit_made) = %q, want %q", got, "abc123def")
	}

	errEv := &v1.Event{Seq: 2, Type: "session_error", Actor: "coordinator", DataJson: `{"msg":"boom failure"}`}
	if got := m.yankText(errEv); got != "boom failure" {
		t.Fatalf("yankText(session_error) = %q, want %q", got, "boom failure")
	}

	turn := &v1.Event{Seq: 3, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"the model answer"}`}
	if got := m.yankText(turn); got != "the model answer" {
		t.Fatalf("yankText(model_turn) = %q, want %q", got, "the model answer")
	}
}

// TestYankKeyCopiesRow verifies that `y` on a selected commit row with empty
// input arms the "copied ✓" notice, while `y` mid-composition types into the
// textarea instead (task 0141). It also confirms the notice self-clears on the
// matching flashClearMsg tick.
func TestYankKeyCopiesRow(t *testing.T) {
	m := model{
		client: newFakeClient(), ctx: context.Background(),
		state: stateSession, status: "running", connected: true, sessionID: "s_live", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		thinkLevels: map[string]string{},
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.input.Focus()
	m.appendEvent(&v1.Event{Seq: 1, Type: "commit_made", Actor: "coordinator", DataJson: `{"sha":"abc123def","message":"do the thing"}`})
	m.rebuild()
	m.selected = 0

	// Empty input: `y` yanks and arms the notice (no text typed into the input).
	m = drive(t, m, "y")
	if m.flashNote != "copied ✓" {
		t.Fatalf("y on empty input did not set flashNote: %q", m.flashNote)
	}
	if m.input.Value() != "" {
		t.Fatalf("y on empty input typed into the textarea: %q", m.input.Value())
	}

	// The clear tick dismisses the notice.
	updated, _ = m.Update(flashClearMsg{seq: m.flashSeq})
	m = updated.(model)
	if m.flashNote != "" {
		t.Fatalf("flashNote did not clear on the matching tick: %q", m.flashNote)
	}

	// With content in the input, `y` types normally instead of yanking.
	m = typeText(t, m, "abc")
	m.flashNote = ""
	updated, _ = m.Update(keyMsg("y"))
	m = updated.(model)
	if m.flashNote != "" {
		t.Fatalf("y mid-composition armed a flash: %q", m.flashNote)
	}
	if m.input.Value() != "abcy" {
		t.Fatalf("y mid-composition did not type into the textarea: %q", m.input.Value())
	}
}
