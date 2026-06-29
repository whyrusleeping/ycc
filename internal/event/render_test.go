package event

import (
	"strings"
	"testing"
)

// Render surfaces a task_focus event's task id so the focus is visible in the
// terse human-facing stream.
func TestRenderTaskFocus(t *testing.T) {
	out := Render(Event{Seq: 2, Actor: "coordinator", Type: TaskFocus, Data: map[string]any{"task": "0007"}})
	if !strings.Contains(out, "0007") {
		t.Fatalf("Render = %q, want it to show the focused task id", out)
	}
}

// Render appends a compact elapsed-duration suffix for model_turn and
// tool_result events that carry duration_ms (task 0055), accepting both a
// freshly-emitted int64 and a JSONL-decoded float64, and omitting it when zero.
func TestRenderDuration(t *testing.T) {
	cases := []struct {
		name string
		typ  Type
		dur  any
		want string
	}{
		{"turn-ms", ModelTurn, int64(340), "340ms"},
		{"turn-s", ModelTurn, int64(1234), "1.2s"},
		{"turn-float", ModelTurn, float64(500), "500ms"},
		{"tool-ms", ToolResult, int64(42), "42ms"},
		{"tool-float-s", ToolResult, float64(2500), "2.5s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := map[string]any{"text": "hi", "result": "ok", "duration_ms": c.dur}
			out := Render(Event{Seq: 1, Actor: "agent", Type: c.typ, Data: data})
			if !strings.Contains(out, c.want) {
				t.Fatalf("Render = %q, want it to contain %q", out, c.want)
			}
		})
	}

	// Absent/zero duration adds no suffix.
	for _, typ := range []Type{ModelTurn, ToolResult} {
		out := Render(Event{Seq: 1, Actor: "agent", Type: typ, Data: map[string]any{"text": "hi", "result": "ok"}})
		if strings.Contains(out, "ms") || strings.Contains(out, "s)") {
			t.Fatalf("Render = %q, want no duration suffix when absent", out)
		}
		out = Render(Event{Seq: 1, Actor: "agent", Type: typ, Data: map[string]any{"text": "hi", "result": "ok", "duration_ms": int64(0)}})
		if strings.Contains(out, "0ms") {
			t.Fatalf("Render = %q, want no duration suffix for zero", out)
		}
	}
}

// Render shows a terse token count for model_turn events that carry usage,
// accepting both a freshly-emitted Usage value and a JSONL-decoded map.
func TestRenderModelTurnTokens(t *testing.T) {
	cases := []struct {
		name  string
		usage any
		want  bool
	}{
		{"struct", Usage{Total: 1234}, true},
		{"map", map[string]any{"total": float64(1234)}, true},
		{"zero", Usage{}, false},
		{"absent", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := map[string]any{"text": "hi"}
			if c.usage != nil {
				data["usage"] = c.usage
			}
			out := Render(Event{Seq: 1, Actor: "agent", Type: ModelTurn, Data: data})
			has := strings.Contains(out, "(1234 tok)")
			if has != c.want {
				t.Fatalf("Render = %q, want token-count presence %v", out, c.want)
			}
		})
	}
}
