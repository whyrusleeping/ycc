package event

import (
	"strings"
	"testing"
)

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
