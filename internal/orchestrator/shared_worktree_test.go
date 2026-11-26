package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/jobs"
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
	commit := func(tool *gollama.Tool, taskID, msg string, paths ...string) *gollama.ToolResult {
		params := map[string]any{"task_id": taskID, "message": msg, "outcome": "Done."}
		if len(paths) > 0 {
			list := make([]any, len(paths))
			for i, path := range paths {
				list[i] = path
			}
			params["paths"] = list
		}
		res, err := tool.Call(context.Background(), params)
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

	// shared.go holds A's uncommitted change too, and A is live: B's commit is
	// refused with the alternatives, and the task is left as it was.
	head := gitRun(t, ws, "rev-parse", "HEAD")
	res := commit(commitB, "0002", "session B work")
	if !res.IsError || !strings.Contains(res.Content, "shared.go") || !strings.Contains(res.Content, "name them in paths") ||
		!strings.Contains(res.Content, "workstream") || !strings.Contains(res.Content, "wait for the other session") {
		t.Fatalf("B commit of a file live A also changed was not refused with the alternatives: %s", res.Content)
	}
	if got := gitRun(t, ws, "rev-parse", "HEAD"); got != head {
		t.Fatalf("refused commit moved HEAD")
	}
	if task, err := store.Get("0002"); err != nil || task.Status == docs.StatusDone {
		t.Fatalf("refused commit left task 0002 = %+v, %v", task, err)
	}
	// Naming it in paths takes it whole, deliberately.
	if res := commit(commitB, "0002", "session B work, with A's shared.go change", "shared.go"); res.IsError {
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

// A chat session's commit of a file another session also changed is refused
// while that session is live, and taken whole, with a note, once it is not.
func TestCommitOfFileAnotherSessionChangedDependsOnLiveness(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "shared.go"), []byte("package shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "-A")
	gitRun(t, ws, "commit", "-m", "seed")
	store := docs.NewStore(ws)
	ownership := workspacelease.NewService()
	live := map[string]bool{"s_a": true, "s_b": true}
	ownership.SetLiveScopes(func(scope string) bool { return live[scope] })
	session := func(id string) func(name, args string) *gollama.ToolResult {
		r, err := git.Open(ws)
		if err != nil {
			t.Fatal(err)
		}
		d := &Deps{
			Workspace: ws, Repo: r, Baseline: r.OpenBaseline(), Docs: store,
			Emitter: event.NewEmitter(&captureRec{}, "coordinator"), Asker: noopAsker{},
			Ownership: ownership, CoordinatorToken: ownership.NewScopedToken(id, "session "+id+" coordinator"),
		}
		reg, _ := BuildMode("chat", d, false)
		return func(name, args string) *gollama.ToolResult {
			return reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: name, Arguments: args}})
		}
	}
	doA, doB := session("s_a"), session("s_b")
	if res := doA("Edit", `{"file_path":"shared.go","old_string":"package shared","new_string":"package shared // A"}`); res.IsError {
		t.Fatalf("A edit: %s", res.Content)
	}
	if res := doB("Write", `{"file_path":"b.go","content":"package b\n"}`); res.IsError {
		t.Fatalf("B write: %s", res.Content)
	}
	if res := doB("Edit", `{"file_path":"shared.go","old_string":"// A","new_string":"// A and B"}`); res.IsError {
		t.Fatalf("B edit: %s", res.Content)
	}
	head := gitRun(t, ws, "rev-parse", "HEAD")
	if res := doB("commit", `{"message":"B work"}`); !res.IsError || !strings.Contains(res.Content, "shared.go") {
		t.Fatalf("B chat commit of a file live A also changed was not refused: %s", res.Content)
	}
	if got := gitRun(t, ws, "rev-parse", "HEAD"); got != head {
		t.Fatalf("refused commit moved HEAD")
	}

	live["s_a"] = false // A ended; its claim no longer holds the file back
	res := doB("commit", `{"message":"B work"}`)
	if res.IsError || !strings.Contains(res.Content, "also written by another session") {
		t.Fatalf("B commit after A ended: %s", res.Content)
	}
	if got := gitRun(t, ws, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(got, "b.go") || !strings.Contains(got, "shared.go") {
		t.Fatalf("B's commit holds the wrong files:\n%s", got)
	}
}

// A finalization retried by a later session adopts the earlier scope's files
// to rebuild it; that is not naming them, so a file a live peer has since
// changed is still refused.
func TestReanchoredFinalizationStillRefusesLiveSharedFile(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	ownership := workspacelease.NewService()
	ownership.SetLiveScopes(func(scope string) bool { return scope != "original" })
	original := finalizationDeps(ws, repo, baseline, store, &captureRec{})
	original.Ownership = ownership
	original.CoordinatorToken = ownership.NewScopedToken("original", "original")
	path := filepath.Join(ws, "shared.go")
	if err := os.WriteFile(path, []byte("package shared // original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ownership.RecordWrite(path, ws, original.CoordinatorToken)
	hook := filepath.Join(ws, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, failed := callCommit(t, original); !failed || !strings.Contains(got, "pre-commit hook rejected") {
		t.Fatalf("initial commit = %v, %q", failed, got)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}

	freshRepo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	retry := finalizationDeps(ws, freshRepo, freshRepo.OpenBaseline(), store, &captureRec{})
	retry.Ownership = ownership
	retry.CoordinatorToken = ownership.NewScopedToken("retry", "retry")
	if err := os.WriteFile(path, []byte("package shared // original and retry\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ownership.RecordWrite(path, ws, retry.CoordinatorToken)
	if err := os.WriteFile(path, []byte("package shared // original and retry and peer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ownership.RecordWrite(path, ws, ownership.NewScopedToken("peer", "live peer"))
	if got, failed := callCommit(t, retry); !failed || !strings.Contains(got, "name them in paths") {
		t.Fatalf("retry was not refused: error=%v result=%q", failed, got)
	}
	if err := exec.Command("git", "-C", ws, "cat-file", "-e", "HEAD:shared.go").Run(); err == nil {
		t.Fatal("refused retry committed shared.go")
	}
}

// A live session's shell edit of a file another session claims records an
// overlap claim: the shell's session does not take the file, and the
// claimant's commit is refused while it is live.
func TestShellOverlapRefusesClaimantsCommit(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "shared.go"), []byte("package shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "-A")
	gitRun(t, ws, "commit", "-m", "seed")
	ownership := workspacelease.NewService()
	session := func(id string) (*Deps, func(string, string) *gollama.ToolResult) {
		repo, err := git.Open(ws)
		if err != nil {
			t.Fatal(err)
		}
		d := finalizationDeps(ws, repo, repo.OpenBaseline(), docs.NewStore(ws), &captureRec{})
		d.Asker = noopAsker{}
		d.Ownership = ownership
		d.CoordinatorToken = ownership.NewScopedToken(id, id)
		reg, _ := BuildMode("chat", d, false)
		return d, func(name, args string) *gollama.ToolResult {
			return reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: name, Arguments: args}})
		}
	}
	_, a := session("a")
	dB, b := session("b")
	if got := a("Edit", `{"file_path":"shared.go","old_string":"package shared","new_string":"package shared // A"}`); got.IsError {
		t.Fatalf("A Edit: %s", got.Content)
	}
	if got := b("Bash", `{"command":"echo 'var b = 1' >> shared.go"}`); got.IsError {
		t.Fatalf("B Bash: %s", got.Content)
	}
	if changes, err := dB.sessionChangeset(); err != nil || len(changes.Paths) != 0 {
		t.Fatalf("B's changeset took A's file: %+v, %v", changes, err)
	}
	if got := a("commit", `{"message":"A work"}`); !got.IsError || !strings.Contains(got.Content, "name them in paths") {
		t.Fatalf("A's commit was not refused: %s", got.Content)
	}
	if got := a("commit", `{"message":"A work with B's shell line","paths":["shared.go"]}`); got.IsError {
		t.Fatalf("A's commit naming shared.go: %s", got.Content)
	}
}

// A shell command that did not touch a file gets no overlap claim on it when
// another session's write of the file was recorded while it ran, even though
// an older (ended) session's claim predates the command.
func TestShellWithoutTheWriteDoesNotBlockConcurrentlyWrittenFile(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "shared.go"), []byte("package shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "-A")
	gitRun(t, ws, "commit", "-m", "seed")
	ownership := workspacelease.NewService()
	ownership.SetLiveScopes(func(scope string) bool { return scope != "stopped" })
	session := func(id string) func(string, string) *gollama.ToolResult {
		repo, err := git.Open(ws)
		if err != nil {
			t.Fatal(err)
		}
		d := finalizationDeps(ws, repo, repo.OpenBaseline(), docs.NewStore(ws), &captureRec{})
		d.Asker = noopAsker{}
		d.Ownership = ownership
		d.CoordinatorToken = ownership.NewScopedToken(id, id)
		reg, _ := BuildMode("chat", d, false)
		return func(name, args string) *gollama.ToolResult {
			return reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: name, Arguments: args}})
		}
	}
	a, b, stopped := session("a"), session("b"), session("stopped")
	if got := stopped("Edit", `{"file_path":"shared.go","old_string":"package shared","new_string":"package shared // past work"}`); got.IsError {
		t.Fatalf("past edit: %s", got.Content)
	}
	if got := a("Edit", `{"file_path":"shared.go","old_string":"past work","new_string":"past work and A"}`); got.IsError {
		t.Fatalf("A first Edit: %s", got.Content)
	}
	ready, cont := filepath.Join(ws, ".git", "test-ready"), filepath.Join(ws, ".git", "test-continue")
	done := make(chan *gollama.ToolResult, 1)
	go func() {
		done <- b("Bash", `{"command":"touch .git/test-ready; while test ! -f .git/test-continue; do sleep 0.05; done"}`)
	}()
	defer os.WriteFile(cont, nil, 0o644)
	waitFor(t, func() bool { _, err := os.Stat(ready); return err == nil })
	if got := a("Edit", `{"file_path":"shared.go","old_string":"past work and A","new_string":"past work and second A edit"}`); got.IsError {
		t.Fatalf("A second Edit: %s", got.Content)
	}
	if err := os.WriteFile(cont, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got.IsError {
		t.Fatalf("B Bash: %s", got.Content)
	}
	if got := a("commit", `{"message":"A work, including past work"}`); got.IsError {
		t.Fatalf("commit refused though B never edited the file; claims=%v result=%s", ownership.Claims(ws)["shared.go"], got.Content)
	}
}

// Two sessions' shells overlap, each writing its own file: neither is claimed
// by whichever shell finished first. Both paths are unattributed: in neither
// session's changeset, listed in both manifests, blocking neither commit, and
// each session commits its own file by naming it. No mode offers release_claim.
func TestOverlappingShellsLeaveUnattributedPathsOptIn(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "-A")
	gitRun(t, ws, "commit", "-m", "seed")
	ownership := workspacelease.NewService()
	ownership.SetLiveScopes(func(string) bool { return true })
	type session struct {
		d    *Deps
		call func(string, string) *gollama.ToolResult
	}
	open := func(id string) session {
		repo, err := git.Open(ws)
		if err != nil {
			t.Fatal(err)
		}
		d := finalizationDeps(ws, repo, repo.OpenBaseline(), docs.NewStore(ws), &captureRec{})
		d.Asker = noopAsker{}
		d.Ownership = ownership
		d.CoordinatorToken = ownership.NewScopedToken(id, id)
		reg, _ := BuildMode("chat", d, false)
		return session{d, func(name, args string) *gollama.ToolResult {
			return reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: name, Arguments: args}})
		}}
	}
	a, b := open("s_a"), open("s_b")
	// A starts, B starts and writes, A writes and finishes first, then B.
	results := make(chan *gollama.ToolResult, 2)
	go func() {
		results <- a.call("Bash", `{"command":"touch .git/a-started; while test ! -f .git/b-wrote; do sleep 0.02; done; echo a > gen_a.txt; touch .git/a-done"}`)
	}()
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(ws, ".git", "a-started")); return err == nil })
	go func() {
		results <- b.call("Bash", `{"command":"echo b > gen_b.txt; touch .git/b-wrote; while test ! -f .git/a-done; do sleep 0.02; done; sleep 0.2"}`)
	}()
	for range 2 {
		if got := <-results; got.IsError || !strings.Contains(got.Content, "left unattributed") || strings.Contains(got.Content, "release_claim") {
			t.Fatalf("overlapping Bash result: %s", got.Content)
		}
	}
	if claims := ownership.Claims(ws); len(claims) != 0 {
		t.Fatalf("claims = %v", claims)
	}
	for _, s := range []session{a, b} {
		changes, err := s.d.sessionChangeset()
		if err != nil {
			t.Fatal(err)
		}
		if len(changes.Paths) != 0 || strings.Join(changes.Unattributed, ",") != "gen_a.txt,gen_b.txt" {
			t.Fatalf("%s changeset paths=%v unattributed=%v", s.d.CoordinatorToken.Scope(), changes.Paths, changes.Unattributed)
		}
		if note := attributionNote(changes); !strings.Contains(note, "UNATTRIBUTED") || strings.Contains(note, "release_claim") {
			t.Fatalf("manifest does not list unattributed paths: %s", note)
		}
	}
	if got := a.call("commit", `{"message":"A work"}`); !got.IsError || !strings.Contains(got.Content, "unattributed") {
		t.Fatalf("A's default commit did not point at the unattributed paths: %s", got.Content)
	}
	if got := a.call("release_claim", `{"paths":["gen_b.txt"],"reason":"B's output"}`); !got.IsError {
		t.Fatalf("release_claim is still offered: %s", got.Content)
	}
	if got := a.call("commit", `{"message":"A work","paths":["gen_a.txt"]}`); got.IsError {
		t.Fatalf("A's commit naming its file: %s", got.Content)
	}
	if got := gitRun(t, ws, "show", "--name-only", "--format=", "HEAD"); strings.TrimSpace(got) != "gen_a.txt" {
		t.Fatalf("A committed %q", got)
	}
	if got := b.call("commit", `{"message":"B work","paths":["gen_b.txt"]}`); got.IsError {
		t.Fatalf("B's commit naming its file: %s", got.Content)
	}
	if got := gitRun(t, ws, "show", "--name-only", "--format=", "HEAD"); strings.TrimSpace(got) != "gen_b.txt" {
		t.Fatalf("B committed %q", got)
	}
	if got := ownership.Unattributed(ws); len(got) != 0 {
		t.Fatalf("unattributed after both commits = %v", got)
	}
}

// claimLifecycleFixture is an empty committed repository and an opener for
// chat sessions sharing one ownership service in it.
func claimLifecycleFixture(t *testing.T) (string, *workspacelease.Service, func(string) (*Deps, func(string, string) *gollama.ToolResult)) {
	t.Helper()
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "commit", "--allow-empty", "-m", "seed")
	ownership := workspacelease.NewService()
	ownership.SetLiveScopes(func(string) bool { return true })
	return ws, ownership, func(scope string) (*Deps, func(string, string) *gollama.ToolResult) {
		repo, err := git.Open(ws)
		if err != nil {
			t.Fatal(err)
		}
		d := finalizationDeps(ws, repo, repo.OpenBaseline(), docs.NewStore(ws), &captureRec{})
		d.Asker, d.Ownership, d.CoordinatorToken = noopAsker{}, ownership, ownership.NewScopedToken(scope, scope)
		reg, _ := BuildMode("chat", d, false)
		return d, func(name, args string) *gollama.ToolResult {
			return reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: name, Arguments: args}})
		}
	}
}

// An unattributed path is retired once it is seen clean, so a later edit is
// attributed afresh rather than held out.
func TestUnattributedPathRetiresWhenReverted(t *testing.T) {
	ws, ownership, open := claimLifecycleFixture(t)
	d, _ := open("s_a")
	file := filepath.Join(ws, "gen.txt")
	if err := os.WriteFile(file, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ownership.RecordUnattributed(ws, []string{"gen.txt"})
	if changes, err := d.sessionChangeset(); err != nil || len(changes.Paths) != 0 || strings.Join(changes.Unattributed, ",") != "gen.txt" {
		t.Fatalf("changeset with an unattributed path: %+v, %v", changes, err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sessionChangeset(); err != nil {
		t.Fatal(err)
	}
	if got := ownership.Unattributed(ws); len(got) != 0 {
		t.Fatalf("unattributed after the revert = %v", got)
	}
	if err := os.WriteFile(file, []byte("later hand edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changes, err := d.sessionChangeset(); err != nil || strings.Join(changes.Paths, ",") != "gen.txt" {
		t.Fatalf("later edit after the mark was retired: %+v, %v", changes, err)
	}
}

// A short foreground write while another session's background job runs is
// not claimed by the command's session: it is left out of that session's
// default commit, and the session commits it by naming it, unrefused, while
// the job still runs.
func TestShortShellWriteDuringAnotherSessionsJobIsCommittedByName(t *testing.T) {
	ws, ownership, open := claimLifecycleFixture(t)
	_, a := open("s_a")
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	dB := finalizationDeps(ws, repo, repo.OpenBaseline(), docs.NewStore(ws), &captureRec{})
	dB.Asker, dB.Ownership, dB.CoordinatorToken = noopAsker{}, ownership, ownership.NewScopedToken("s_b", "s_b")
	dB.Jobs = jobs.NewRegistry()
	defer dB.Jobs.KillAll()
	regB, _ := BuildMode("chat", dB, false)
	started, stop := filepath.Join(ws, ".git", "job-started"), filepath.Join(ws, ".git", "job-stop")
	defer os.WriteFile(stop, nil, 0o644)
	if got := regB.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: "Bash",
		Arguments: `{"command":"touch .git/job-started; while test ! -f .git/job-stop; do sleep 0.02; done","run_in_background":true}`}}); got.IsError {
		t.Fatalf("B background Bash: %s", got.Content)
	}
	waitFor(t, func() bool { _, err := os.Stat(started); return err == nil })
	if got := a("Bash", `{"command":"echo a > a.txt"}`); got.IsError || !strings.Contains(got.Content, "left unattributed (1): a.txt") {
		t.Fatalf("A Bash: %s", got.Content)
	}
	if claims := ownership.ClaimDetails(ws)["a.txt"]; len(claims) != 0 {
		t.Fatalf("a.txt claims = %+v", claims)
	}
	if got := a("commit", `{"message":"A work"}`); !got.IsError {
		t.Fatalf("A's default commit took the unattributed file: %s", got.Content)
	}
	if got := a("commit", `{"message":"A work","paths":["a.txt"]}`); got.IsError {
		t.Fatalf("A's commit naming a.txt while B's job runs: %s", got.Content)
	}
	if got := gitRun(t, ws, "show", "--name-only", "--format=", "HEAD"); strings.TrimSpace(got) != "a.txt" {
		t.Fatalf("A committed %q", got)
	}
}

// A path a third session claimed before two other sessions' shells overlapped
// stays that session's alone: the overlapping shells, one of which changed it,
// record no overlap claim, so its commit is not refused on a guess.
func TestOverlappingShellsDoNotBlockAnEarlierClaimant(t *testing.T) {
	ws, ownership, open := claimLifecycleFixture(t)
	_, c := open("s_c")
	_, a := open("s_a")
	_, b := open("s_b")
	if got := c("Write", `{"file_path":"f.txt","content":"C owned this before either shell\n"}`); got.IsError {
		t.Fatal(got.Content)
	}
	marker := filepath.Join(ws, ".git", "b-done")
	defer os.WriteFile(marker, nil, 0o644)
	done := make(chan *gollama.ToolResult, 1)
	go func() {
		done <- a("Bash", `{"command":"touch .git/a-ready; while test ! -f .git/b-done; do sleep 0.02; done; cat f.txt"}`)
	}()
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(ws, ".git", "a-ready")); return err == nil })
	if got := b("Bash", `{"command":"echo B >> f.txt"}`); got.IsError {
		t.Fatal(got.Content)
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got.IsError {
		t.Fatal(got.Content)
	}
	if got := ownership.Claims(ws)["f.txt"]; strings.Join(got, ",") != "s_c" {
		t.Fatalf("f.txt claims = %v", got)
	}
	if got := c("commit", `{"message":"C work"}`); got.IsError {
		t.Fatalf("C's commit was refused: %s", got.Content)
	}
}

// An unattributed path that was already dirty at the baseline and changed
// since is left out and listed as unattributed, not as another session's, so
// the default commit points at it.
func TestBaselineDirtyUnattributedPathIsListed(t *testing.T) {
	ws, ownership, open := claimLifecycleFixture(t)
	path := filepath.Join(ws, "already-dirty.txt")
	if err := os.WriteFile(path, []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, call := open("s_a")
	if err := os.WriteFile(path, []byte("changed after baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ownership.RecordUnattributed(ws, []string{"already-dirty.txt"})
	changes, err := d.sessionChangeset()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Paths) != 0 || len(changes.Deferred) != 0 || strings.Join(changes.Unattributed, ",") != "already-dirty.txt" {
		t.Fatalf("paths=%v deferred=%v unattributed=%v", changes.Paths, changes.Deferred, changes.Unattributed)
	}
	if got := call("commit", `{"message":"A work"}`); !got.IsError || !strings.Contains(got.Content, "unattributed") || !strings.Contains(got.Content, "already-dirty.txt") {
		t.Fatalf("default commit lacks the opt-in guidance: %s", got.Content)
	}
	if got := call("commit", `{"message":"A work","paths":["already-dirty.txt"]}`); got.IsError {
		t.Fatalf("commit naming the path: %s", got.Content)
	}
}
