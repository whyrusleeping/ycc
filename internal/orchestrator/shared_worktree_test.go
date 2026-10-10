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
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// Two sessions work in one worktree at the same time. Each one's commit holds
// exactly its own files (plus its task document), the second commit is not
// invalidated by the first one moving HEAD, and neither session is ever
// refused for the other's activity.
func TestTwoSessionsShareAWorktreeAndCommitIndependently(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	for _, title := range []string{"session A task", "session B task"} {
		task, err := store.Create(title, "## Acceptance\n- done\n\n## Work log\n", 1, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Update(task.ID, func(task *docs.Task) { task.Status = docs.StatusInReview }); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, ws, "add", "-A")
	gitRun(t, ws, "commit", "-m", "seed")

	ownership := workspacelease.NewService()
	sessions := map[string]*Deps{}
	depsB := func() *Deps { return sessions["s_b"] }
	session := func(id string) (*Deps, *gollama.Tool, func(name, args string) *gollama.ToolResult) {
		r, err := git.Open(ws) // each session captures its own baseline at start
		if err != nil {
			t.Fatal(err)
		}
		d := &Deps{
			Workspace: ws, Repo: r, Baseline: r.OpenBaseline(), Docs: store,
			Emitter: event.NewEmitter(&captureRec{}, "coordinator"), Asker: noopAsker{},
			Ownership: ownership, CoordinatorToken: ownership.NewScopedToken(id, "session "+id+" coordinator"),
			WorkImplementation: "direct",
		}
		sessions[id] = d
		reg, _ := BuildMode("chat", d, false)
		dispatch := func(name, args string) *gollama.ToolResult {
			return reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: name, Arguments: args}})
		}
		return d, commitTool(d), dispatch
	}
	commit := func(tool *gollama.Tool, taskID, msg string) *gollama.ToolResult {
		res, err := tool.Call(context.Background(), map[string]any{"task_id": taskID, "message": msg, "outcome": "Done."})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	write := func(do func(string, string) *gollama.ToolResult, path, content string) {
		args, _ := json.Marshal(map[string]string{"file_path": path, "content": content})
		if res := do("Write", string(args)); res.IsError {
			t.Fatalf("Write %s: %s", path, res.Content)
		}
	}

	// Uncommitted bookkeeping predates both sessions: B's task doc is already
	// in progress and memory.md has an unrecorded note.
	if _, err := store.Update("0002", func(task *docs.Task) { task.Status = docs.StatusInProgress }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMemory("earlier note", "lesson"); err != nil {
		t.Fatal(err)
	}

	_, commitA, doA := session("s_a")
	write(doA, "a.go", "package a\n")
	_, commitB, doB := session("s_b") // starts while A has uncommitted work
	write(doB, "b.go", "package b\n")
	write(doA, "a2.go", "package a\n")
	write(doA, "shared.go", "package shared // from A\n")
	// Both sessions keep writing shared bookkeeping that was already dirty at
	// their baselines; neither may become unable to commit because of it.
	if _, err := store.AppendWorkLog("0002", "B progress"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMemory("A learned something", "lesson"); err != nil {
		t.Fatal(err)
	}
	if res := doB("Edit", `{"file_path":"shared.go","old_string":"from A","new_string":"from A and B"}`); res.IsError {
		t.Fatalf("B edit of shared file: %s", res.Content)
	}
	dB := depsB()
	changes, err := dB.changeset("0002")
	if err != nil {
		t.Fatal(err)
	}
	if note := attributionNote(changes); !strings.Contains(note, "shared.go") || !strings.Contains(note, "a2.go") {
		t.Fatalf("B's manifest does not surface the shared tree:\n%s", note)
	}

	if res := commit(commitB, "0002", "session B work"); res.IsError {
		t.Fatalf("B commit: %s", res.Content)
	}
	if got := gitRun(t, ws, "show", "--name-only", "--format=", "HEAD"); strings.Contains(got, "a.go") || strings.Contains(got, "a2.go") ||
		!strings.Contains(got, "b.go") || !strings.Contains(got, "shared.go") {
		t.Fatalf("B's commit holds the wrong files:\n%s", got)
	}
	if got := ownership.Claims(ws)["shared.go"]; len(got) != 0 {
		t.Fatalf("committed, clean shared.go still claimed by %v", got)
	}

	// A keeps working after B moved HEAD, then commits on top.
	if res := doA("Edit", `{"file_path":"a.go","old_string":"package a","new_string":"package a // edited"}`); res.IsError {
		t.Fatalf("A edit after B's commit: %s", res.Content)
	}
	if res := commit(commitA, "0001", "session A work"); res.IsError {
		t.Fatalf("A commit after B moved HEAD: %s", res.Content)
	}
	got := gitRun(t, ws, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(got, "a.go") || !strings.Contains(got, "a2.go") || strings.Contains(got, "b.go") || strings.Contains(got, "shared.go") {
		t.Fatalf("A's commit holds the wrong files:\n%s", got)
	}
	if status := gitRun(t, ws, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("worktree not clean after both commits:\n%s", status)
	}
	if data, _ := os.ReadFile(filepath.Join(ws, "a.go")); !strings.Contains(string(data), "edited") {
		t.Fatalf("a.go = %q", data)
	}
	if claims := ownership.Claims(ws); len(claims) != 0 {
		t.Fatalf("committed paths are still claimed: %v", claims)
	}
}
