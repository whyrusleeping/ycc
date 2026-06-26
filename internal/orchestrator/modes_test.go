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
	want := map[string]bool{"feature": false, "bug": false, "spec": false, "backlog": false}
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
