package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

// A tool_call immediately followed by its matching tool_result must fold into a
// single combined chat-log row (task 0043). Spawn-style tools (whose subagent
// events appear between call and result) and id-mismatched pairs must not fold.
func TestMergedResultPairing(t *testing.T) {
	m := &model{evs: []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator"},
		{Seq: 2, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read"}`},
		{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"ok"}`},
		{Seq: 4, Type: "model_turn", Actor: "coordinator"},
	}}
	if got := m.mergedResultIdx(1); got != 2 {
		t.Fatalf("mergedResultIdx(1) = %d, want 2", got)
	}
	if !m.isMergedResult(2) {
		t.Fatal("isMergedResult(2) = false, want true")
	}
	if m.isMergedResult(1) {
		t.Fatal("isMergedResult(1) = true, want false (call is not a result)")
	}

	// Spawn-style: a non-adjacent result (subagent event in between) must not fold.
	spawn := &model{evs: []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"s1","name":"spawn_implementer"}`},
		{Seq: 2, Type: "subagent_spawned", Actor: "coordinator"},
		{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"s1","result":"done"}`},
	}}
	if got := spawn.mergedResultIdx(0); got != -1 {
		t.Fatalf("spawn mergedResultIdx(0) = %d, want -1", got)
	}

	// Id mismatch (adjacent but different ids) must not fold.
	mismatch := &model{evs: []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"a","name":"Read"}`},
		{Seq: 2, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"b","result":"ok"}`},
	}}
	if got := mismatch.mergedResultIdx(0); got != -1 {
		t.Fatalf("mismatch mergedResultIdx(0) = %d, want -1", got)
	}
}

// After rebuild, a folded tool_result shares its call's start line and emits no
// block of its own, and clicks anywhere in the combined region resolve to the
// call (task 0043).
func TestRebuildCombinesRow(t *testing.T) {
	m := model{
		state: stateSession, status: "running", follow: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.appendEvent(&v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"hi"}`})
	m.appendEvent(&v1.Event{Seq: 2, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read","args":"{\"file_path\":\"x.go\"}"}`})
	m.appendEvent(&v1.Event{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"contents"}`})
	m.appendEvent(&v1.Event{Seq: 4, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"bye"}`})
	m.rebuild() // appendEvent no longer rebuilds; caller batches + rebuilds once

	if m.eventStart[2] != m.eventStart[1] {
		t.Fatalf("folded result start %d != call start %d", m.eventStart[2], m.eventStart[1])
	}
	// The result block must not advance the line counter past the call.
	if m.eventStart[3] <= m.eventStart[1] {
		t.Fatalf("trailing event start %d should be after combined row %d", m.eventStart[3], m.eventStart[1])
	}
	// A click in the combined region resolves to the call (index 1).
	if got := m.eventAt(m.eventStart[1]); got != 1 {
		t.Fatalf("eventAt(call start) = %d, want 1", got)
	}
}

// An empty model_turn (an agent turn carrying only tool calls, no text) is
// hidden: it renders no row of its own and shares the previous rendered row's
// start line, so the chat no longer shows a bare line with just a duration. The
// following tool call still resolves and selection skips the hidden turn.
func TestRebuildHidesEmptyModelTurn(t *testing.T) {
	m := model{
		state: stateSession, status: "running", follow: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.appendEvent(&v1.Event{Seq: 1, Type: "user_input", Actor: "user", DataJson: `{"text":"go"}`})
	// Empty agent turn: no text, just timing — the noise we want gone.
	m.appendEvent(&v1.Event{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"","duration_ms":340}`})
	m.appendEvent(&v1.Event{Seq: 3, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read","args":"{\"file_path\":\"x.go\"}"}`})
	m.appendEvent(&v1.Event{Seq: 4, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"ok"}`})
	m.rebuild() // appendEvent no longer rebuilds; caller batches + rebuilds once

	if !m.isEmptyModelTurn(1) {
		t.Fatal("isEmptyModelTurn(1) = false, want true for a text-less model_turn")
	}
	if m.isEmptyModelTurn(0) {
		t.Fatal("isEmptyModelTurn(0) = true, want false for a user_input")
	}
	// The hidden turn shares the previous row's start line and emits no block.
	if m.eventStart[1] != m.eventStart[0] {
		t.Fatalf("empty model_turn start %d != previous row start %d", m.eventStart[1], m.eventStart[0])
	}
	// The following tool call advances past the shared row.
	if m.eventStart[2] <= m.eventStart[0] {
		t.Fatalf("tool_call start %d should be after the user_input row %d", m.eventStart[2], m.eventStart[0])
	}
	// A click on the hidden turn's line resolves to the previous visible row.
	if got := m.eventAt(m.eventStart[1]); got != 0 {
		t.Fatalf("eventAt(empty turn line) = %d, want 0", got)
	}
	// Selecting downward from the user_input skips the hidden turn onto the call.
	m.selected = 0
	m.moveSelection(1)
	if m.selected != 2 {
		t.Fatalf("moveSelection(1) landed on %d, want 2 (skip the empty turn)", m.selected)
	}
}

// Expanding a combined tool_call+tool_result row reveals both the full params and the full response with no information lost (task 0043).
func TestRenderCombinedExpanded(t *testing.T) {
	m := model{w: 100, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	m.evs = []*v1.Event{
		{Seq: 2, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read","args":"{\"file_path\":\"hello.go\"}"}`},
		{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"RESULTBODY"}`},
	}
	m.expanded[2] = true
	out := m.renderBlock(0, m.evs[0])
	if !strings.Contains(out, "hello.go") {
		t.Fatalf("expanded combined row missing args content:\n%s", out)
	}
	if !strings.Contains(out, "RESULTBODY") {
		t.Fatalf("expanded combined row missing result content:\n%s", out)
	}
	if !strings.Contains(out, "Response") {
		t.Fatalf("expanded combined row missing Response box:\n%s", out)
	}
	// Collapsed: still shows the tool name + a status marker.
	m.expanded[2] = false
	m.bodyCache = map[int]string{}
	col := m.renderBlock(0, m.evs[0])
	if !strings.Contains(col, "Read") || !strings.Contains(col, "✓") {
		t.Fatalf("collapsed combined row missing name/status:\n%s", col)
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

// The status header must not latch on "idle" either: prodding a finished
// session emits a user_input echo as soon as the daemon accepts it, but the
// first model event can lag far behind (long context + thinking). The echo —
// and any later activity — must flip the status back to running so the spinner
// arms and the footer stops claiming "session finished" while the agent is
// actually working on the follow-up.
func TestAppendEventClearsLatchedIdle(t *testing.T) {
	for _, activity := range []string{"user_input", "thinking", "model_turn"} {
		m := &model{w: 80, follow: true, expanded: map[int]bool{}, bodyCache: map[int]string{}}
		m.appendEvent(&v1.Event{Type: "session_idle", DataJson: `{"report":"done"}`})
		if m.status != "idle" {
			t.Fatalf("after session_idle status = %q, want idle", m.status)
		}
		m.appendEvent(&v1.Event{Type: activity, DataJson: `{"text":"follow-up"}`})
		if m.status != "running" {
			t.Fatalf("after %s status = %q, want running", activity, m.status)
		}
	}
}

// A transient (broadcast-only) event — Seq=0, Transient=true, e.g. turn_delta —
// must be ignored by the event loop: it never enters m.evs, never advances seq
// tracking, and never corrupts reducer-fed state. There is no rendering yet
// (task 0114); the TUI just tolerates the seq-less event safely.
func TestEvMsgIgnoresTransientEvents(t *testing.T) {
	m := newSessionTextareaModel(t)
	m.sessionID = "s1"
	m.client = newFakeClient()
	m.ctx = context.Background()
	m.events = make(chan *v1.Event, 4)

	// A normal persisted event is recorded.
	real := &v1.Event{Seq: 1, Type: "model_turn", DataJson: `{"text":"hi"}`}
	nm, _ := m.Update(evMsg{real})
	m = nm.(model)
	if len(m.evs) != 1 {
		t.Fatalf("after persisted event len(evs) = %d, want 1", len(m.evs))
	}

	// A transient event must be dropped: len(evs) stays 1.
	trans := &v1.Event{Seq: 0, Type: "turn_delta", Transient: true, DataJson: `{"text":"par"}`}
	nm, _ = m.Update(evMsg{trans})
	m = nm.(model)
	if len(m.evs) != 1 {
		t.Fatalf("transient event was not ignored: len(evs) = %d, want 1", len(m.evs))
	}
	if m.evs[0].Seq != 1 || m.evs[0].Type != "model_turn" {
		t.Fatalf("transient event corrupted event list: %+v", m.evs[0])
	}

	// Transient events queued on the stream (batched drain path) are also skipped.
	m.events <- &v1.Event{Seq: 0, Type: "turn_delta", Transient: true}
	m.events <- &v1.Event{Seq: 2, Type: "tool_call", DataJson: `{"name":"x"}`}
	nm, _ = m.Update(evMsg{&v1.Event{Seq: 0, Type: "turn_delta", Transient: true}})
	m = nm.(model)
	if len(m.evs) != 2 {
		t.Fatalf("batched drain mishandled transients: len(evs) = %d, want 2", len(m.evs))
	}
	if m.evs[1].Seq != 2 || m.evs[1].Type != "tool_call" {
		t.Fatalf("persisted event lost/corrupted in batched drain: %+v", m.evs[1])
	}
}
