package event

import "testing"

// A log ending with a session_stopped event reduces to StatusStopped (spec §12).
func TestReduceSessionStopped(t *testing.T) {
	events := []Event{
		{Seq: 1, Type: SessionStarted, Data: map[string]any{"mode": "work"}},
		{Seq: 2, Type: ModelTurn},
		{Seq: 3, Type: SessionStopped},
	}
	p := Reduce(events)
	if p.Status != StatusStopped {
		t.Fatalf("Status = %q, want %q", p.Status, StatusStopped)
	}
}

// TestReduceWorkstreamLifecycle verifies the parallel-workstream events fold into
// the projection's workstream fields (design §6, §8), including a JSONL-decoded
// []any conflicts payload, and that interaction_level_changed updates the level.
func TestReduceWorkstreamLifecycle(t *testing.T) {
	// created → conflict (fresh []string) → merged clears conflicts.
	p := Reduce([]Event{
		{Seq: 1, Type: WorkstreamCreated, Data: map[string]any{"workstream": "ws_abc", "branch": "ycc/ws/ws_abc"}},
		{Seq: 2, Type: WorkstreamConflict, Data: map[string]any{"conflicts": []string{"a.go", "b.go"}}},
	})
	if p.WorkstreamID != "ws_abc" {
		t.Fatalf("WorkstreamID = %q, want ws_abc", p.WorkstreamID)
	}
	if p.WorkstreamState != "conflict" {
		t.Fatalf("WorkstreamState = %q, want conflict", p.WorkstreamState)
	}
	if len(p.WorkstreamConflicts) != 2 || p.WorkstreamConflicts[0] != "a.go" {
		t.Fatalf("WorkstreamConflicts = %v", p.WorkstreamConflicts)
	}

	// A JSONL-decoded []any conflicts payload is accepted too.
	p = Reduce([]Event{
		{Seq: 1, Type: WorkstreamConflict, Data: map[string]any{"conflicts": []any{"x.go"}}},
	})
	if len(p.WorkstreamConflicts) != 1 || p.WorkstreamConflicts[0] != "x.go" {
		t.Fatalf("[]any conflicts = %v", p.WorkstreamConflicts)
	}

	// merged clears conflicts and sets state.
	p = Reduce([]Event{
		{Seq: 1, Type: WorkstreamConflict, Data: map[string]any{"conflicts": []any{"x.go"}}},
		{Seq: 2, Type: WorkstreamMerged, Data: map[string]any{"commit": "abc123"}},
	})
	if p.WorkstreamState != "merged" || len(p.WorkstreamConflicts) != 0 {
		t.Fatalf("after merged: state=%q conflicts=%v", p.WorkstreamState, p.WorkstreamConflicts)
	}

	// discarded sets state and clears conflicts.
	p = Reduce([]Event{
		{Seq: 1, Type: WorkstreamConflict, Data: map[string]any{"conflicts": []any{"x.go"}}},
		{Seq: 2, Type: WorkstreamDiscarded, Data: map[string]any{}},
	})
	if p.WorkstreamState != "discarded" || len(p.WorkstreamConflicts) != 0 {
		t.Fatalf("after discarded: state=%q conflicts=%v", p.WorkstreamState, p.WorkstreamConflicts)
	}
}

// TestReduceInteractionLevelChanged verifies a mid-session settings overlay
// updates the projection's interaction level (needed by the merge accept gate).
func TestReduceInteractionLevelChanged(t *testing.T) {
	p := Reduce([]Event{
		{Seq: 1, Type: SessionStarted, Data: map[string]any{"interaction_level": "judgement"}},
		{Seq: 2, Type: InteractionLevelChanged, Data: map[string]any{"from": "judgement", "to": "autonomous"}},
	})
	if p.InteractionLevel != "autonomous" {
		t.Fatalf("InteractionLevel = %q, want autonomous", p.InteractionLevel)
	}
}
