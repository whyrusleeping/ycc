package orchestrator

import (
	"context"
	"os"
	"path/filepath"
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

	res, err := RunCapture(context.Background(), cd, nil, "make fetch retry on errors", "", "")
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

// The capture agent has bounded read access: it can Read files in the workspace
// and query the backlog (list_backlog) while drafting a task, then still end by
// creating a well-formed task. This verifies those grounding tools are
// registered and resolve across a multi-turn capture run.
func TestRunCaptureGroundsInCodebase(t *testing.T) {
	ws := t.TempDir()
	store := docs.NewStore(ws)

	// A real file the agent can read to ground the task.
	if err := os.WriteFile(filepath.Join(ws, "fetch.go"), []byte("package main\n// fetch does an HTTP GET\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An existing backlog task so list_backlog returns content.
	if _, err := store.Create("Existing task", "## Work log\n", 3, nil, nil); err != nil {
		t.Fatal(err)
	}

	turner := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("Read", `{"file_path":"fetch.go"}`),
		call("list_backlog", `{}`),
		call("create_task", `{"title":"Add retry to fetch","description":"retries on 5xx","priority":2}`),
	}}
	cd := CaptureDeps{Workspace: ws, Docs: store, Client: turner, Model: "m", ModelName: "m"}

	res, err := RunCapture(context.Background(), cd, nil, "make fetch retry on errors", "", "")
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

	res, err := RunCapture(context.Background(), cd, nil, "add retries", "", "")
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
