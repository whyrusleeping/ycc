package event

import (
	"testing"
	"time"
)

// NewFuncRecorder stamps each event with a monotonic seq starting at 1 and a
// timestamp, preserves actor/type/data, and invokes fn with the stamped event.
func TestFuncRecorder(t *testing.T) {
	var got []Event
	rec := NewFuncRecorder(func(ev Event) { got = append(got, ev) })

	e1 := rec.Record("capture", ModelTurn, map[string]any{"text": "thinking"})
	e2 := rec.Record("capture", ToolCall, map[string]any{"name": "create_task"})

	if e1.Seq != 1 || e2.Seq != 2 {
		t.Fatalf("seq = %d,%d; want 1,2", e1.Seq, e2.Seq)
	}
	if e1.TS.IsZero() || e2.TS.IsZero() {
		t.Fatalf("expected non-zero timestamps, got %v,%v", e1.TS, e2.TS)
	}
	if time.Since(e1.TS) > time.Minute {
		t.Fatalf("timestamp not recent: %v", e1.TS)
	}
	if e1.Actor != "capture" || e1.Type != ModelTurn || e1.Data["text"] != "thinking" {
		t.Fatalf("e1 = %+v", e1)
	}
	if e2.Actor != "capture" || e2.Type != ToolCall || e2.Data["name"] != "create_task" {
		t.Fatalf("e2 = %+v", e2)
	}

	if len(got) != 2 {
		t.Fatalf("fn invoked %d times, want 2", len(got))
	}
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("fn received seqs %d,%d; want 1,2", got[0].Seq, got[1].Seq)
	}
}

// A nil fn must be tolerated: Record still stamps and returns the event.
func TestFuncRecorderNilFn(t *testing.T) {
	rec := NewFuncRecorder(nil)
	ev := rec.Record("capture", ToolResult, nil)
	if ev.Seq != 1 || ev.Actor != "capture" || ev.Type != ToolResult {
		t.Fatalf("ev = %+v", ev)
	}
}
