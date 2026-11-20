package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// bookkeepingSession is a session whose own docs store attributes its backlog
// and memory writes to it, as session.Manager wires them.
func bookkeepingSession(t *testing.T, ws string, ownership *workspacelease.Service, id string) (*Deps, *docs.Store) {
	t.Helper()
	r, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	d := &Deps{
		Workspace: ws, Repo: r, Baseline: r.OpenBaseline(), Docs: store,
		Emitter: event.NewEmitter(&captureRec{}, "coordinator"), Asker: noopAsker{},
		Ownership: ownership, CoordinatorToken: ownership.NewScopedToken(id, "session "+id+" coordinator"),
		WorkImplementation: "direct",
	}
	store.SetWriteObserver(d.RecordBookkeeping)
	return d, store
}

func headFile(t *testing.T, ws, rel string) string {
	t.Helper()
	return gitRun(t, ws, "show", "HEAD:"+rel)
}

// Two live sessions edit the same task files and memory.md. Each commit holds
// only that session's own lines; the other's stay uncommitted in the worktree
// until it commits them.
func TestCommitsTakeOnlyTheSessionsOwnBookkeepingLines(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	seed := docs.NewStore(ws)
	for _, title := range []string{"A's task", "B's task", "shared umbrella"} {
		if _, err := seed.Create(title, "## Acceptance\n- done\n\n## Work log\n", 1, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := seed.AppendMemory("seed note", "lesson"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "-A")
	gitRun(t, ws, "commit", "-m", "seed")
	taskPath := func(id string) string {
		task, err := seed.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(ws, task.Path)
		return filepath.ToSlash(rel)
	}

	ownership := workspacelease.NewService()
	dA, storeA := bookkeepingSession(t, ws, ownership, "s_a")
	dB, storeB := bookkeepingSession(t, ws, ownership, "s_b")
	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(storeA.Update("0001", func(task *docs.Task) { task.Status = docs.StatusInReview }))
	must(storeB.Update("0002", func(task *docs.Task) { task.Status = docs.StatusInReview }))
	// B edits A's task file while A works on it.
	must(storeB.Update("0001", func(task *docs.Task) { task.Priority = 5 }))
	must(storeA.AppendWorkLog("0003", "A progress"))
	must(storeB.AppendWorkLog("0003", "B progress"))
	must(storeA.AppendMemory("A lesson", "lesson"))
	must(storeB.AppendMemory("B lesson", "lesson"))
	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := commitTool(dA).Call(context.Background(), map[string]any{"task_id": "0001", "message": "A work", "outcome": "Done."})
	if err != nil || res.IsError {
		t.Fatalf("A commit: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, "committed only this session's lines") {
		t.Fatalf("A's commit result does not report the partial files:\n%s", res.Content)
	}
	if got := headFile(t, ws, taskPath("0001")); !strings.Contains(got, "status: done") || strings.Contains(got, "priority: 5") {
		t.Fatalf("A's task in HEAD carries B's edit or lacks A's:\n%s", got)
	}
	if got := headFile(t, ws, taskPath("0003")); !strings.Contains(got, "A progress") || strings.Contains(got, "B progress") {
		t.Fatalf("shared task in HEAD:\n%s", got)
	}
	if got := headFile(t, ws, "memory.md"); !strings.Contains(got, "A lesson") || strings.Contains(got, "B lesson") {
		t.Fatalf("memory in HEAD:\n%s", got)
	}
	if got := gitRun(t, ws, "show", "--name-only", "--format=", "HEAD"); strings.Contains(got, taskPath("0002")) {
		t.Fatalf("A's commit took B's task file:\n%s", got)
	}
	worktree, _ := os.ReadFile(filepath.Join(ws, taskPath("0001")))
	if !strings.Contains(string(worktree), "priority: 5") || !strings.Contains(string(worktree), "status: done") {
		t.Fatalf("worktree lost an edit:\n%s", worktree)
	}

	res, err = commitTool(dB).Call(context.Background(), map[string]any{"task_id": "0002", "message": "B work", "outcome": "Done."})
	if err != nil || res.IsError {
		t.Fatalf("B commit: %v %+v", err, res)
	}
	if got := headFile(t, ws, taskPath("0001")); !strings.Contains(got, "status: done") || !strings.Contains(got, "priority: 5") {
		t.Fatalf("B's edit of A's task not committed on top:\n%s", got)
	}
	if got := headFile(t, ws, "memory.md"); !strings.Contains(got, "A lesson") || !strings.Contains(got, "B lesson") {
		t.Fatalf("memory in HEAD after B:\n%s", got)
	}
	if status := gitRun(t, ws, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("worktree after both commits:\n%s", status)
	}
}

// A watch run commits its own backlog notes when it reports, leaving another
// live session's edit of the same task file alone.
func TestWatchReportCommitsTheRunsOwnBookkeeping(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	seed := docs.NewStore(ws)
	task, err := seed.Create("watched work", "## Work log\n", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "-A")
	gitRun(t, ws, "commit", "-m", "seed")
	rel, _ := filepath.Rel(ws, task.Path)

	ownership := workspacelease.NewService()
	watch, _ := bookkeepingSession(t, ws, ownership, "s_watch")
	_, other := bookkeepingSession(t, ws, ownership, "s_other")
	if _, err := other.Update(task.ID, func(task *docs.Task) { task.Priority = 4 }); err != nil {
		t.Fatal(err)
	}
	// Watch runs have no backlog tools; they append notes with Edit.
	reg, _ := BuildMode("watch", watch, true)
	args, _ := json.Marshal(map[string]string{"file_path": task.Path, "old_string": "## Work log\n", "new_string": "## Work log\n- watch: build still running\n"})
	if res := reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: "Edit", Arguments: string(args)}}); res.IsError {
		t.Fatalf("Edit: %s", res.Content)
	}
	if err := os.WriteFile(filepath.Join(ws, "scratch.go"), []byte("package scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	watch.WatchReport = func(WatchReport) (string, error) { return "recorded", nil }
	res, err := watchReport(watch).Call(context.Background(), map[string]any{"status": WatchOK, "summary": "Build still running", "notes": ""})
	if err != nil || res.IsError || !strings.Contains(res.Content, "Committed this run's backlog/memory edits") {
		t.Fatalf("watch_report: %v %+v", err, res)
	}
	got := headFile(t, ws, filepath.ToSlash(rel))
	if !strings.Contains(got, "watch: build still running") || strings.Contains(got, "priority: 4") {
		t.Fatalf("watch commit holds the wrong lines:\n%s", got)
	}
	if files := gitRun(t, ws, "show", "--name-only", "--format=", "HEAD"); strings.TrimSpace(files) != filepath.ToSlash(rel) {
		t.Fatalf("watch commit holds the wrong files:\n%s", files)
	}
}

func seededTask(t *testing.T, ws string, titles ...string) []*docs.Task {
	t.Helper()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	seed := docs.NewStore(ws)
	var tasks []*docs.Task
	for _, title := range titles {
		task, err := seed.Create(title, "## Work log\n", 1, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	gitRun(t, ws, "add", "-A")
	gitRun(t, ws, "commit", "-m", "seed")
	return tasks
}

// B adds a dependency under the key A's uncommitted edit introduced: B's
// commit keeps the key well-formed without A's dependency or A's progress.
func TestPartialTaskVersionStaysWellFormed(t *testing.T) {
	ws := t.TempDir()
	tasks := seededTask(t, ws, "shared", "first dependency", "second dependency")
	ownership := workspacelease.NewService()
	_, a := bookkeepingSession(t, ws, ownership, "s_a")
	dB, b := bookkeepingSession(t, ws, ownership, "s_b")
	if _, err := a.Update(tasks[0].ID, func(task *docs.Task) { task.DependsOn = []string{tasks[1].ID} }); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AppendWorkLog(tasks[0].ID, "unrelated A progress"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Update(tasks[0].ID, func(task *docs.Task) { task.DependsOn = append(task.DependsOn, tasks[2].ID) }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commitSessionChanges(context.Background(), dB, "add dependency", nil); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(ws, tasks[0].Path)
	committed := headFile(t, ws, filepath.ToSlash(rel))
	if !docs.ValidTaskContent([]byte(committed)) || !strings.Contains(committed, `"0003"`) ||
		strings.Contains(committed, `"0002"`) || strings.Contains(committed, "unrelated A progress") {
		t.Fatalf("B's commit:\n%s", committed)
	}
}

// A note filed under a memory section another session created lands in that
// section, without that session's own note.
func TestCommittedMemoryNoteKeepsItsSection(t *testing.T) {
	ws := t.TempDir()
	seededTask(t, ws, "shared")
	if _, err := docs.NewStore(ws).AppendMemory("seed lesson", "lesson"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "memory.md")
	gitRun(t, ws, "commit", "-m", "seed memory")
	ownership := workspacelease.NewService()
	_, a := bookkeepingSession(t, ws, ownership, "s_a")
	dB, b := bookkeepingSession(t, ws, ownership, "s_b")
	if _, err := a.AppendMemory("A gotcha", "gotcha"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AppendMemory("B gotcha", "gotcha"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commitSessionChanges(context.Background(), dB, "B note", nil); err != nil {
		t.Fatal(err)
	}
	got := headFile(t, ws, "memory.md")
	if i := strings.Index(got, "## Codebase gotchas"); i < 0 || !strings.Contains(got[i:], "B gotcha") || strings.Contains(got, "A gotcha") {
		t.Fatalf("memory in HEAD:\n%s", got)
	}
}

// A line commit refuses to discard an independently staged removal.
func TestLineCommitRefusesToDiscardAStagedRemoval(t *testing.T) {
	ws := t.TempDir()
	tasks := seededTask(t, ws, "shared")
	ownership := workspacelease.NewService()
	dA, a := bookkeepingSession(t, ws, ownership, "s_a")
	_, b := bookkeepingSession(t, ws, ownership, "s_b")
	rel, _ := filepath.Rel(ws, tasks[0].Path)
	gitRun(t, ws, "rm", "-q", "--cached", "--", rel)
	if _, err := a.AppendWorkLog(tasks[0].ID, "A note"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AppendWorkLog(tasks[0].ID, "B note"); err != nil {
		t.Fatal(err)
	}
	if sha, _, err := commitSessionChanges(context.Background(), dA, "notes", nil); err == nil || !strings.Contains(err.Error(), "staged") {
		t.Fatalf("commit %s: err = %v; want a staged-content refusal", sha, err)
	}
}

// A watch run whose notes cannot be committed is told so and is not ended,
// unless it reports an alert naming them.
func TestWatchReportSurfacesAFailedNotesCommit(t *testing.T) {
	ws := t.TempDir()
	tasks := seededTask(t, ws, "watched work")
	ownership := workspacelease.NewService()
	watch, store := bookkeepingSession(t, ws, ownership, "s_watch")
	recorded := ""
	watch.WatchReport = func(r WatchReport) (string, error) { recorded = r.Summary; return "recorded", nil }
	if _, err := store.AppendWorkLog(tasks[0].ID, "watch note"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := watchReport(watch).Call(context.Background(), map[string]any{"status": WatchOK, "summary": "healthy", "notes": ""})
	if err != nil || !res.IsError || recorded != "" {
		t.Fatalf("failed notes commit: %v %+v (recorded %q)", err, res, recorded)
	}
	res, err = watchReport(watch).Call(context.Background(), map[string]any{"status": WatchAlert, "summary": "notes stuck", "notes": ""})
	if err != nil || res.IsError || !strings.Contains(recorded, "uncommitted") {
		t.Fatalf("alert report: %v %+v (recorded %q)", err, res, recorded)
	}
}

// With line-ending conversion, a session's partial commit still leaves the
// other session's pending note attributed to it afterwards.
func TestLineAttributionSurvivesLineEndingConversion(t *testing.T) {
	ws := t.TempDir()
	seededTask(t, ws, "shared")
	gitRun(t, ws, "config", "core.autocrlf", "true")
	path := filepath.Join(ws, "memory.md")
	if err := os.WriteFile(path, []byte("# Memory\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "memory.md")
	gitRun(t, ws, "commit", "-m", "seed memory")
	ownership := workspacelease.NewService()
	a, _ := bookkeepingSession(t, ws, ownership, "s_a")
	b, _ := bookkeepingSession(t, ws, ownership, "s_b")
	c, _ := bookkeepingSession(t, ws, ownership, "s_c")
	appendNote := func(d *Deps, note string) {
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		after := append(append([]byte(nil), before...), []byte(note+"\r\n")...)
		if err := os.WriteFile(path, after, 0o644); err != nil {
			t.Fatal(err)
		}
		d.RecordBookkeeping(path, before, after)
	}
	appendNote(a, "A note")
	appendNote(b, "B note")
	if _, _, err := commitSessionChanges(context.Background(), a, "A note", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "unrelated.txt"), []byte("unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commitSessionChanges(context.Background(), c, "unrelated", nil); err != nil {
		t.Fatal(err)
	}
	if got := headFile(t, ws, "memory.md"); !strings.Contains(got, "A note") || strings.Contains(got, "B note") {
		t.Fatalf("memory in HEAD:\n%s", got)
	}
}

// Keeping the key another session's hunk introduced does not take that hunk's
// other replacements: an unrelated field it also changed stays as committed.
func TestStructuralParentKeepsUnrelatedFields(t *testing.T) {
	ws := t.TempDir()
	tasks := seededTask(t, ws, "shared", "first dependency", "second dependency")
	raw, err := os.ReadFile(tasks[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	var old []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "updated: ") {
			line = `updated: "2020-01-01"`
		}
		old = append(old, line)
	}
	if err := os.WriteFile(tasks[0].Path, []byte(strings.Join(old, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "commit", "-qam", "older task")
	ownership := workspacelease.NewService()
	_, a := bookkeepingSession(t, ws, ownership, "s_a")
	dB, b := bookkeepingSession(t, ws, ownership, "s_b")
	if _, err := a.Update(tasks[0].ID, func(task *docs.Task) { task.DependsOn = []string{tasks[1].ID} }); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Update(tasks[0].ID, func(task *docs.Task) { task.DependsOn = append(task.DependsOn, tasks[2].ID) }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commitSessionChanges(context.Background(), dB, "B dependency", nil); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(ws, tasks[0].Path)
	if got := headFile(t, ws, filepath.ToSlash(rel)); !strings.Contains(got, "updated: ") || !docs.ValidTaskContent([]byte(got)) {
		t.Fatalf("B's commit:\n%s", got)
	}
}

// Docs-store writes in LF under core.autocrlf=true keep their attribution
// across another session's partial commit.
func TestLFBookkeepingKeepsAttributionUnderAutoCRLF(t *testing.T) {
	ws := t.TempDir()
	seededTask(t, ws, "shared")
	gitRun(t, ws, "config", "core.autocrlf", "true")
	if _, err := docs.NewStore(ws).AppendMemory("seed note", "lesson"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "memory.md")
	gitRun(t, ws, "commit", "-qm", "seed memory")
	ownership := workspacelease.NewService()
	dA, a := bookkeepingSession(t, ws, ownership, "s_a")
	_, b := bookkeepingSession(t, ws, ownership, "s_b")
	dC, _ := bookkeepingSession(t, ws, ownership, "s_c")
	if _, err := a.AppendMemory("A note", "lesson"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AppendMemory("B private note", "lesson"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commitSessionChanges(context.Background(), dA, "A note", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "implementation.txt"), []byte("unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commitSessionChanges(context.Background(), dC, "implementation", nil); err != nil {
		t.Fatal(err)
	}
	if got := headFile(t, ws, "memory.md"); !strings.Contains(got, "A note") || strings.Contains(got, "B private note") {
		t.Fatalf("memory in HEAD:\n%s", got)
	}
}

// A same-session write landing while its commit runs (here, during the
// pre-commit hook) neither rides along nor costs anyone their attribution:
// the published version is folded exactly.
func TestWriteDuringCommitKeepsAttribution(t *testing.T) {
	ws := t.TempDir()
	task := seededTask(t, ws, "shared")[0]
	rel, _ := filepath.Rel(ws, task.Path)
	ownership := workspacelease.NewService()
	dA, a := bookkeepingSession(t, ws, ownership, "s_a")
	_, b := bookkeepingSession(t, ws, ownership, "s_b")
	dC, _ := bookkeepingSession(t, ws, ownership, "s_c")
	if _, err := a.AppendWorkLog(task.ID, "A first note"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AppendWorkLog(task.ID, "B private note"); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(ws, ".git", "hooks")
	ready, release := filepath.Join(hooks, "ready"), filepath.Join(hooks, "release")
	hook := filepath.Join(hooks, "pre-commit")
	script := "#!/bin/sh\n: > '" + ready + "'\nwhile [ ! -f '" + release + "' ]; do sleep 0.01; done\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	committed := make(chan error, 1)
	go func() {
		_, _, err := commitSessionChanges(context.Background(), dA, "A first note", nil)
		committed <- err
	}()
	defer os.WriteFile(release, nil, 0o600)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hook did not start")
		}
	}
	if _, err := a.AppendWorkLog(task.ID, "A second note"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if got := headFile(t, ws, filepath.ToSlash(rel)); !strings.Contains(got, "A first note") || strings.Contains(got, "A second note") || strings.Contains(got, "B private note") {
		t.Fatalf("A's commit:\n%s", got)
	}
	if err := os.WriteFile(filepath.Join(ws, "unrelated.txt"), []byte("implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commitSessionChanges(context.Background(), dC, "unrelated", nil); err != nil {
		t.Fatal(err)
	}
	if got := headFile(t, ws, filepath.ToSlash(rel)); strings.Contains(got, "B private note") || strings.Contains(got, "A second note") {
		t.Fatalf("an unrelated commit swept live sessions' notes:\n%s", got)
	}
}

// A partial commit leaves another session's staged lines staged.
func TestPartialCommitKeepsStagedLines(t *testing.T) {
	ws := t.TempDir()
	task := seededTask(t, ws, "shared")[0]
	rel, _ := filepath.Rel(ws, task.Path)
	ownership := workspacelease.NewService()
	dA, a := bookkeepingSession(t, ws, ownership, "s_a")
	_, b := bookkeepingSession(t, ws, ownership, "s_b")
	if _, err := a.AppendWorkLog(task.ID, "A note"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AppendWorkLog(task.ID, "staged B note"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "--", rel)
	if _, _, err := commitSessionChanges(context.Background(), dA, "A note", nil); err != nil {
		t.Fatal(err)
	}
	if got := headFile(t, ws, filepath.ToSlash(rel)); strings.Contains(got, "staged B note") {
		t.Fatalf("B's note committed:\n%s", got)
	}
	got := gitRun(t, ws, "diff", "--cached", "--", rel)
	if added := addedLines(got); len(added) != 1 || !strings.Contains(added[0], "staged B note") {
		t.Fatalf("staged diff after A's commit:\n%s", got)
	}
}

func addedLines(diff string) []string {
	var out []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			out = append(out, line)
		}
	}
	return out
}

// Retrying a partial commit that already landed keeps the staged lines the
// first publication kept.
func TestPartialCommitRetryKeepsStagedLines(t *testing.T) {
	ws := t.TempDir()
	task := seededTask(t, ws, "shared")[0]
	rel, _ := filepath.Rel(ws, task.Path)
	ownership := workspacelease.NewService()
	dA, a := bookkeepingSession(t, ws, ownership, "s_a")
	_, b := bookkeepingSession(t, ws, ownership, "s_b")
	if _, err := a.AppendWorkLog(task.ID, "A note"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AppendWorkLog(task.ID, "staged B note"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "--", rel)
	changes, err := dA.sessionChangeset()
	if err != nil {
		t.Fatal(err)
	}
	sha, err := dA.Repo.Commit(changes, "A note")
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := dA.Repo.Commit(changes, "A note"); err != nil || retry != sha {
		t.Fatalf("retry = %s, %v; want %s", retry, err, sha)
	}
	if staged := gitRun(t, ws, "diff", "--cached", "--", rel); !strings.Contains(staged, "staged B note") {
		t.Fatalf("retry cleared B's staged note: %q", staged)
	}
}

// Read-only inspections of a session's changes while its commit runs cannot
// displace the version that commit publishes.
func TestReadOnlyInspectionsKeepThePublishedVersion(t *testing.T) {
	ws := t.TempDir()
	task := seededTask(t, ws, "shared")[0]
	rel := filepath.ToSlash(strings.TrimPrefix(task.Path, ws+string(filepath.Separator)))
	ownership := workspacelease.NewService()
	dA, a := bookkeepingSession(t, ws, ownership, "s_a")
	_, b := bookkeepingSession(t, ws, ownership, "s_b")
	dC, _ := bookkeepingSession(t, ws, ownership, "s_c")
	if _, err := a.AppendWorkLog(task.ID, "A first note"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AppendWorkLog(task.ID, "B private note"); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(ws, ".git", "hooks")
	ready, release := filepath.Join(hooks, "ready"), filepath.Join(hooks, "release")
	hook := filepath.Join(hooks, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n: > '"+ready+"'\nwhile [ ! -f '"+release+"' ]; do sleep 0.01; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	committed := make(chan error, 1)
	go func() {
		_, _, err := commitSessionChanges(context.Background(), dA, "A first note", nil)
		committed <- err
	}()
	defer os.WriteFile(release, nil, 0o600)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hook did not start")
		}
	}
	head := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD"))
	lines := BookkeepingLines(ws, docs.NewStore(ws), ownership, "s_a")
	for i := range 10 {
		if _, err := a.AppendWorkLog(task.ID, "A later note "+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := lines(head, rel, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "unrelated.txt"), []byte("implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commitSessionChanges(context.Background(), dC, "unrelated", nil); err != nil {
		t.Fatal(err)
	}
	if got := headFile(t, ws, rel); !strings.Contains(got, "A first note") || strings.Contains(got, "B private note") || strings.Contains(got, "A later note") {
		t.Fatalf("HEAD after an unrelated commit:\n%s", got)
	}
}
