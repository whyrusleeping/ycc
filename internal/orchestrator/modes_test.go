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
	if !hasTool(featureReg, "switch_to_work") {
		t.Fatal("feature mode missing switch_to_work")
	}
	specReg, _ := BuildMode("spec", d, "judgement")
	if hasTool(specReg, "switch_to_work") {
		t.Fatal("spec mode should not have switch_to_work")
	}
	// spec.md is edited directly: authoring modes carry Read/Edit/Write, not a
	// dedicated spec tool.
	for _, mode := range []string{"spec", "feature", "bug"} {
		reg, _ := BuildMode(mode, d, "judgement")
		for _, tool := range []string{"Read", "Edit", "Write"} {
			if !hasTool(reg, tool) {
				t.Fatalf("%s mode missing %s", mode, tool)
			}
		}
		if hasTool(reg, "update_spec") || hasTool(reg, "read_spec") {
			t.Fatalf("%s mode should not have the removed read_spec/update_spec tools", mode)
		}
	}
}

func TestSwitchToWorkSignalsModeChange(t *testing.T) {
	res, _ := switchToWork().Call(context.Background(), map[string]any{})
	ctrl := tools.ControlOf(res)
	if ctrl == nil || !ctrl.Stop || ctrl.Mode != "work" {
		t.Fatalf("switch_to_work control = %+v", ctrl)
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
	reg, _ := BuildMode("spec", d, "judgement")

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
