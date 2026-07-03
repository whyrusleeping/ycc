package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func readyStreamModel(t *testing.T) model {
	t.Helper()
	m := model{
		state: stateSession, status: "running", mode: "implement",
		sessionID: "sess", follow: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		liveTails: map[string]string{},
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(model)
}

// A transient turn_delta drives a live "streaming…" tail row; a later snapshot
// replaces it in place; the persisted model_turn removes it, leaving the final
// text rendered once with no stale tail. The transient never enters m.evs.
func TestLiveTailStreamThenSwap(t *testing.T) {
	m := readyStreamModel(t)
	m.appendEvent(&v1.Event{Seq: 1, Type: "user_input", Actor: "user", DataJson: `{"text":"go"}`})
	m.rebuild()

	// First snapshot.
	if !m.applyTransient(&v1.Event{Type: "turn_delta", Actor: "coordinator", Transient: true, DataJson: `{"text":"partial answer"}`}) {
		t.Fatal("applyTransient reported no change on first snapshot")
	}
	if len(m.evs) != 1 {
		t.Fatalf("transient leaked into m.evs: len=%d", len(m.evs))
	}
	m.rebuild()
	view := m.vp.View()
	if !strings.Contains(view, "partial answer") {
		t.Fatalf("view missing streamed text:\n%s", view)
	}
	if !strings.Contains(view, "streaming…") {
		t.Fatalf("view missing in-progress marker:\n%s", view)
	}

	// Newer snapshot (full accumulated text) replaces the tail.
	m.applyTransient(&v1.Event{Type: "turn_delta", Actor: "coordinator", Transient: true, DataJson: `{"text":"partial answer complete"}`})
	m.rebuild()
	view = m.vp.View()
	if !strings.Contains(view, "partial answer complete") {
		t.Fatalf("view missing replaced snapshot:\n%s", view)
	}
	if strings.Contains(view, "partial answer\n") {
		t.Fatalf("stale prior snapshot still shown:\n%s", view)
	}

	// The persisted model_turn arrives: the tail is dropped, final text shows once.
	m.appendEvent(&v1.Event{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"partial answer complete"}`})
	if len(m.liveTails) != 0 {
		t.Fatalf("live tail not cleared by model_turn: %v", m.liveTails)
	}
	m.rebuild()
	view = m.vp.View()
	if strings.Contains(view, "streaming…") {
		t.Fatalf("stale streaming tail after model_turn:\n%s", view)
	}
	if strings.Count(view, "partial answer complete") != 1 {
		t.Fatalf("final text should render exactly once:\n%s", view)
	}
}

// A done/empty delta clears the live tail with no persisted event.
func TestLiveTailDoneDeltaClears(t *testing.T) {
	m := readyStreamModel(t)
	m.applyTransient(&v1.Event{Type: "turn_delta", Actor: "coordinator", Transient: true, DataJson: `{"text":"typing"}`})
	if len(m.liveTails) != 1 {
		t.Fatalf("tail not set: %v", m.liveTails)
	}
	changed := m.applyTransient(&v1.Event{Type: "turn_delta", Actor: "coordinator", Transient: true, DataJson: `{"text":"","done":true}`})
	if !changed || len(m.liveTails) != 0 {
		t.Fatalf("done delta did not clear tail: changed=%v tails=%v", changed, m.liveTails)
	}
	m.rebuild()
	if strings.Contains(m.vp.View(), "streaming…") {
		t.Fatalf("tail still rendered after done delta:\n%s", m.vp.View())
	}
}

// The evMsg Update path routes a transient turn_delta into live tail state and
// NEVER into m.evs (no reducer/replay/seq involvement).
func TestEvMsgTransientRouting(t *testing.T) {
	m := readyStreamModel(t)
	updated, _ := m.Update(evMsg{ev: &v1.Event{Type: "turn_delta", Actor: "implementer", Transient: true, DataJson: `{"text":"stream via update"}`}})
	m = updated.(model)
	if len(m.evs) != 0 {
		t.Fatalf("transient entered m.evs via Update: %d", len(m.evs))
	}
	if m.liveTails["implementer"] != "stream via update" {
		t.Fatalf("transient not routed to live tail: %v", m.liveTails)
	}
	if !strings.Contains(m.vp.View(), "stream via update") {
		t.Fatalf("streamed text not rendered:\n%s", m.vp.View())
	}
}
