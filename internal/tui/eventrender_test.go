package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/whyrusleeping/ycc/internal/clientconfig"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestRenderSessionNoticeIsVisible(t *testing.T) {
	m := model{w: 80}
	body := m.renderBody(&v1.Event{Type: "session_notice", DataJson: `{"msg":"preset fallback warning"}`})
	if !strings.Contains(body, "preset fallback warning") {
		t.Fatalf("session notice body = %q", body)
	}
}

// Keep one representative header test: actor runs are a stateful rendering
// contract, unlike the particular glyph or color selected for each event type.
func TestActorRunDedupAndFraming(t *testing.T) {
	m := model{w: 100, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	m.evs = []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"first words"}`},
		{Seq: 2, Type: "thinking", Actor: "coordinator", DataJson: `{"text":"pondering"}`},
		{Seq: 3, Type: "model_turn", Actor: "implementer", DataJson: `{"text":"now me"}`},
	}

	first := m.renderBlock(0, m.evs[0])
	if !strings.Contains(first, "coordinator") || !strings.Contains(first, "first words") {
		t.Fatalf("first actor row lost its actor or content:\n%s", first)
	}
	cont := m.renderBlock(1, m.evs[1])
	if strings.Contains(cont, "coordinator") {
		t.Fatalf("continuation row repeated the actor name:\n%s", cont)
	}
	if switched := m.renderBlock(2, m.evs[2]); !strings.Contains(switched, "implementer") {
		t.Fatalf("actor switch did not identify the new actor:\n%s", switched)
	}
}

func TestThinkingRendering(t *testing.T) {
	ev := &v1.Event{Type: "thinking", DataJson: `{"text":"first I will read the file","blocks":1,"reasoning_tokens":384}`}
	m := &model{w: 80}
	if body := m.renderBody(ev); !strings.Contains(body, "read the file") {
		t.Fatalf("reasoning content missing from body: %q", body)
	}
	empty := &v1.Event{Type: "thinking", DataJson: `{"text":""}`}
	if body := m.renderBody(empty); strings.TrimSpace(body) != "" {
		t.Fatalf("empty thinking body = %q", body)
	}
}

func TestIdleReportRenderedInFull(t *testing.T) {
	m := &model{w: 80, bodyCache: map[int]string{}}
	idle := &v1.Event{Type: "session_idle", DataJson: `{"report":"All done.\n\nAssumptions:\n- used port 8080"}`}
	body := m.renderBody(idle)
	for _, want := range []string{"All done", "Assumptions", "port 8080"} {
		if !strings.Contains(body, want) {
			t.Fatalf("finish report dropped %q: %q", want, body)
		}
	}
}

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

	different := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"Completed task 0042."}`}
	m = mk(turn, different)
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

// Regression coverage for backend errors running past the terminal edge.
func TestRenderBodySessionErrorWraps(t *testing.T) {
	long := "invalid_request_error: " + strings.Repeat("abcdefghij0123456789", 12) + " end"
	ev := &v1.Event{Seq: 1, Type: "session_error", DataJson: `{"msg":` + jsonQuote(long) + `}`}
	body := (&model{w: 40}).renderBody(ev)
	if body == "" {
		t.Fatal("renderBody returned empty for session_error")
	}
	for _, line := range strings.Split(body, "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Fatalf("error line width %d exceeds terminal width 40: %q", w, line)
		}
	}
	if !strings.Contains(stripANSI(body), "end") {
		t.Fatalf("wrapped error dropped trailing content: %q", body)
	}
}
