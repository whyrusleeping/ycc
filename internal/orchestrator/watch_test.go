package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/tools"
)

type watchConfirmAsker struct {
	noopAsker
	approve bool
	calls   int
	before  func()
}

func (a *watchConfirmAsker) Confirm(context.Context, string) (bool, error) {
	a.calls++
	if a.before != nil {
		a.before()
	}
	return a.approve, nil
}

func watchDefinitionArgs() map[string]any {
	return map[string]any{"name": "check-build", "brief": "Check progress and completion", "interval_minutes": float64(10)}
}

func TestWatchDefinitionPreflightBeforeConfirmation(t *testing.T) {
	for _, creating := range []bool{true, false} {
		t.Run(map[bool]string{true: "create", false: "update"}[creating], func(t *testing.T) {
			d := depsFor(t)
			asker := &watchConfirmAsker{approve: true}
			d.Asker = asker
			mutations, validations := 0, 0
			d.ScheduleWatch = func(context.Context, WatchSpec) (string, error) { mutations++; return "created", nil }
			d.UpdateWatch = func(context.Context, WatchSpec) (string, error) { mutations++; return "updated", nil }
			d.ValidateWatch = func(s WatchSpec, gotCreating bool) error {
				validations++
				if gotCreating != creating || s.Name != "check-build" || s.Interval != 10*time.Minute {
					t.Fatalf("unexpected validation: %+v, creating=%v", s, gotCreating)
				}
				return errors.New("definition rejected")
			}
			res, err := watchDefinitionTool(d, creating).Call(context.Background(), watchDefinitionArgs())
			if err != nil || !res.IsError || validations != 1 || asker.calls != 0 || mutations != 0 {
				t.Fatalf("res=%+v err=%v validations=%d confirms=%d mutations=%d", res, err, validations, asker.calls, mutations)
			}
			if !strings.Contains(res.Content, "no watch changed") || !strings.Contains(res.Content, "update_watch") {
				t.Fatalf("preflight error lacks recovery: %s", res.Content)
			}
		})
	}
}

func TestWatchDefinitionConfirmationAndReplacement(t *testing.T) {
	for _, creating := range []bool{true, false} {
		for _, approved := range []bool{true, false} {
			t.Run(map[bool]string{true: "create", false: "update"}[creating]+map[bool]string{true: "/approved", false: "/declined"}[approved], func(t *testing.T) {
				d := depsFor(t)
				validated := false
				asker := &watchConfirmAsker{approve: approved, before: func() {
					if !validated {
						t.Fatal("confirmation preceded validation")
					}
				}}
				d.Asker = asker
				args := watchDefinitionArgs()
				args["model"], args["probe"], args["probe_always"] = "fast", "check-progress", true
				args["expires_hours"], args["allowed_actions"] = float64(2), []any{"retry a failed fetch"}
				var preflight, mutated WatchSpec
				creates, updates := 0, 0
				d.ValidateWatch = func(s WatchSpec, c bool) error {
					if c != creating {
						t.Fatalf("creating=%v", c)
					}
					preflight, validated = s, true
					return nil
				}
				d.ScheduleWatch = func(_ context.Context, s WatchSpec) (string, error) { creates++; mutated = s; return "created", nil }
				d.UpdateWatch = func(_ context.Context, s WatchSpec) (string, error) { updates++; mutated = s; return "updated", nil }
				res, err := watchDefinitionTool(d, creating).Call(context.Background(), args)
				if err != nil || res.IsError == approved || asker.calls != 1 {
					t.Fatalf("res=%+v err=%v confirms=%d", res, err, asker.calls)
				}
				if !approved {
					if creates+updates != 0 {
						t.Fatal("declined definition mutated a watch")
					}
					return
				}
				if creates+updates != 1 || (creates == 1) != creating || !reflect.DeepEqual(preflight, mutated) {
					t.Fatalf("creates=%d updates=%d preflight=%+v mutated=%+v", creates, updates, preflight, mutated)
				}
				if mutated.Model != "fast" || mutated.Probe != "check-progress" || !mutated.ProbeAlways || len(mutated.AllowedActions) != 1 || time.Until(mutated.Expires) < 119*time.Minute {
					t.Fatalf("lost definition fields: %+v", mutated)
				}
			})
		}
	}
}

func TestUpdateWatchOmittedFieldsAreReplacementDefaults(t *testing.T) {
	d := depsFor(t)
	called := false
	d.UpdateWatch = func(_ context.Context, s WatchSpec) (string, error) {
		called = true
		if s.Model != "" || s.Probe != "" || s.ProbeAlways || !s.Expires.IsZero() || len(s.AllowedActions) != 0 {
			t.Fatalf("omitted fields did not reset: %+v", s)
		}
		return "replaced", nil
	}
	res, err := updateWatch(d).Call(context.Background(), watchDefinitionArgs())
	if err != nil || res.IsError || !called {
		t.Fatalf("res=%+v err=%v called=%v", res, err, called)
	}
}

func TestWatchDefinitionAbsoluteExpiryRoundtrip(t *testing.T) {
	for _, expiry := range []string{"2099-07-08T09:10:11Z", "2099-07-08T09:10:11.123456789+05:30"} {
		for _, creating := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/create=%v", expiry, creating), func(t *testing.T) {
				d := depsFor(t)
				want, err := time.Parse(time.RFC3339, expiry)
				if err != nil {
					t.Fatal(err)
				}
				d.GetWatch = func(string) (string, error) {
					data, err := json.Marshal(map[string]any{"definition": map[string]any{"expires": want}})
					return string(data), err
				}
				inspected, err := getWatch(d).Call(context.Background(), map[string]any{"name": "check-build"})
				if err != nil || inspected.IsError {
					t.Fatalf("inspection=%+v err=%v", inspected, err)
				}
				var info struct {
					Definition map[string]any `json:"definition"`
				}
				if err := json.Unmarshal([]byte(inspected.Content), &info); err != nil {
					t.Fatal(err)
				}
				validated := false
				asker := &watchConfirmAsker{approve: true, before: func() {
					if !validated {
						t.Fatal("confirmation before expiry validation")
					}
				}}
				d.Asker = asker
				d.ValidateWatch = func(s WatchSpec, c bool) error {
					if c != creating || !s.Expires.Equal(want) || s.Expires.Format(time.RFC3339Nano) != expiry {
						t.Fatalf("preflight expiry=%s creating=%v", s.Expires.Format(time.RFC3339Nano), c)
					}
					validated = true
					return nil
				}
				mutations := 0
				mutate := func(_ context.Context, s WatchSpec) (string, error) {
					mutations++
					if !s.Expires.Equal(want) || s.Expires.Format(time.RFC3339Nano) != expiry {
						t.Fatalf("forwarded expiry=%s want=%s", s.Expires.Format(time.RFC3339Nano), expiry)
					}
					return "saved", nil
				}
				d.ScheduleWatch, d.UpdateWatch = mutate, mutate
				args := watchDefinitionArgs()
				args["expires"] = info.Definition["expires"]
				args["probe"] = "corrected-progress-check"
				res, err := watchDefinitionTool(d, creating).Call(context.Background(), args)
				if err != nil || res.IsError || mutations != 1 || asker.calls != 1 {
					t.Fatalf("res=%+v err=%v mutations=%d confirms=%d", res, err, mutations, asker.calls)
				}
			})
		}
	}
}

func TestWatchDefinitionAbsoluteExpiryRejectedBeforeConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"conflict", map[string]any{"expires": "2099-07-08T09:10:11Z", "expires_hours": float64(2)}},
		{"conflict with null", map[string]any{"expires": "2099-07-08T09:10:11Z", "expires_hours": nil}},
		{"malformed", map[string]any{"expires": "next Tuesday"}},
		{"missing timezone", map[string]any{"expires": "2099-07-08T09:10:11"}},
		{"empty", map[string]any{"expires": ""}},
		{"wrong type", map[string]any{"expires": 2}},
		{"null", map[string]any{"expires": nil}},
	} {
		for _, creating := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/create=%v", tc.name, creating), func(t *testing.T) {
				d := depsFor(t)
				asker := &watchConfirmAsker{approve: true}
				d.Asker = asker
				mutate := func(context.Context, WatchSpec) (string, error) {
					t.Fatal("invalid expiry reached mutation")
					return "", nil
				}
				d.ScheduleWatch, d.UpdateWatch = mutate, mutate
				d.ValidateWatch = func(WatchSpec, bool) error { t.Fatal("invalid expiry reached backend validation"); return nil }
				args := watchDefinitionArgs()
				for key, value := range tc.args {
					args[key] = value
				}
				res, err := watchDefinitionTool(d, creating).Call(context.Background(), args)
				if err != nil || !res.IsError || asker.calls != 0 {
					t.Fatalf("res=%+v err=%v confirms=%d", res, err, asker.calls)
				}
			})
		}
	}
}

func TestWatchDefinitionParsingRejectsBeforeConfirmation(t *testing.T) {
	for _, tc := range []struct {
		field string
		value any
	}{
		{"name", 3}, {"name", "  "}, {"brief", false},
		{"interval_minutes", "10"}, {"interval_minutes", 5.5}, {"interval_minutes", 4}, {"interval_minutes", 1e30},
		{"expires_hours", "2"}, {"expires_hours", 0}, {"expires_hours", -1}, {"expires_hours", 1.5}, {"expires_hours", 1e30},
	} {
		t.Run(fmt.Sprintf("%s/%v", tc.field, tc.value), func(t *testing.T) {
			d := depsFor(t)
			asker := &watchConfirmAsker{approve: true}
			d.Asker = asker
			d.ScheduleWatch = func(context.Context, WatchSpec) (string, error) { t.Fatal("invalid mutation"); return "", nil }
			d.ValidateWatch = func(WatchSpec, bool) error { t.Fatal("invalid parameters reached backend"); return nil }
			args := watchDefinitionArgs()
			args[tc.field] = tc.value
			res, _ := scheduleWatch(d).Call(context.Background(), args)
			if !res.IsError || asker.calls != 0 {
				t.Fatalf("res=%+v confirms=%d", res, asker.calls)
			}
		})
	}
}

func TestScheduleWatchLegacyHookAndAtomicRejection(t *testing.T) {
	d := depsFor(t)
	asker := &watchConfirmAsker{approve: true}
	d.Asker = asker
	calls := 0
	d.ScheduleWatch = func(context.Context, WatchSpec) (string, error) { calls++; return "", errors.New("already exists") }
	res, _ := scheduleWatch(d).Call(context.Background(), watchDefinitionArgs())
	if !res.IsError || calls != 1 || asker.calls != 1 || !strings.Contains(res.Content, "update_watch") {
		t.Fatalf("legacy nil preflight/atomic rejection: res=%+v calls=%d confirms=%d", res, calls, asker.calls)
	}
}

func TestWatchInspectToolsReturnBackendContent(t *testing.T) {
	d := depsFor(t)
	const info = `{"definition":{"name":"check-build"},"notes":"baseline","last":{"summary":"healthy"}}`
	d.ListWatches = func() (string, error) { return "existing watches", nil }
	d.GetWatch = func(name string) (string, error) {
		if name != "check-build" {
			t.Fatalf("name=%q", name)
		}
		return info, nil
	}
	for _, tc := range []struct {
		tool *gollama.Tool
		want string
	}{{listWatches(d), "existing watches"}, {getWatch(d), info}} {
		res, err := tc.tool.Call(context.Background(), map[string]any{"name": "check-build"})
		if err != nil || res.IsError || res.Content != tc.want {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	}
}

func TestWatchPausePreflightAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		paused, approved          bool
		info                      string
		inspectErr                error
		wantConfirm, wantMutation int
	}{
		{name: "missing", paused: true, approved: true, inspectErr: errors.New("not found")},
		{name: "expired", approved: true, info: `{"definition":{"expires":"2000-01-01T00:00:00Z"}}`},
		{name: "invalid inspection", approved: true, info: `invalid json`},
		{name: "declined", paused: true, info: `{}`, wantConfirm: 1},
		{name: "pause", paused: true, approved: true, info: `{}`, wantConfirm: 1, wantMutation: 1},
		{name: "resume", approved: true, info: `{}`, wantConfirm: 1, wantMutation: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := depsFor(t)
			inspections, mutations := 0, 0
			asker := &watchConfirmAsker{approve: tc.approved, before: func() {
				if inspections != 1 {
					t.Fatal("confirmation before inspection")
				}
			}}
			d.Asker = asker
			d.GetWatch = func(name string) (string, error) {
				inspections++
				if name != "check-build" {
					t.Fatalf("inspect name=%q", name)
				}
				return tc.info, tc.inspectErr
			}
			d.SetWatchPaused = func(name string, paused bool) (string, error) {
				mutations++
				if name != "check-build" || paused != tc.paused {
					t.Fatalf("mutation %q %v", name, paused)
				}
				return "changed", nil
			}
			res, err := setWatchPaused(d).Call(context.Background(), map[string]any{"name": "check-build", "paused": tc.paused})
			if err != nil || res.IsError != (tc.wantMutation == 0) || inspections != 1 || asker.calls != tc.wantConfirm || mutations != tc.wantMutation {
				t.Fatalf("res=%+v err=%v inspections=%d confirms=%d mutations=%d", res, err, inspections, asker.calls, mutations)
			}
		})
	}
}

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

func TestWatchManagementToolRegistration(t *testing.T) {
	for _, mode := range []string{"chat", "watch", "pm", "work", "integrate"} {
		for _, impl := range []string{"direct", "delegate"} {
			d := depsFor(t)
			d.WorkImplementation = impl
			reg, _ := BuildMode(mode, d, false)
			names := []string{"list_watches", "get_watch", "update_watch", "set_watch_paused"}
			for _, name := range names {
				if hasTool(reg, name) {
					t.Fatalf("%s: %s registered without hooks", mode, name)
				}
			}
			d.ListWatches = func() (string, error) { return "", nil }
			d.GetWatch = func(string) (string, error) { return "{}", nil }
			d.UpdateWatch = func(context.Context, WatchSpec) (string, error) { return "", nil }
			d.SetWatchPaused = func(string, bool) (string, error) { return "", nil }
			reg, _ = BuildMode(mode, d, false)
			for _, name := range names {
				if hasTool(reg, name) != (mode == "chat") {
					t.Fatalf("%s/%s: wrong %s registration", mode, impl, name)
				}
			}
			d.GetWatch = nil
			reg, _ = BuildMode(mode, d, false)
			if hasTool(reg, "get_watch") || hasTool(reg, "set_watch_paused") {
				t.Fatalf("%s: inspection required", mode)
			}
		}
	}
}
