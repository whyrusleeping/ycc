package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// Backlog/memory bookkeeping tools must keep working while another execution
// scope (a background implementer, a mutating agent, another session) holds the
// worktree lease. They are atomic docs.Store writes serialized by the store's
// own lock; failing them on the execution lease only blocks the user.
func TestBacklogToolsIgnoreBusyWorktreeLease(t *testing.T) {
	for _, mode := range []string{"chat", "pm", "work"} {
		t.Run(mode, func(t *testing.T) {
			d, store := bgDeps(t, &syncRec{}, nil, nil)
			defer d.Jobs.KillAll()
			ownership := workspacelease.NewService()
			d.Ownership = ownership
			d.CoordinatorToken = ownership.NewToken("this session coordinator")
			busy, err := ownership.Acquire(d.Workspace, ownership.NewToken("other session implementer"))
			if err != nil {
				t.Fatal(err)
			}
			defer busy.Release()

			proposed, err := store.CreateWithStatus("idea", "", 3, nil, nil, docs.StatusProposed)
			if err != nil {
				t.Fatal(err)
			}
			reg, _ := BuildMode(mode, d, false)
			calls := []gollama.ToolCallFunction{
				{Name: "update_task", Arguments: `{"task_id":"` + proposed.ID + `","status":"todo"}`},
				{Name: "create_task", Arguments: `{"title":"captured while busy"}`},
				{Name: "remember", Arguments: `{"note":"recorded while busy"}`},
			}
			if mode != "chat" {
				calls = append(calls, gollama.ToolCallFunction{Name: "propose_plan", Arguments: `{"task_id":"` + proposed.ID + `","plan":"planned while busy"}`})
			}
			for _, fn := range calls {
				if res := reg.Dispatch(context.Background(), gollama.ToolCall{Function: fn}); res.IsError {
					t.Fatalf("%s while worktree busy: %s", fn.Name, res.Content)
				}
			}
			if got, err := store.Get(proposed.ID); err != nil || got.Status != docs.StatusTodo {
				t.Fatalf("promoted task = %+v, %v; want todo", got, err)
			}
		})
	}
}

// Raw Edit/Write of docs-layer prose (task files, spec, memory, plans, other
// Markdown) must also work while another scope owns the worktree: a chat session
// tightening acceptance criteria or a policy doc must not wait hours for another
// session's implementer. Code writes still serialize with operation sections.
func TestFileToolsEditDocsLayerWhileWorktreeBusy(t *testing.T) {
	d, store := bgDeps(t, &syncRec{}, nil, nil)
	defer d.Jobs.KillAll()
	ownership := workspacelease.NewService()
	d.Ownership = ownership
	d.CoordinatorToken = ownership.NewToken("this session coordinator")
	busy, err := ownership.Acquire(d.Workspace, ownership.NewToken("other session implementer"))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Release()

	tasks, err := store.List()
	if err != nil || len(tasks) == 0 {
		t.Fatalf("list: %v %v", tasks, err)
	}
	taskRel, err := filepath.Rel(d.Workspace, tasks[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"spec.md": "# Spec\nold\n", "docs/validation.md": "policy old\n", "main.go": "package old\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(d.Workspace, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d.Workspace, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	reg, _ := BuildMode("chat", d, false)
	edit := func(path, old, new string) *gollama.ToolResult {
		args, _ := json.Marshal(map[string]string{"file_path": path, "old_string": old, "new_string": new})
		return reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: "Edit", Arguments: string(args)}})
	}
	for _, c := range []struct{ path, old, new string }{
		{taskRel, "## Work log", "## Acceptance\n- tightened\n\n## Work log"},
		{"spec.md", "old", "new"},
		{"docs/validation.md", "old", "new"},
	} {
		if res := edit(c.path, c.old, c.new); res.IsError {
			t.Fatalf("Edit %s while worktree busy: %s", c.path, res.Content)
		}
	}
	write := gollama.ToolCall{Function: gollama.ToolCallFunction{Name: "Write", Arguments: `{"file_path":"plans/new-plan.md","content":"steps\n"}`}}
	if res := reg.Dispatch(context.Background(), write); res.IsError {
		t.Fatalf("Write plan while worktree busy: %s", res.Content)
	}
	if got, err := store.Get(tasks[0].ID); err != nil || !strings.Contains(got.Body, "- tightened") {
		t.Fatalf("task after Edit = %+v, %v", got, err)
	}
	// A code write meets the other scope's operation section and waits for it
	// to end rather than failing.
	go func() {
		time.Sleep(50 * time.Millisecond)
		busy.Release()
	}()
	start := time.Now()
	if res := edit("main.go", "old", "new"); res.IsError {
		t.Fatalf("code Edit did not wait out the section: %s", res.Content)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("code Edit bypassed the held worktree section")
	}
}
