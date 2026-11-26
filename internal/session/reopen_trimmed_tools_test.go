package session

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

// pm and watch sessions whose history calls tools those modes no longer
// offer (leases; for pm also the notice and background job tools) reopen and
// replay those calls, and the reopened session does not offer them.
func TestReopenReplaysToolsPmAndWatchNoLongerOffer(t *testing.T) {
	cases := []struct {
		mode  string
		tools []string
	}{
		{"pm", []string{"acquire_lease", "release_lease", "notice", "set_intent", "job_output", "wait", "kill_job", "list_jobs"}},
		{"watch", []string{"acquire_lease", "release_lease"}},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			ws := t.TempDir()
			if _, err := git.Open(ws); err != nil {
				t.Fatal(err)
			}
			absWS, _ := filepath.Abs(ws)
			id := "s_trimmed_" + c.mode
			events := []event.Event{
				{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": c.mode, "workspace": absWS}},
				{Seq: 2, TS: ts(2), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "check the deploy"}},
				{Seq: 3, TS: ts(3), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "checking"}},
			}
			seq := 4
			for _, name := range c.tools {
				callID := "call_" + name
				events = append(events,
					event.Event{Seq: seq, TS: ts(seq), Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{
						"name": name, "args": `{}`, "id": callID,
					}},
					event.Event{Seq: seq + 1, TS: ts(seq + 1), Actor: "coordinator", Type: event.ToolResult, Data: map[string]any{
						"name": name, "id": callID, "result": "ok " + name,
					}},
				)
				seq += 2
			}
			events = append(events,
				event.Event{Seq: seq, TS: ts(seq), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "done"}},
				event.Event{Seq: seq + 1, TS: ts(seq + 1), Type: event.SessionIdle, Data: map[string]any{"report": "done"}},
			)
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
			answered := map[string]bool{}
			for _, msg := range got {
				if msg.Role == "tool" {
					answered[msg.ToolCallID] = true
				}
			}
			offered := map[string]bool{}
			for _, def := range loop.Tools.APIDefs() {
				if def.Function != nil {
					offered[def.Function.Name] = true
				}
			}
			for _, name := range c.tools {
				if !answered["call_"+name] {
					t.Errorf("replayed history lost the %s call: %+v", name, got)
				}
				if offered[name] {
					t.Errorf("reopened %s session offers %s", c.mode, name)
				}
			}
		})
	}
}
