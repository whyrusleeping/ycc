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
// []any conflicts payload.
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

	// Needs-attention preserves its reason; ready clears it.
	p = Reduce([]Event{{Seq: 1, Type: WorkstreamNeedsAttention, Data: map[string]any{"reason": "no commits since base"}}})
	if p.WorkstreamState != "needs_attention" || p.WorkstreamAttentionReason != "no commits since base" {
		t.Fatalf("needs attention: state=%q reason=%q", p.WorkstreamState, p.WorkstreamAttentionReason)
	}
	p = Reduce([]Event{
		{Seq: 1, Type: WorkstreamNeedsAttention, Data: map[string]any{"reason": "blocked"}},
		{Seq: 2, Type: WorkstreamReady, Data: map[string]any{"commits": 1}},
	})
	if p.WorkstreamState != "ready" || p.WorkstreamAttentionReason != "" {
		t.Fatalf("ready: state=%q reason=%q", p.WorkstreamState, p.WorkstreamAttentionReason)
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

// The coordinator model folds from session_started (which records the model the
// session was started on, including a per-session override) and is updated by a
// later role_config_changed, so a resume replays on the right model.
func TestReduceCoordinatorModel(t *testing.T) {
	p := Reduce([]Event{
		{Seq: 1, Type: SessionStarted, Data: map[string]any{"mode": "chat", "coordinator": "gpt"}},
		{Seq: 2, Type: ModelTurn},
	})
	if p.Coordinator != "gpt" {
		t.Fatalf("Coordinator = %q, want gpt", p.Coordinator)
	}

	p = Reduce([]Event{
		{Seq: 1, Type: SessionStarted, Data: map[string]any{"mode": "chat", "coordinator": "gpt"}},
		{Seq: 2, Type: RoleConfigChanged, Data: map[string]any{"coordinator": "claude", "implementer": "claude"}},
	})
	if p.Coordinator != "claude" {
		t.Fatalf("Coordinator after role change = %q, want claude", p.Coordinator)
	}

	// Older logs without the field simply leave it empty (caller falls back to
	// the configured default).
	p = Reduce([]Event{{Seq: 1, Type: SessionStarted, Data: map[string]any{"mode": "work"}}})
	if p.Coordinator != "" {
		t.Fatalf("Coordinator = %q, want empty for a legacy log", p.Coordinator)
	}
}
