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

// A snapshot that takes whole a code file another session also changed is
// still reviewed: every reviewer round is told which files hold the other work
// and to judge only this task's changes, and the coordinator gets the files
// beside the verdicts.
func TestReviewNotesAnotherSessionsChangesWithoutBlocking(t *testing.T) {
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
		call("submit_review", `{"verdict":"revise","summary":"fix it"}`),
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
	seen := func() string {
		var b strings.Builder
		for _, m := range turner.messages {
			b.WriteString(m.Content + "\n")
		}
		return b.String()
	}
	// Without another session's changes the review runs as before.
	if res := review(spawnReviewers(dB), map[string]any{"task_id": "0001"}); res.IsError || turner.i != 1 ||
		strings.Contains(seen(), "OTHER WORK") || strings.Contains(res.Content, "Other work") {
		t.Fatalf("clean snapshot review: %s (turns %d)", res.Content, turner.i)
	}

	if res := doA("Edit", `{"file_path":"shared.go","old_string":"package shared","new_string":"package shared // A"}`); res.IsError {
		t.Fatalf("A edit: %s", res.Content)
	}
	if res := doB("Edit", `{"file_path":"shared.go","old_string":"// A","new_string":"// A and B"}`); res.IsError {
		t.Fatalf("B edit: %s", res.Content)
	}
	rounds := []struct {
		name   string
		tool   *gollama.Tool
		params map[string]any
	}{
		{"spawn_reviewers", spawnReviewers(dB), map[string]any{"task_id": "0001"}},
		{"retained re_review", reReview(dB), map[string]any{"task_id": "0001"}},
		{"fresh re_review", reReview(dB), map[string]any{"task_id": "0001", "context_mode": "fresh"}},
	}
	for i, round := range rounds {
		res := review(round.tool, round.params)
		if res.IsError || turner.i != i+2 {
			t.Fatalf("%s did not run the reviewer: %s (turns %d)", round.name, res.Content, turner.i)
		}
		// The latest prompt itself carries the note, not only older history.
		var latest string
		for _, m := range turner.messages {
			if m.Role == "user" {
				latest = m.Content
			}
		}
		if !strings.Contains(latest, "OTHER WORK IN THIS SNAPSHOT") || !strings.Contains(latest, "Also written by another session (1): shared.go") ||
			!strings.Contains(latest, "not a finding against this task") {
			t.Fatalf("%s reviewer was not told about the other work:\n%s", round.name, latest)
		}
		if !strings.Contains(res.Content, "Also written by another session (1): shared.go") || !strings.Contains(res.Content, "name the other work") {
			t.Fatalf("%s result does not name the other work for the coordinator: %s", round.name, res.Content)
		}
	}
}

// A code file already dirty before the session, then edited by it, is named
// too; bookkeeping files are not.
func TestReviewNotesPreexistingChanges(t *testing.T) {
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
	note := otherChangesReviewNote(built)
	if !strings.Contains(note, "Already dirty before this session (1): user.go") || strings.Contains(note, "backlog") {
		t.Fatalf("preexisting note = %q", note)
	}
}

// A file staged before the session whose worktree copy was since restored is
// not in the snapshot, so it is not named.
func TestReviewNoteIgnoresPathsOutsideSnapshot(t *testing.T) {
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
	if note := otherChangesReviewNote(built); note != "" {
		t.Fatalf("scope %v warned about a path outside it: %s", built.Paths, note)
	}
}
