package orchestrator

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

func depsFor(t *testing.T) *Deps {
	t.Helper()
	ws := t.TempDir()
	return &Deps{
		Workspace: ws,
		Docs:      docs.NewStore(ws),
		Emitter:   event.NewEmitter(event.NewStdoutRecorder(io.Discard), "coordinator"),
		Asker:     noopAsker{},
	}
}

func TestModesListed(t *testing.T) {
	names := map[string]bool{}
	for _, m := range Modes() {
		names[m.Name] = true
	}
	// Exactly three modes: pm, chat, work — the former spec/backlog/feature/bug
	// modes were collapsed into pm.
	for _, want := range []string{"pm", "chat", "work"} {
		if !names[want] {
			t.Fatalf("mode %q missing from Modes()", want)
		}
	}
	for _, gone := range []string{"spec", "backlog", "feature", "bug"} {
		if names[gone] {
			t.Fatalf("mode %q should have been removed (collapsed into pm)", gone)
		}
	}
}

func TestPresetsOpenPM(t *testing.T) {
	want := map[string]bool{"onboard": false, "feature": false, "bug": false, "spec": false, "backlog": false}
	for _, p := range Presets() {
		if _, ok := want[p.Name]; !ok {
			t.Fatalf("unexpected preset %q", p.Name)
		}
		want[p.Name] = true
		if p.Mode != "pm" {
			t.Fatalf("preset %q opens mode %q, want pm", p.Name, p.Mode)
		}
		if strings.TrimSpace(p.Prompt) == "" {
			t.Fatalf("preset %q has no opening prompt", p.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("preset %q missing", name)
		}
	}
}

// The onboarding prompt must steer the agent to detect both greenfield and
// brownfield situations from the workspace.
func TestOnboardPromptCoversBothBranches(t *testing.T) {
	lower := strings.ToLower(onboardPresetPrompt)
	for _, want := range []string{"greenfield", "brownfield"} {
		if !strings.Contains(lower, want) {
			t.Fatalf("onboardPresetPrompt does not mention %q", want)
		}
	}
}

func TestBuildModeToolsets(t *testing.T) {
	d := depsFor(t)
	// pm exposes planning/docs/backlog tools and switch_to_work, but NO
	// implementation tools (no spawn_implementer / commit).
	pmReg, _ := BuildMode("pm", d, "judgement")
	for _, want := range []string{"Read", "Edit", "Write", "Bash", "list_backlog", "get_task", "create_task", "update_task", "propose_plan", "switch_to_work", "ask_user", "finish"} {
		if !hasTool(pmReg, want) {
			t.Fatalf("pm mode missing %s", want)
		}
	}
	for _, gone := range []string{"spawn_implementer", "spawn_reviewers", "commit"} {
		if hasTool(pmReg, gone) {
			t.Fatalf("pm mode should not have %s (no implementation)", gone)
		}
	}
	// The removed authoring modes no longer build.
	for _, mode := range []string{"spec", "backlog", "feature", "bug"} {
		reg, _ := BuildMode(mode, d, "judgement")
		// Unknown modes fall through to the work coordinator; assert they are not
		// silently still distinct authoring modes by checking they carry the work
		// pipeline (spawn_implementer).
		if !hasTool(reg, "spawn_implementer") {
			t.Fatalf("removed mode %q should fall through to work coordinator", mode)
		}
	}
}

func TestWorkCoordinatorHasFileAndPipelineTools(t *testing.T) {
	d := depsFor(t)
	reg, _ := BuildMode("work", d, "judgement")
	// The work coordinator can inspect/edit the workspace directly...
	for _, want := range []string{"Read", "Write", "Edit", "Bash"} {
		if !hasTool(reg, want) {
			t.Fatalf("work coordinator missing file tool %s", want)
		}
	}
	// ...while still driving the implementation pipeline.
	for _, want := range []string{"spawn_implementer", "spawn_reviewers", "commit", "list_backlog"} {
		if !hasTool(reg, want) {
			t.Fatalf("work coordinator missing pipeline tool %s", want)
		}
	}
}

func TestListBacklogReadiness(t *testing.T) {
	d := depsFor(t)
	a, _ := d.Docs.Create("alpha", "", 1, nil, nil) // 0001, no deps -> READY
	d.Docs.Update(a.ID, func(tk *docs.Task) { tk.Status = docs.StatusDone })
	d.Docs.Create("beta", "", 1, []string{a.ID}, nil)    // 0002 dep on done 0001 -> READY
	d.Docs.Create("gamma", "", 1, []string{"0002"}, nil) // 0003 dep on todo 0002 -> blocked
	res, _ := listBacklog(d).Call(context.Background(), map[string]any{})
	out := res.Content
	if !strings.Contains(out, "0002") || !strings.Contains(out, "[READY]") {
		t.Fatalf("expected 0002 marked READY:\n%s", out)
	}
	if !strings.Contains(out, "[blocked by 0002]") {
		t.Fatalf("expected 0003 blocked by 0002:\n%s", out)
	}
	if !strings.Contains(out, "Ready to start (all deps done): 0002") {
		t.Fatalf("expected ready summary listing 0002:\n%s", out)
	}
}

func TestSwitchToWorkSignalsModeChangeWithTask(t *testing.T) {
	d := depsFor(t)
	res, _ := switchToWork(d).Call(context.Background(), map[string]any{"task_id": "0021", "plan": "do the thing"})
	ctrl := tools.ControlOf(res)
	if ctrl == nil || !ctrl.Stop || ctrl.Mode != "work" {
		t.Fatalf("switch_to_work control = %+v", ctrl)
	}
	// The carried prompt must name the specific task so work implements THAT task.
	if !strings.Contains(ctrl.Prompt, "0021") {
		t.Fatalf("switch_to_work prompt does not carry the target task: %q", ctrl.Prompt)
	}
}

func TestSwitchToWorkRequiresApproval(t *testing.T) {
	d := depsFor(t)
	d.Asker = declineAsker{}
	res, _ := switchToWork(d).Call(context.Background(), map[string]any{"task_id": "0021", "plan": "p"})
	if ctrl := tools.ControlOf(res); ctrl != nil && ctrl.Mode == "work" {
		t.Fatal("switch_to_work transitioned despite the user declining approval")
	}
}

func TestCreateTask(t *testing.T) {
	d := depsFor(t)
	ctx := context.Background()

	r, err := createTask(d).Call(ctx, map[string]any{
		"title": "Wire the TUI", "description": "build it", "priority": float64(2),
		"depends_on": []any{"0003"},
	})
	if err != nil || r.IsError {
		t.Fatalf("create_task: %v %s", err, r.Content)
	}
	tasks, _ := d.Docs.List()
	if len(tasks) != 1 || tasks[0].Title != "Wire the TUI" || tasks[0].Priority != 2 {
		t.Fatalf("task not created correctly: %+v", tasks)
	}
}

// Writing spec.md through an authoring mode's Write tool persists the file and
// emits a doc_updated event via the Workspace OnWrite hook.
func TestSpecEditEmitsDocUpdated(t *testing.T) {
	ws := t.TempDir()
	var buf bytes.Buffer
	d := &Deps{
		Workspace: ws,
		Docs:      docs.NewStore(ws),
		Emitter:   event.NewEmitter(event.NewStdoutRecorder(&buf), "coordinator"),
		Asker:     noopAsker{},
	}
	reg, _ := BuildMode("pm", d, "judgement")

	res := reg.Dispatch(context.Background(), gollama.ToolCall{
		Function: gollama.ToolCallFunction{
			Name:      "Write",
			Arguments: `{"file_path":"spec.md","content":"# Spec\n\n## Goals\nship it\n"}`,
		},
	})
	if res.IsError {
		t.Fatalf("Write spec.md: %s", res.Content)
	}
	body, _ := d.Docs.ReadSpec()
	if !strings.Contains(body, "## Goals") || !strings.Contains(body, "ship it") {
		t.Fatalf("spec not written:\n%s", body)
	}
	if !strings.Contains(buf.String(), string(event.DocUpdated)) {
		t.Fatalf("expected a %s event, got events:\n%s", event.DocUpdated, buf.String())
	}
}

func hasTool(reg *tools.Registry, name string) bool {
	for _, def := range reg.APIDefs() {
		if def.Function != nil && def.Function.Name == name {
			return true
		}
	}
	return false
}

// declineAsker rejects every confirmation, simulating a user who declines (or no
// human being available in autonomous mode).
type declineAsker struct{}

func (declineAsker) Ask(context.Context, string, []string) (string, error) { return "ok", nil }
func (declineAsker) Confirm(context.Context, string) (bool, error)         { return false, nil }

// captureRec records every event in memory so tests can assert on emission.
type captureRec struct{ events []event.Event }

func (r *captureRec) Record(actor string, t event.Type, data map[string]any) event.Event {
	ev := event.Event{Seq: len(r.events) + 1, Actor: actor, Type: t, Data: data}
	r.events = append(r.events, ev)
	return ev
}

func (r *captureRec) focusTasks() []string {
	var out []string
	for _, ev := range r.events {
		if ev.Type == event.TaskFocus {
			out = append(out, ev.Data["task"].(string))
		}
	}
	return out
}

// The pm→work hand-off records a task_focus for the explicit target task so the
// session is durably linked to it (spec §20.2).
func TestSwitchToWorkEmitsTaskFocus(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")

	if _, err := switchToWork(d).Call(context.Background(), map[string]any{"task_id": "0021", "plan": "p"}); err != nil {
		t.Fatalf("switch_to_work: %v", err)
	}
	if got := rec.focusTasks(); len(got) != 1 || got[0] != "0021" {
		t.Fatalf("focus events = %v, want [0021]", got)
	}
}

// A declined hand-off must not record focus (work never starts).
func TestSwitchToWorkDeclinedEmitsNoFocus(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	d.Asker = declineAsker{}

	if _, err := switchToWork(d).Call(context.Background(), map[string]any{"task_id": "0021", "plan": "p"}); err != nil {
		t.Fatalf("switch_to_work: %v", err)
	}
	if got := rec.focusTasks(); len(got) != 0 {
		t.Fatalf("declined hand-off emitted focus events %v", got)
	}
}

// The work coordinator records focus when it accepts a task (update_task→
// in_progress), and dedupes: re-focusing the same task is a no-op, focusing a new
// task emits again. Other status changes don't establish focus.
func TestUpdateTaskInProgressEmitsFocusWithDedupe(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	ctx := context.Background()

	a, err := d.Docs.Create("task a", "", 3, nil, nil)
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := d.Docs.Create("task b", "", 3, nil, nil)
	if err != nil {
		t.Fatalf("create b: %v", err)
	}

	call := func(id, status string) {
		if _, err := updateTask(d).Call(ctx, map[string]any{"task_id": id, "status": status}); err != nil {
			t.Fatalf("update_task %s %s: %v", id, status, err)
		}
	}
	call(a.ID, "in_progress") // focus a
	call(a.ID, "in_progress") // dedupe: still a, no new event
	call(a.ID, "in_review")   // non-pickup status: no focus
	call(b.ID, "in_progress") // focus b

	want := []string{a.ID, b.ID}
	got := rec.focusTasks()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("focus events = %v, want %v", got, want)
	}
}
