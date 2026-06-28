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
