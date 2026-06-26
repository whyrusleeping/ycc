package orchestrator

import (
	"context"
	"io"
	"strings"
	"testing"

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
	for _, want := range []string{"chat", "work", "spec", "backlog", "feature", "bug"} {
		if !names[want] {
			t.Fatalf("mode %q missing from Modes()", want)
		}
	}
}

func TestBuildModeToolsets(t *testing.T) {
	d := depsFor(t)
	// feature mode must expose switch_to_work; spec mode must not.
	featureReg, _ := BuildMode("feature", d, "judgement")
	if !hasTool(featureReg, "switch_to_work") || !hasTool(featureReg, "update_spec") {
		t.Fatal("feature mode missing switch_to_work/update_spec")
	}
	specReg, _ := BuildMode("spec", d, "judgement")
	if hasTool(specReg, "switch_to_work") {
		t.Fatal("spec mode should not have switch_to_work")
	}
	if !hasTool(specReg, "update_spec") {
		t.Fatal("spec mode missing update_spec")
	}
}

func TestSwitchToWorkSignalsModeChange(t *testing.T) {
	res, _ := switchToWork().Call(context.Background(), map[string]any{})
	ctrl := tools.ControlOf(res)
	if ctrl == nil || !ctrl.Stop || ctrl.Mode != "work" {
		t.Fatalf("switch_to_work control = %+v", ctrl)
	}
}

func TestCreateTaskAndUpdateSpec(t *testing.T) {
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

	if _, err := updateSpec(d).Call(ctx, map[string]any{"section": "Goals", "content": "ship it"}); err != nil {
		t.Fatal(err)
	}
	body, _ := d.Docs.ReadSpec()
	if !strings.Contains(body, "## Goals") || !strings.Contains(body, "ship it") {
		t.Fatalf("spec not updated:\n%s", body)
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
