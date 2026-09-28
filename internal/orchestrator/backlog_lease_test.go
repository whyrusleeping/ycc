package orchestrator

import (
	"context"
	"testing"

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
