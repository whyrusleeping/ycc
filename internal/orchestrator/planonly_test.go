package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
)

func dispatch(t *testing.T, reg interface {
	Dispatch(context.Context, gollama.ToolCall) *gollama.ToolResult
}, name string, args map[string]any) *gollama.ToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return reg.Dispatch(context.Background(), gollama.ToolCall{ID: "c", Type: "function",
		Function: gollama.ToolCallFunction{Name: name, Arguments: string(raw)}})
}

// Plan only refuses chat Write/Edit outside the planning docs (backlog, plans,
// memory.md, spec and doc_globs), allows them inside, and lifts when turned off.
func TestPlanOnlyChatWriteGate(t *testing.T) {
	d, store := bgDeps(t, &syncRec{}, nil, nil)
	if err := os.MkdirAll(filepath.Join(d.Workspace, ".ycc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.Workspace, ".ycc", "config.toml"), []byte("doc_globs = [\"docs/design/*.md\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.Workspace, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.Docs = docs.NewStore(d.Workspace) // re-read the config written above
	tasks, err := store.List()
	if err != nil || len(tasks) == 0 {
		t.Fatalf("tasks = %v, %v", tasks, err)
	}
	taskRel, err := filepath.Rel(d.Workspace, tasks[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := BuildMode("chat", d, false)
	d.SetPlanOnly(true)

	for _, path := range []string{"spec.md", "docs/design/cache.md", "plans/cache.md", "memory.md"} {
		if res := dispatch(t, reg, "Write", map[string]any{"file_path": path, "content": "# notes\n"}); res.IsError {
			t.Fatalf("plan-only Write %s refused: %s", path, res.Content)
		}
	}
	if res := dispatch(t, reg, "Edit", map[string]any{"file_path": taskRel, "old_string": "## Work log", "new_string": "## Work log\n- planned"}); res.IsError {
		t.Fatalf("plan-only Edit of the backlog task refused: %s", res.Content)
	}
	for _, path := range []string{"cache.go", "README.md", "docs/other/notes.md"} {
		res := dispatch(t, reg, "Write", map[string]any{"file_path": path, "content": "x\n"})
		if !res.IsError || !strings.Contains(res.Content, "plan-only mode is on") {
			t.Fatalf("plan-only Write %s = %+v", path, res)
		}
		if _, err := os.Stat(filepath.Join(d.Workspace, path)); !os.IsNotExist(err) {
			t.Fatalf("refused Write still created %s (%v)", path, err)
		}
	}
	res := dispatch(t, reg, "Edit", map[string]any{"file_path": "main.go", "old_string": "package main", "new_string": "package cache"})
	if !res.IsError || !strings.Contains(res.Content, "main.go was not changed") {
		t.Fatalf("plan-only Edit main.go = %+v", res)
	}

	// A link under a planning directory cannot redirect a write into code.
	if err := os.MkdirAll(filepath.Join(d.Workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"plans/code": "../src", "plans/main.md": "../main.go", "plans/dangling.md": "../src/new.go"} {
		if err := os.Symlink(target, filepath.Join(d.Workspace, link)); err != nil {
			t.Fatal(err)
		}
	}
	if res := dispatch(t, reg, "Write", map[string]any{"file_path": "plans/code/unplanned.go", "content": "package src\n"}); !res.IsError {
		t.Fatalf("Write through a directory link = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(d.Workspace, "src", "unplanned.go")); !os.IsNotExist(err) {
		t.Fatalf("Write through a directory link created src/unplanned.go (%v)", err)
	}
	if res := dispatch(t, reg, "Edit", map[string]any{"file_path": "plans/main.md", "old_string": "package main", "new_string": "package cache"}); !res.IsError {
		t.Fatalf("Edit through a file link = %+v", res)
	}
	if res := dispatch(t, reg, "Write", map[string]any{"file_path": "plans/dangling.md", "content": "x\n"}); !res.IsError {
		t.Fatalf("Write through a dangling link = %+v", res)
	}
	if data, _ := os.ReadFile(filepath.Join(d.Workspace, "main.go")); string(data) != "package main\n" {
		t.Fatalf("main.go changed through a link: %q", data)
	}
	if res := dispatch(t, reg, "Write", map[string]any{"file_path": "plans/real.md", "content": "# plan\n"}); res.IsError {
		t.Fatalf("plain plan Write refused alongside links: %s", res.Content)
	}

	d.SetPlanOnly(false)
	if res := dispatch(t, reg, "Edit", map[string]any{"file_path": "main.go", "old_string": "package main", "new_string": "package cache"}); res.IsError {
		t.Fatalf("Edit after plan only was turned off: %s", res.Content)
	}
}

// Plan only forces spawned agents read-only and refuses follow-ups to an agent
// that was started with write access.
func TestPlanOnlySubagentsReadOnly(t *testing.T) {
	d, _ := bgDeps(t, &syncRec{}, nil, nil)
	d.ResolveAgent = func(name string) (AgentSpec, error) {
		return AgentSpec{Name: name, Model: "m", NewClient: func() engine.Turner {
			return &scripted{resp: []*gollama.ResponseMessageGenerate{text("done")}}
		}}, nil
	}
	if res, _ := spawnAgent(d).Call(context.Background(), map[string]any{"model": "coder", "prompt": "edit it", "mutating": true}); res.IsError {
		t.Fatalf("spawn = %+v", res)
	}
	waitGenericIdle(t, d, "agent_1")

	d.SetPlanOnly(true)
	res, _ := spawnAgent(d).Call(context.Background(), map[string]any{"model": "coder", "prompt": "edit it", "mutating": true})
	if res.IsError || !strings.Contains(res.Content, "mutating=true was ignored") {
		t.Fatalf("plan-only spawn = %+v", res)
	}
	d.mu.Lock()
	h := d.genericAgent["agent_2"]
	d.mu.Unlock()
	if h == nil || h.writeAccess || hasTool(h.loop.Tools, "Write") || hasTool(h.loop.Tools, "Edit") {
		t.Fatalf("plan-only agent has write access: %+v", h)
	}
	waitGenericIdle(t, d, "agent_2")
	res, _ = sendToAgent(d).Call(context.Background(), map[string]any{"agent_id": "agent_1", "prompt": "continue"})
	if !res.IsError || !strings.Contains(res.Content, "plan-only mode is on") {
		t.Fatalf("plan-only follow-up to a writer = %+v", res)
	}
	if res, _ := sendToAgent(d).Call(context.Background(), map[string]any{"agent_id": "agent_2", "prompt": "continue"}); res.IsError {
		t.Fatalf("plan-only follow-up to a reader = %+v", res)
	}
}
