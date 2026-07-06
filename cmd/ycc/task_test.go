package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/docs"
)

// newDirectBackend returns a task backend over a fresh temp-workspace backlog,
// the same daemon-free path `ycc task` uses when no daemon is reachable.
func newDirectBackend(t *testing.T) (directBackend, string) {
	t.Helper()
	ws := t.TempDir()
	return directBackend{store: docs.NewStore(ws)}, ws
}

// TestRunTaskAddDirect: add creates a task file with a scaffolded body and
// prints the assigned id.
func TestRunTaskAddDirect(t *testing.T) {
	be, ws := newDirectBackend(t)
	ctx := context.Background()

	var out bytes.Buffer
	err := runTaskAdd(ctx, be, &out, strings.NewReader(""), addParams{
		title: "Wire the widget", desc: "Do the thing.", priority: 2,
	})
	if err != nil {
		t.Fatalf("runTaskAdd: %v", err)
	}
	if !strings.Contains(out.String(), "created 0001") {
		t.Fatalf("add output = %q, want it to print the id", out.String())
	}

	// The task file exists with the canonical scaffold.
	matches, _ := filepath.Glob(filepath.Join(ws, "backlog", "0001-*.md"))
	if len(matches) != 1 {
		t.Fatalf("expected one task file, got %v", matches)
	}
	tk, err := be.store.Get("0001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, want := range []string{"## Description", "## Acceptance criteria", "## Work log", "Do the thing."} {
		if !strings.Contains(tk.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, tk.Body)
		}
	}
	if tk.Priority != 2 {
		t.Fatalf("priority = %d, want 2", tk.Priority)
	}
}

// TestRunTaskAddStdin: --desc - reads the whole description from stdin.
func TestRunTaskAddStdin(t *testing.T) {
	be, _ := newDirectBackend(t)
	var out bytes.Buffer
	err := runTaskAdd(context.Background(), be, &out, strings.NewReader("From stdin body.\n"), addParams{
		title: "Piped", desc: "-",
	})
	if err != nil {
		t.Fatalf("runTaskAdd: %v", err)
	}
	tk, err := be.store.Get("0001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(tk.Body, "From stdin body.") {
		t.Fatalf("body should contain stdin text:\n%s", tk.Body)
	}
	// Default priority applied when unset.
	if tk.Priority != 3 {
		t.Fatalf("priority = %d, want default 3", tk.Priority)
	}
}

// TestRunTaskListDirect: list shows readiness marks, hides done by default and
// shows it with --all.
func TestRunTaskListDirect(t *testing.T) {
	be, _ := newDirectBackend(t)
	ctx := context.Background()
	a, err := be.store.Create("First", "", 1, nil, nil)
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	if _, err := be.store.Create("Second", "", 2, []string{a.ID}, nil); err != nil {
		t.Fatalf("create b: %v", err)
	}
	done, err := be.store.Create("Third", "", 3, nil, nil)
	if err != nil {
		t.Fatalf("create c: %v", err)
	}
	if _, err := be.store.Update(done.ID, func(tk *docs.Task) { tk.Status = docs.StatusDone }); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	var out bytes.Buffer
	if err := runTaskList(ctx, be, &out, false); err != nil {
		t.Fatalf("runTaskList: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "[READY]") {
		t.Fatalf("expected a [READY] mark:\n%s", s)
	}
	if !strings.Contains(s, "[blocked by "+a.ID+"]") {
		t.Fatalf("expected a blocked-by mark:\n%s", s)
	}
	if strings.Contains(s, "Third") {
		t.Fatalf("done task should be hidden by default:\n%s", s)
	}
	if !strings.Contains(s, "done task(s) hidden") {
		t.Fatalf("expected a hidden-count note:\n%s", s)
	}
	if !strings.Contains(s, "Ready to start") {
		t.Fatalf("expected a ready-to-start summary:\n%s", s)
	}

	var all bytes.Buffer
	if err := runTaskList(ctx, be, &all, true); err != nil {
		t.Fatalf("runTaskList --all: %v", err)
	}
	if !strings.Contains(all.String(), "Third") {
		t.Fatalf("--all should show the done task:\n%s", all.String())
	}
}

// TestRunTaskListEmpty: an empty backlog prints a friendly note.
func TestRunTaskListEmpty(t *testing.T) {
	be, _ := newDirectBackend(t)
	var out bytes.Buffer
	if err := runTaskList(context.Background(), be, &out, false); err != nil {
		t.Fatalf("runTaskList: %v", err)
	}
	if !strings.Contains(out.String(), "backlog is empty") {
		t.Fatalf("expected empty-backlog note:\n%s", out.String())
	}
}

// TestRunTaskShowDirect: show prints the header fields and the markdown body.
func TestRunTaskShowDirect(t *testing.T) {
	be, _ := newDirectBackend(t)
	if _, err := be.store.Create("Showable", docs.TaskBody("Detailed body here."), 4, nil, []string{"spec-1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	var out bytes.Buffer
	if err := runTaskShow(context.Background(), be, &out, "0001"); err != nil {
		t.Fatalf("runTaskShow: %v", err)
	}
	s := out.String()
	for _, want := range []string{"id:", "0001", "title:", "Showable", "priority:", "spec-1", "Detailed body here.", "readiness: READY"} {
		if !strings.Contains(s, want) {
			t.Fatalf("show output missing %q:\n%s", want, s)
		}
	}
}
