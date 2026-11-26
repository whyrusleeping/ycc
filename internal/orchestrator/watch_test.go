package orchestrator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/whyrusleeping/ycc/internal/tools"
)

func TestWatchReportCompletionNotification(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		flags                      map[string]any
		completed, notify, invalid bool
	}{
		{name: "ordinary"},
		{name: "completion default", flags: map[string]any{"completed": true}, completed: true, notify: true},
		{name: "completion suppressed", flags: map[string]any{"completed": true, "notify": false}, completed: true},
		{name: "completion explicit", flags: map[string]any{"completed": true, "notify": true}, completed: true, notify: true},
		{name: "notify without completion", flags: map[string]any{"notify": true}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := depsFor(t)
			calls := 0
			d.WatchReport = func(r WatchReport) (string, error) {
				calls++
				if r.Completed != tc.completed || r.Notify != tc.notify {
					t.Fatalf("report=%+v", r)
				}
				data, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err := json.Unmarshal(data, &fields); err != nil {
					t.Fatal(err)
				}
				if (fields["completed"] == true) != tc.completed || (fields["notify"] == true) != tc.notify {
					t.Fatalf("json=%s", data)
				}
				return "recorded", nil
			}
			args := map[string]any{"status": WatchOK, "summary": "Checked progress", "notes": "baseline"}
			for k, v := range tc.flags {
				args[k] = v
			}
			res, err := watchReport(d).Call(context.Background(), args)
			if err != nil || res.IsError != tc.invalid || calls != map[bool]int{true: 0, false: 1}[tc.invalid] {
				t.Fatalf("res=%+v err=%v calls=%d", res, err, calls)
			}
			if !tc.invalid {
				control, ok := res.Structured.(*tools.Control)
				if !ok || !control.Stop || control.Report != "Checked progress" {
					t.Fatalf("control=%+v", res.Structured)
				}
			}
		})
	}
}

// Watches are created and managed only through the clients and RPC; no agent
// mode offers watch-management tools, and only watch runs get watch_report.
func TestNoAgentWatchManagementTools(t *testing.T) {
	for _, mode := range []string{"chat", "watch", "pm", "work", "integrate"} {
		for _, impl := range []string{"direct", "delegate"} {
			for _, unattended := range []bool{false, true} {
				d := depsFor(t)
				d.WorkImplementation = impl
				reg, _ := BuildMode(mode, d, unattended)
				for _, name := range []string{"schedule_watch", "list_watches", "get_watch", "update_watch", "set_watch_paused"} {
					if hasTool(reg, name) {
						t.Fatalf("%s/%s unattended=%t: %s offered", mode, impl, unattended, name)
					}
				}
				if hasTool(reg, "watch_report") != (mode == "watch") {
					t.Fatalf("%s/%s unattended=%t: wrong watch_report registration", mode, impl, unattended)
				}
			}
		}
	}
}
