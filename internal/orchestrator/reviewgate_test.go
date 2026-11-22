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
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// A snapshot that takes whole a code file another session also changed is not
// reviewed until the coordinator chooses to take it; that choice holds for
// later re-reviews of the task.
func TestReviewWarnsBeforeSnapshotWithAnotherSessionsChanges(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "shared.go"), []byte("package shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.CreateWithStatus("B task", "## Work log\n", 1, nil, nil, docs.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "-A")
	gitRun(t, ws, "commit", "-m", "seed")
	ownership := workspacelease.NewService()
	turner := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("submit_review", `{"verdict":"revise","summary":"fix it"}`),
		call("submit_review", `{"verdict":"accept","summary":"ok"}`),
	}}
	session := func(id string) (*Deps, func(name, args string) *gollama.ToolResult) {
		r, err := git.Open(ws)
		if err != nil {
			t.Fatal(err)
		}
		d := &Deps{
			Workspace: ws, Repo: r, Baseline: r.OpenBaseline(), Docs: store,
			Emitter: event.NewEmitter(&captureRec{}, "coordinator"), Asker: noopAsker{},
			Ownership: ownership, CoordinatorToken: ownership.NewScopedToken(id, "session "+id+" coordinator"),
			Reviewers: []AgentSpec{{Name: "rev", Model: "m", NewClient: func() engine.Turner { return turner }}},
		}
		reg, _ := BuildMode("chat", d, false)
		return d, func(name, args string) *gollama.ToolResult {
			return reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: name, Arguments: args}})
		}
	}
	_, doA := session("s_a")
	dB, doB := session("s_b")
	if res := doB("Write", `{"file_path":"b.go","content":"package b\n"}`); res.IsError {
		t.Fatalf("B write: %s", res.Content)
	}
	review := func(tool *gollama.Tool, params map[string]any) *gollama.ToolResult {
		res, err := tool.Call(context.Background(), params)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	// Without another session's changes the review runs as before.
	if res := review(spawnReviewers(dB), map[string]any{"task_id": "0001"}); res.IsError || turner.i != 1 {
		t.Fatalf("clean snapshot review: %s (turns %d)", res.Content, turner.i)
	}

	if res := doA("Edit", `{"file_path":"shared.go","old_string":"package shared","new_string":"package shared // A"}`); res.IsError {
		t.Fatalf("A edit: %s", res.Content)
	}
	if res := doB("Edit", `{"file_path":"shared.go","old_string":"// A","new_string":"// A and B"}`); res.IsError {
		t.Fatalf("B edit: %s", res.Content)
	}
	for name, tool := range map[string]*gollama.Tool{"spawn_reviewers": spawnReviewers(dB), "re_review": reReview(dB)} {
		res := review(tool, map[string]any{"task_id": "0001"})
		if !res.IsError || !strings.Contains(res.Content, "shared.go") || !strings.Contains(res.Content, "another session") ||
			!strings.Contains(res.Content, "workstream") || strings.Contains(res.Content, "b.go") || turner.i != 1 {
			t.Fatalf("%s did not warn before reviewing another session's changes: %s (turns %d)", name, res.Content, turner.i)
		}
	}
	if res := review(reReview(dB), map[string]any{"task_id": "0001", "take_other_changes": true}); res.IsError || turner.i != 2 {
		t.Fatalf("re_review taking the file whole: %s (turns %d)", res.Content, turner.i)
	}
	// The choice is per call: the other work in the file may have changed.
	if res := review(reReview(dB), map[string]any{"task_id": "0001"}); !res.IsError || turner.i != 2 {
		t.Fatalf("re_review without the opt-in did not warn again: %s (turns %d)", res.Content, turner.i)
	}
}

// A code file already dirty before the session, then edited by it, is named
// too; bookkeeping files are not.
func TestReviewWarnsBeforeSnapshotWithPreexistingChanges(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "user.go"), []byte("package user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "-A")
	gitRun(t, ws, "commit", "-m", "seed")
	if err := os.WriteFile(filepath.Join(ws, "user.go"), []byte("package user // user's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.CreateWithStatus("task", "## Work log\n", 1, nil, nil, docs.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	d := &Deps{Workspace: ws, Repo: repo, Baseline: repo.OpenBaseline(), Docs: store,
		Emitter: event.NewEmitter(&captureRec{}, "coordinator"), Asker: noopAsker{}}
	if err := os.WriteFile(filepath.Join(ws, "user.go"), []byte("package user // user's work, then mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendWorkLog("0001", "progress"); err != nil {
		t.Fatal(err)
	}
	built := d.reviewDiff("0001")
	if built.Err != nil {
		t.Fatal(built.Err)
	}
	note := reviewGate("spawn_reviewers", built, false)
	if !strings.Contains(note, "Already dirty before this session (1): user.go") || strings.Contains(note, "backlog") {
		t.Fatalf("preexisting note = %q", note)
	}
}

// A file staged before the session whose worktree copy was since restored is
// not in the snapshot, so it is not named.
func TestReviewGateIgnoresPathsOutsideSnapshot(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ws, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store := docs.NewStore(ws)
	if _, err := store.CreateWithStatus("task", "## Work log\n", 1, nil, nil, docs.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	write("user.go", "package user\n")
	gitRun(t, ws, "add", "-A")
	gitRun(t, ws, "commit", "-m", "seed")
	write("user.go", "package user // staged\n")
	gitRun(t, ws, "add", "user.go")
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	d := &Deps{Workspace: ws, Repo: repo, Baseline: repo.OpenBaseline(), Docs: store,
		Emitter: event.NewEmitter(&captureRec{}, "coordinator"), Asker: noopAsker{}}
	write("user.go", "package user\n")
	write("mine.go", "package user // mine\n")
	built := d.reviewDiff("0001")
	if built.Err != nil {
		t.Fatal(built.Err)
	}
	if note := reviewGate("spawn_reviewers", built, false); note != "" {
		t.Fatalf("scope %v warned about a path outside it: %s", built.Paths, note)
	}
}
