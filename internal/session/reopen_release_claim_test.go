package session

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

// Chat and work sessions whose history calls the removed release_claim tool,
// with the claim_released record it wrote, reopen and replay the call, and the
// reopened session does not offer the tool.
func TestReopenReplaysRemovedReleaseClaim(t *testing.T) {
	for _, mode := range []string{"chat", "work"} {
		t.Run(mode, func(t *testing.T) {
			ws := t.TempDir()
			if _, err := git.Open(ws); err != nil {
				t.Fatal(err)
			}
			absWS, _ := filepath.Abs(ws)
			id := "s_release_claim_" + mode
			events := []event.Event{
				{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": mode, "workspace": absWS}},
				{Seq: 2, TS: ts(2), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "commit my work"}},
				{Seq: 3, TS: ts(3), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "releasing"}},
				{Seq: 4, TS: ts(4), Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{
					"name": "release_claim", "args": `{"paths":["gen.go"],"reason":"another session's output"}`, "id": "call_release",
				}},
				{Seq: 5, TS: ts(5), Actor: "coordinator", Type: event.ClaimReleased, Data: map[string]any{
					"paths": []any{"gen.go"}, "held": []any{"gen.go"}, "reason": "another session's output",
				}},
				{Seq: 6, TS: ts(6), Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{
					"name": "release_claim", "id": "call_release", "result": "Released 1 path(s)",
				}},
				{Seq: 7, TS: ts(7), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "done"}},
				{Seq: 8, TS: ts(8), Type: event.SessionIdle, Data: map[string]any{"report": "done"}},
			}
			writeSession(t, ws, id, events)

			m := NewManager(testRegistry(), ws)
			t.Cleanup(m.ReclaimAll)
			sess, err := m.Reopen("", id)
			if err != nil {
				t.Fatalf("Reopen: %v", err)
			}
			defer sess.Stop()

			loop := sess.currentLoop()
			got := loop.History()
			if want := engine.ReplayHistory(events); !reflect.DeepEqual(got, want) {
				t.Fatalf("history mismatch:\n got=%+v\nwant=%+v", got, want)
			}
			answered := false
			for _, msg := range got {
				answered = answered || (msg.Role == "tool" && msg.ToolCallID == "call_release")
			}
			if !answered {
				t.Errorf("replayed history lost the release_claim call: %+v", got)
			}
			for _, def := range loop.Tools.APIDefs() {
				if def.Function != nil && def.Function.Name == "release_claim" {
					t.Errorf("reopened %s session offers release_claim", mode)
				}
			}
		})
	}
}
