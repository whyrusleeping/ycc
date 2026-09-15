package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

func TestBaselineUntrackedTaskDelegationReviewAndReopenCommit(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	task, err := store.Create("selected task", "## Description\n\nOriginal selected intent.\n\n## Acceptance criteria\n\n- Implement it.\n", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Create("unrelated task", "Unrelated baseline task.\n", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	otherBefore, err := os.ReadFile(other.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "user.txt"), []byte("user source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "user.txt")
	// Session startup captures the task before status and plan tools mutate it.
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	baseline := repo.OpenBaseline()
	if err := repo.PersistBaseline("original", baseline); err != nil {
		t.Fatal(err)
	}
	impl := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("Write", `{"file_path":"owned.txt","content":"implementation\n"}`),
		call("finish", `{"report":"Implemented and verified."}`),
		call("finish", `{"report":"Revision verified."}`),
	}}
	d := &Deps{
		Workspace: ws, Docs: store, Repo: repo, Baseline: baseline,
		Emitter: event.NewEmitter(&captureRec{}, "coordinator"), Asker: noopAsker{},
		Implementer: AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return impl }},
	}
	ctx := context.Background()
	invoke := func(tool *gollama.Tool, params map[string]any) string {
		t.Helper()
		result, err := tool.Call(ctx, params)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("%s: %s", tool.Name, result.Content)
		}
		return result.Content
	}
	invoke(updateTask(d), map[string]any{"task_id": task.ID, "status": "in_progress"})
	invoke(proposePlan(d), map[string]any{"task_id": task.ID, "plan": "A selected task plan."})
	invoke(spawnImplementer(d), map[string]any{"task_id": task.ID, "plan": "Implement the selected task."})
	invoke(sendToImplementer(d), map[string]any{"task_id": task.ID, "instructions": "Verify again.", "context_mode": "fresh"})
	if _, err := repo.Changes(baseline); err == nil {
		t.Fatal("strict changes unexpectedly adopted backlog")
	}
	before := d.reviewDiff(task.ID)
	if before.Err != nil {
		t.Fatal(before.Err)
	}
	if !strings.Contains(before.Result, "+Original selected intent.") || !strings.Contains(before.Result, "+A selected task plan.") {
		t.Fatalf("preload omits full adopted document: %s", before.Result)
	}
	if strings.Contains(before.Result, "Unrelated baseline task") || strings.Contains(before.Result, "user source") {
		t.Fatalf("review includes unrelated baseline work: %s", before.Result)
	}
	// Reopen with no in-memory focus. Explicit task IDs must select exactly the
	// same document and scope from the original durable baseline.
	reopened, err := git.OpenExisting(ws)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.LoadBaseline("original")
	if err != nil {
		t.Fatal(err)
	}
	d = &Deps{Workspace: ws, Docs: docs.NewStore(ws), Repo: reopened, Baseline: restored,
		Emitter: event.NewEmitter(&captureRec{}, "coordinator"), Asker: noopAsker{}}
	after := d.reviewDiff("1")
	if after.Err != nil {
		t.Fatal(after.Err)
	}
	if after.SnapshotID != before.SnapshotID {
		t.Fatal("reopen or numeric task alias changed review scope")
	}
	reviewer := &scripted{resp: []*gollama.ResponseMessageGenerate{call("finish", `{"report":"Accepted."}`)}}
	d.Reviewers = []AgentSpec{{Name: "reviewer", Model: "m", NewClient: func() engine.Turner { return reviewer }}}
	invoke(spawnReviewers(d), map[string]any{"task_id": task.ID})
	found := false
	for _, msg := range reviewer.messages {
		if strings.Contains(msg.Content, "+Original selected intent.") {
			found = true
		}
	}
	if !found {
		t.Fatal("reviewer did not receive adopted task diff")
	}
	invoke(commitTool(d), map[string]any{"task_id": task.ID, "message": "finish selected task", "outcome": "Implemented and verified."})
	committed := gitRun(t, ws, "show", "HEAD:backlog/"+filepath.Base(task.Path))
	if !strings.Contains(committed, "status: done") || !strings.Contains(committed, "Original selected intent.") {
		t.Fatalf("task not committed: %s", committed)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "show", "HEAD:owned.txt")); got != "implementation" {
		t.Fatalf("implementation missing: %q", got)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "diff", "--cached", "--name-only")); got != "user.txt" {
		t.Fatalf("unrelated staging lost: %q", got)
	}
	if got, err := os.ReadFile(other.Path); err != nil || string(got) != string(otherBefore) {
		t.Fatalf("unrelated task changed: %q, %v", got, err)
	}
	if got := gitRun(t, ws, "ls-tree", "HEAD", "--", "backlog/"+filepath.Base(other.Path)); got != "" {
		t.Fatalf("unrelated task committed: %s", got)
	}
	count := gitRun(t, ws, "rev-list", "--count", "HEAD")
	invoke(commitTool(d), map[string]any{"task_id": task.ID, "message": "finish selected task", "outcome": "Implemented and verified."})
	if got := gitRun(t, ws, "rev-list", "--count", "HEAD"); got != count {
		t.Fatal("done recovery duplicated commit")
	}
}
