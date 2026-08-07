package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/whyrusleeping/ycc/internal/clientconfig"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// The actor name is spelled out only when an actor first starts a run of rows;
// continuation rows by the same actor show its compact glyph instead. A
// model_turn is rendered as framing prose, dropping the redundant type label.
func TestActorRunDedupAndFraming(t *testing.T) {
	m := model{w: 100, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	m.evs = []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"first words"}`},
		{Seq: 2, Type: "thinking", Actor: "coordinator", DataJson: `{"text":"pondering"}`},
		{Seq: 3, Type: "model_turn", Actor: "implementer", DataJson: `{"text":"now me"}`},
	}

	// First coordinator row spells out the name; model_turn omits the type label.
	first := m.renderBlock(0, m.evs[0])
	if !strings.Contains(first, "coordinator") {
		t.Fatalf("first-of-run row should spell out actor name:\n%s", first)
	}
	if strings.Contains(first, "model_turn") {
		t.Fatalf("model_turn row should drop the redundant type label:\n%s", first)
	}
	if !strings.Contains(first, "first words") {
		t.Fatalf("model_turn row should show its prose:\n%s", first)
	}

	// Second coordinator row (continuation) shows the glyph, not the name.
	cont := m.renderBlock(1, m.evs[1])
	if strings.Contains(cont, "coordinator") {
		t.Fatalf("continuation row should not repeat the actor name:\n%s", cont)
	}
	if !strings.Contains(cont, actorGlyph("coordinator")) {
		t.Fatalf("continuation row should show the actor glyph:\n%s", cont)
	}

	// Actor switch spells out the new actor again.
	switched := m.renderBlock(2, m.evs[2])
	if !strings.Contains(switched, "implementer") {
		t.Fatalf("actor switch should spell out the new actor:\n%s", switched)
	}
}

// Each event type renders its consistent leading glyph, and continuation rows
// still carry the actor glyph (column alignment preserved).
func TestTypeGlyphsInHeader(t *testing.T) {
	m := &model{w: 120, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	m.evs = []*v1.Event{
		{Seq: 1, Type: "thinking", Actor: "coordinator", DataJson: `{"text":"pondering"}`},
		{Seq: 2, Type: "user_input", Actor: "user", DataJson: `{"text":"go"}`},
		{Seq: 3, Type: "review_submitted", Actor: "reviewer-1", DataJson: `{"model":"claude","verdict":"accept","summary":"lgtm"}`},
	}
	cases := []struct {
		i    int
		typ  string
		want string
	}{
		{0, "thinking", typeGlyph("thinking")},
		{1, "user_input", typeGlyph("user_input")},
		{2, "review_submitted", typeGlyph("review_submitted")},
	}
	for _, c := range cases {
		out := m.renderBlock(c.i, m.evs[c.i])
		if !strings.Contains(out, c.want) {
			t.Fatalf("%s row should contain glyph %q:\n%s", c.typ, c.want, out)
		}
	}
}

// detailLine color-codes review verdicts: accept=success, revise=warn, reject=danger.
func TestVerdictColorsInDetailLine(t *testing.T) {
	// lipgloss v2 styles always emit ANSI (the program output layer handles
	// profile downsampling), so no color-profile setup is needed here.
	for _, v := range []string{"accept", "revise", "reject"} {
		ev := &v1.Event{Seq: 1, Type: "review_submitted", Actor: "reviewer-1",
			DataJson: fmt.Sprintf(`{"model":"claude","verdict":%q,"summary":"sum"}`, v)}
		d := detailLine(ev)
		styled := verdictStyle(v).Render(v)
		if !strings.Contains(d, styled) {
			t.Fatalf("verdict %q should be styled via verdictStyle in detailLine:\ngot  %q\nwant substring %q", v, d, styled)
		}
		// The styled token must differ from the bare token (i.e. ANSI was applied).
		if styled == v {
			t.Fatalf("verdictStyle(%q) produced no styling: %q", v, styled)
		}
	}
}

// A contiguous sub-agent run renders ├─ on its non-last rows and └─ on the last,
// nesting the sub-agents under the coordinator.
func TestSubAgentTreeConnectors(t *testing.T) {
	m := &model{w: 120, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	m.evs = []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"dispatching"}`},
		{Seq: 2, Type: "model_turn", Actor: "implementer", DataJson: `{"text":"working a"}`},
		{Seq: 3, Type: "model_turn", Actor: "implementer", DataJson: `{"text":"working b"}`},
		{Seq: 4, Type: "model_turn", Actor: "reviewer-1", DataJson: `{"text":"reviewing"}`},
	}
	// Coordinator row: no connector.
	if c := m.renderBlock(0, m.evs[0]); strings.Contains(c, "├─") || strings.Contains(c, "└─") {
		t.Fatalf("coordinator row should not have a sub-agent connector:\n%s", c)
	}
	// Non-last sub-agent rows use ├─.
	for _, i := range []int{1, 2} {
		out := m.renderBlock(i, m.evs[i])
		if !strings.Contains(out, "├─") {
			t.Fatalf("non-last sub-agent row %d should use ├─:\n%s", i, out)
		}
	}
	// Last sub-agent row of the run uses └─.
	last := m.renderBlock(3, m.evs[3])
	if !strings.Contains(last, "└─") {
		t.Fatalf("last sub-agent row should use └─:\n%s", last)
	}
}

// A thinking event renders a one-line "(reasoning)" detail and an expandable
// body carrying the reasoning summary.
func TestThinkingRendering(t *testing.T) {
	ev := &v1.Event{Type: "thinking", DataJson: `{"text":"first I will read the file","blocks":1,"reasoning_tokens":384}`}
	if d := detailLine(ev); !strings.Contains(d, "reasoning summary") || !strings.Contains(d, "384 hidden tokens") || !strings.Contains(d, "read the file") {
		t.Fatalf("detailLine = %q", d)
	}
	m := &model{w: 80}
	body := m.renderBody(ev)
	if !strings.Contains(body, "read the file") {
		t.Fatalf("renderBody = %q", body)
	}
	if d := expandedDetailLine(ev); !strings.Contains(d, "384 hidden reasoning tokens") {
		t.Fatalf("expandedDetailLine = %q", d)
	}
	// An empty reasoning summary produces no body (nothing to expand).
	empty := &v1.Event{Type: "thinking", DataJson: `{"text":""}`}
	if b := m.renderBody(empty); strings.TrimSpace(b) != "" {
		t.Fatalf("empty thinking body = %q", b)
	}
}

// When a prose row is expanded, the header drops its one-line snippet (the full
// text is in the body box) but keeps non-body metadata like a model_turn's
// elapsed time; collapsed rows still show the snippet preview.
func TestExpandedHeaderDropsSnippet(t *testing.T) {
	turn := &v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"here is my long final answer about things","duration_ms":1200}`}
	m := &model{w: 120, bodyCache: map[int]string{}}

	// Collapsed: snippet present.
	collapsed := m.renderHeader(0, turn, false, false, true, true)
	if !strings.Contains(collapsed, "long final answer") {
		t.Fatalf("collapsed header should show the snippet, got %q", collapsed)
	}

	// Expanded: snippet gone, duration kept.
	expanded := m.renderHeader(0, turn, false, true, true, true)
	if strings.Contains(expanded, "long final answer") {
		t.Fatalf("expanded header should drop the redundant snippet, got %q", expanded)
	}
	if !strings.Contains(expanded, "1.2s") {
		t.Fatalf("expanded model_turn header should keep its elapsed time, got %q", expanded)
	}

	// A user_input row drops its snippet entirely when expanded.
	in := &v1.Event{Seq: 2, Type: "user_input", Actor: "user", DataJson: `{"text":"please refactor the parser module"}`}
	if h := m.renderHeader(1, in, false, true, true, true); strings.Contains(h, "refactor the parser") {
		t.Fatalf("expanded user_input header should drop the snippet, got %q", h)
	}
}

// A session_idle report is the canonical human-facing finish message and renders
// in full, whether it echoes the final turn, adds details, or differs entirely.
func TestIdleReportRenderedInFull(t *testing.T) {
	mk := func(evs ...*v1.Event) *model {
		m := &model{w: 80, bodyCache: map[int]string{}}
		m.evs = evs
		return m
	}
	turn := &v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"All done — shipped the feature and tests pass."}`}

	// Exact echo: the finish report remains the canonical, visible body.
	idle := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"All done — shipped the feature and tests pass."}`}
	m := mk(turn, idle)
	if b := m.renderBody(idle); !strings.Contains(b, "shipped the feature") {
		t.Fatalf("echoing finish report should render in full, got %q", b)
	}

	// Echo + appended assumptions: the full report remains.
	idle2 := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"All done — shipped the feature and tests pass.\n\nAssumptions made without consulting the user (unattended execution):\n- used port 8080\n"}`}
	m = mk(turn, idle2)
	b := m.renderBody(idle2)
	if !strings.Contains(b, "shipped the feature") || !strings.Contains(b, "Assumptions") || !strings.Contains(b, "port 8080") {
		t.Fatalf("finish body should keep the complete report, got %q", b)
	}

	// Different control-tool report: rendered in full.
	idle3 := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"Completed task 0042 and committed the change."}`}
	m = mk(turn, idle3)
	if b := m.renderBody(idle3); !strings.Contains(b, "task 0042") {
		t.Fatalf("a differing report should render in full, got %q", b)
	}
}

// A final model_turn echoed by session_idle is folded into the canonical finish
// report. Additive reports coalesce too; genuinely different rows both remain.
func TestFinishReportCoalescesPrecedingTurn(t *testing.T) {
	mk := func(evs ...*v1.Event) *model {
		m := &model{w: 80, bodyCache: map[int]string{}}
		m.evs = evs
		return m
	}
	turn := &v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"All green. Shipped it."}`}

	echo := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"All green. Shipped it."}`}
	m := mk(turn, echo)
	if !m.hiddenRow(0) || m.hiddenRow(1) {
		t.Fatal("echoed final turn should fold into a visible finish report")
	}

	added := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"All green. Shipped it.\n\nAssumptions:\n- used port 8080"}`}
	m = mk(turn, added)
	if !m.hiddenRow(0) || m.hiddenRow(1) {
		t.Fatal("a final-turn prefix should fold into the visible additive finish report")
	}

	diff := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"Completed task 0042."}`}
	m = mk(turn, diff)
	if m.hiddenRow(0) || m.hiddenRow(1) {
		t.Fatal("a differing finish report should preserve both rows")
	}
}

func TestFinishReportAlwaysExpandedAndCannotCollapse(t *testing.T) {
	m := &model{
		w: 80, ready: true, prefs: clientconfig.Prefs{AutoExpandLogs: false},
		expanded: map[int]bool{2: false}, bodyCache: map[int]string{},
		blockCache: map[int]string{}, hiddenCache: map[int]bool{},
	}
	idle := &v1.Event{Seq: 2, Type: "session_idle", Actor: "coordinator", DataJson: `{"report":"## Finished\n\n- shipped"}`}
	m.evs = []*v1.Event{idle}
	if !m.eventExpanded(2, "session_idle") {
		t.Fatal("finish report must ignore auto-expand=false and manual collapse overrides")
	}
	m.toggle(0)
	if !m.eventExpanded(2, "session_idle") {
		t.Fatal("finish report must remain expanded after toggle")
	}
}

// TestRenderBodySessionErrorWraps verifies a long single-line session_error
// message (e.g. a backend 400 invalid_request_error with a JSON body) is wrapped
// to the body width instead of running off the right edge. Regression for the
// truncated/unwrapped error display.
func TestRenderBodySessionErrorWraps(t *testing.T) {
	long := "invalid_request_error: " + strings.Repeat("abcdefghij0123456789", 12) + " end"
	ev := &v1.Event{Seq: 1, Type: "session_error", DataJson: `{"msg":` + jsonQuote(long) + `}`}

	m := &model{w: 40}
	body := m.renderBody(ev)
	if body == "" {
		t.Fatal("renderBody returned empty for session_error")
	}
	for _, line := range strings.Split(body, "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Fatalf("error line width %d exceeds terminal width 40: %q", w, line)
		}
	}
	// The full message must survive wrapping (no content dropped): stripping the
	// indent/bar prefix and joining should recover the original characters.
	if !strings.Contains(stripANSI(body), "end") {
		t.Fatalf("wrapped error dropped trailing content: %q", body)
	}
}
