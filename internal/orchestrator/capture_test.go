package orchestrator

import (
	"context"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
)

// A capture turn that calls create_task produces a TaskID and a real task file.
func TestRunCaptureCreatesTask(t *testing.T) {
	ws := t.TempDir()
	store := docs.NewStore(ws)
	turner := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("create_task", `{"title":"Add retry to fetch","description":"retries on 5xx","priority":2}`),
	}}
	cd := CaptureDeps{Workspace: ws, Docs: store, Client: turner, Model: "m", ModelName: "m", MaxTok: 0}

	res, err := RunCapture(context.Background(), cd, "make fetch retry on errors", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.TaskID == "" {
		t.Fatalf("expected a created task id, got %+v", res)
	}
	if res.Question != "" {
		t.Fatalf("unexpected question: %q", res.Question)
	}
	tk, err := store.Get(res.TaskID)
	if err != nil {
		t.Fatalf("created task not found: %v", err)
	}
	if tk.Title != "Add retry to fetch" {
		t.Fatalf("title = %q", tk.Title)
	}
}

// A capture turn that calls ask_clarification returns the question without
// creating any task.
func TestRunCaptureAsksClarification(t *testing.T) {
	ws := t.TempDir()
	store := docs.NewStore(ws)
	turner := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("ask_clarification", `{"question":"Which endpoint should retry?"}`),
	}}
	cd := CaptureDeps{Workspace: ws, Docs: store, Client: turner, Model: "m", ModelName: "m"}

	res, err := RunCapture(context.Background(), cd, "add retries", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Question != "Which endpoint should retry?" {
		t.Fatalf("question = %q", res.Question)
	}
	if res.TaskID != "" {
		t.Fatalf("unexpected task id: %q", res.TaskID)
	}
	if tasks, _ := store.List(); len(tasks) != 0 {
		t.Fatalf("clarification should not create a task; got %d", len(tasks))
	}
}
