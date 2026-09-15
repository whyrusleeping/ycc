package orchestrator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

type failEventRecorder struct {
	mu     sync.Mutex
	failAt event.Type
	failed error
	events []event.Event
}

type failSaveJournal struct {
	inner  finalizationJournal
	failAt int
	saves  int
}

func (j *failSaveJournal) Load() (*finalizationRecord, error) { return j.inner.Load() }

func (j *failSaveJournal) Save(record *finalizationRecord) error {
	j.saves++
	if j.saves == j.failAt {
		return errors.New("journal disk full")
	}
	return j.inner.Save(record)
}

func (r *failEventRecorder) Record(actor string, typ event.Type, data map[string]any) event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed != nil {
		return event.Event{}
	}
	if typ == r.failAt {
		r.failed = errors.New("event disk full")
		return event.Event{}
	}
	ev := event.Event{Seq: len(r.events) + 1, Actor: actor, Type: typ, Data: data}
	r.events = append(r.events, ev)
	return ev
}

func (r *failEventRecorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed
}

func setupFinalizationRepo(t *testing.T) (string, *git.Repo, *git.Baseline, *docs.Store) {
	t.Helper()
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	task, err := store.Create("recover commit", "## Description\n\nKeep intent.\n\n## Acceptance criteria\n\n- works\n\n## Work log\n\n- evidence\n", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(task.ID, func(task *docs.Task) { task.Status = docs.StatusInReview }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "owned.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "-A")
	gitRun(t, ws, "commit", "-m", "seed task")
	// This staged change predates the task baseline and must survive every retry.
	if err := os.WriteFile(filepath.Join(ws, "unrelated.txt"), []byte("user work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, ws, "add", "unrelated.txt")
	repo, err = git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	baseline := repo.OpenBaseline()
	if err := repo.PersistBaseline("finalization-test", baseline); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "owned.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return ws, repo, baseline, store
}

func finalizationDeps(ws string, repo *git.Repo, baseline *git.Baseline, store *docs.Store, recorder event.Recorder) *Deps {
	return &Deps{
		Workspace: ws, Repo: repo, Baseline: baseline, Docs: store,
		Emitter: event.NewEmitter(recorder, "coordinator"),
	}
}

func callCommit(t *testing.T, d *Deps) (string, bool) {
	t.Helper()
	result, err := commitTool(d).Call(context.Background(), map[string]any{
		"task_id": "0001", "message": "finish safely", "outcome": "Implemented and verified recovery.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Content, result.IsError
}

func TestCommitFinalizationRestoresAfterHookFailureAndRetryIsIdempotent(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	hook := filepath.Join(ws, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho policy says no >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := finalizationDeps(ws, repo, baseline, store, &captureRec{})
	before, err := store.Get("0001")
	if err != nil {
		t.Fatal(err)
	}
	if got, failed := callCommit(t, d); !failed || !strings.Contains(got, "policy says no") {
		t.Fatalf("hook failure = error %v, %q", failed, got)
	}
	afterFailure, err := store.Get("0001")
	if err != nil {
		t.Fatal(err)
	}
	if afterFailure.Status != before.Status || afterFailure.Body != before.Body {
		t.Fatalf("failed commit left task completed or compacted: before=%+v after=%+v", before, afterFailure)
	}
	if staged := gitRun(t, ws, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "unrelated.txt" {
		t.Fatalf("failed scoped commit changed unrelated index: %q", staged)
	}
	// Fix both the rejected source and its task evidence. A retry must renew the
	// pre-commit snapshot rather than wedging on the first Completion.Before.
	if err := os.WriteFile(filepath.Join(ws, "owned.txt"), []byte("after hook fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update("0001", func(task *docs.Task) {
		task.Body += "\n- hook rejection fixed and reverified\n"
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if got, failed := callCommit(t, d); failed || !strings.Contains(got, "committed ") {
		t.Fatalf("retry = error %v, %q", failed, got)
	}
	count := strings.TrimSpace(gitRun(t, ws, "rev-list", "--count", "HEAD"))
	completed, err := store.Get("0001")
	if err != nil {
		t.Fatal(err)
	}
	body := completed.Body
	if completed.Status != docs.StatusDone || !strings.Contains(body, "Implemented and verified recovery.") {
		t.Fatalf("retry did not complete task: %+v", completed)
	}
	committedTask := gitRun(t, ws, "show", "HEAD:backlog/0001-recover-commit.md")
	if !strings.Contains(committedTask, "status: done") || !strings.Contains(committedTask, "Implemented and verified recovery.") {
		t.Fatalf("accepted commit does not contain completed outcome:\n%s", committedTask)
	}
	if got := gitRun(t, ws, "show", "HEAD:owned.txt"); got != "after hook fix\n" {
		t.Fatalf("retry committed stale rejected source: %q", got)
	}
	if got, failed := callCommit(t, d); failed || !strings.Contains(got, "committed ") {
		t.Fatalf("already committed retry = error %v, %q", failed, got)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "rev-list", "--count", "HEAD")); got != count {
		t.Fatalf("already committed retry created another commit: before=%s after=%s", count, got)
	}
	again, _ := store.Get("0001")
	if again.Body != body {
		t.Fatal("already committed retry compacted the task again")
	}
	if staged := gitRun(t, ws, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "unrelated.txt" {
		t.Fatalf("successful scoped commit consumed unrelated index: %q", staged)
	}
	if names := gitRun(t, ws, "show", "--pretty=format:", "--name-only", "HEAD"); strings.Contains(names, "unrelated.txt") {
		t.Fatalf("scoped commit included unrelated work:\n%s", names)
	}
}

func TestCommitFinalizationResumesCreatedCommitAfterHeadUpdateFailure(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	branchRef := strings.TrimSpace(gitRun(t, ws, "symbolic-ref", "HEAD"))
	lockPath := filepath.Join(ws, ".git", filepath.FromSlash(branchRef)+".lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("locked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := finalizationDeps(ws, repo, baseline, store, &captureRec{})
	beforeHead := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD"))
	if got, failed := callCommit(t, d); !failed || !strings.Contains(got, "advance HEAD") {
		t.Fatalf("HEAD update failure = error %v, %q", failed, got)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD")); got != beforeHead {
		t.Fatalf("failed HEAD CAS moved HEAD: before=%s after=%s", beforeHead, got)
	}
	if task, _ := store.Get("0001"); task.Status == docs.StatusDone {
		t.Fatalf("uninstalled created commit left task done: %+v", task)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}

	reopened, err := git.OpenExisting(ws)
	if err != nil {
		t.Fatal(err)
	}
	reloadedBaseline, err := reopened.LoadBaseline("finalization-test")
	if err != nil {
		t.Fatal(err)
	}
	restarted := finalizationDeps(ws, reopened, reloadedBaseline, docs.NewStore(ws), &captureRec{})
	if got, failed := callCommit(t, restarted); failed || !strings.Contains(got, "committed ") {
		t.Fatalf("created commit restart retry = error %v, %q", failed, got)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD")); got == beforeHead {
		t.Fatal("created commit retry did not advance HEAD")
	}
}

func TestCommitFinalizationRetainsInstalledCommitUntilIndexPublicationRecovers(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	hook := filepath.Join(ws, ".git", "hooks", "pre-commit")
	script := `#!/bin/sh
set -eu
gitdir=$(git rev-parse --absolute-git-dir)
mv "$gitdir/index" "$gitdir/index.saved"
mkdir "$gitdir/index"
rm "$0"
`
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := finalizationDeps(ws, repo, baseline, store, &captureRec{})
	if got, failed := callCommit(t, d); !failed || !strings.Contains(got, "selected index paths remain pending") {
		t.Fatalf("index publication failure = error %v, %q", failed, got)
	}
	committedHead := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD"))
	if task, _ := store.Get("0001"); task.Status != docs.StatusDone {
		t.Fatalf("installed commit rolled its task back: %+v", task)
	}
	committedTask := gitRun(t, ws, "show", "HEAD:backlog/0001-recover-commit.md")
	if !strings.Contains(committedTask, "status: done") {
		t.Fatalf("installed commit lacks completed task:\n%s", committedTask)
	}

	indexPath := filepath.Join(ws, ".git", "index")
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(indexPath+".saved", indexPath); err != nil {
		t.Fatal(err)
	}
	reopened, err := git.OpenExisting(ws)
	if err != nil {
		t.Fatal(err)
	}
	reloadedBaseline, err := reopened.LoadBaseline("finalization-test")
	if err != nil {
		t.Fatal(err)
	}
	restarted := finalizationDeps(ws, reopened, reloadedBaseline, docs.NewStore(ws), &captureRec{})
	if got, failed := callCommit(t, restarted); failed || !strings.Contains(got, "committed ") {
		t.Fatalf("index recovery retry = error %v, %q", failed, got)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD")); got != committedHead {
		t.Fatalf("index recovery duplicated commit: before=%s after=%s", committedHead, got)
	}
	if staged := strings.TrimSpace(gitRun(t, ws, "diff", "--cached", "--name-only")); staged != "unrelated.txt" {
		t.Fatalf("index recovery broadened or consumed unrelated staging: %q", staged)
	}
}

func TestCommitFinalizationRestoresTaskWhenCompletedCheckpointFails(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	d := finalizationDeps(ws, repo, baseline, store, &captureRec{})
	before, err := store.Get("0001")
	if err != nil {
		t.Fatal(err)
	}
	path, err := repo.FinalizationPath("0001")
	if err != nil {
		t.Fatal(err)
	}
	journal := &failSaveJournal{inner: fileFinalizationJournal{path: path}, failAt: 2}
	_, err = finalizeTaskWithJournal(context.Background(), d, "0001", "finish safely", "Implemented and verified recovery.", journal)
	if err == nil || !strings.Contains(err.Error(), "record completed task state") || !strings.Contains(err.Error(), "task was restored") {
		t.Fatalf("completed checkpoint failure = %v", err)
	}
	after, err := store.Get("0001")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != before.Status || after.Body != before.Body {
		t.Fatalf("checkpoint failure left completed task: before=%+v after=%+v", before, after)
	}
	if got, failed := callCommit(t, d); failed || !strings.Contains(got, "committed ") {
		t.Fatalf("checkpoint retry = error %v, %q", failed, got)
	}
}

func TestCommitFinalizationAcceptsLegacyDoneAndAlreadyCommittedNoOp(t *testing.T) {
	t.Run("legacy done is compacted and committed", func(t *testing.T) {
		ws, repo, baseline, store := setupFinalizationRepo(t)
		if _, err := store.Update("0001", func(task *docs.Task) { task.Status = docs.StatusDone }); err != nil {
			t.Fatal(err)
		}
		d := finalizationDeps(ws, repo, baseline, store, &captureRec{})
		if got, failed := callCommit(t, d); failed || !strings.Contains(got, "committed ") {
			t.Fatalf("legacy done commit = error %v, %q", failed, got)
		}
		if task, _ := store.Get("0001"); task.Status != docs.StatusDone || !strings.Contains(task.Body, "Implemented and verified recovery.") {
			t.Fatalf("legacy done task was not finalized: %+v", task)
		}
	})

	t.Run("already committed done is a no-op", func(t *testing.T) {
		ws, _, _, store := setupFinalizationRepo(t)
		if err := os.WriteFile(filepath.Join(ws, "owned.txt"), []byte("before\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Update("0001", func(task *docs.Task) { task.Status = docs.StatusDone }); err != nil {
			t.Fatal(err)
		}
		gitRun(t, ws, "add", "backlog/0001-recover-commit.md")
		gitRun(t, ws, "commit", "-m", "legacy completion")
		repo, err := git.Open(ws)
		if err != nil {
			t.Fatal(err)
		}
		baseline := repo.OpenBaseline()
		beforeCount := strings.TrimSpace(gitRun(t, ws, "rev-list", "--count", "HEAD"))
		recorder := &captureRec{}
		d := finalizationDeps(ws, repo, baseline, store, recorder)
		if got, failed := callCommit(t, d); failed || !strings.Contains(got, "committed ") {
			t.Fatalf("already committed no-op = error %v, %q", failed, got)
		}
		if afterCount := strings.TrimSpace(gitRun(t, ws, "rev-list", "--count", "HEAD")); afterCount != beforeCount {
			t.Fatalf("no-op created commit: before=%s after=%s", beforeCount, afterCount)
		}
		if len(recorder.events) != 0 {
			t.Fatalf("no-op emitted new finalization events: %+v", recorder.events)
		}
	})
}

func TestCommitFinalizationStopsBeforeMutationWhenEventLogFailed(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	recorder := &failEventRecorder{failed: errors.New("event disk full")}
	d := finalizationDeps(ws, repo, baseline, store, recorder)
	before, err := store.Get("0001")
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD"))
	if got, failed := callCommit(t, d); !failed || !strings.Contains(got, "event log is unavailable") {
		t.Fatalf("terminal event failure = error %v, %q", failed, got)
	}
	after, err := store.Get("0001")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != before.Status || after.Body != before.Body {
		t.Fatalf("terminal event failure mutated task: before=%+v after=%+v", before, after)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD")); got != head {
		t.Fatalf("terminal event failure moved HEAD from %s to %s", head, got)
	}
}

func TestCommitFinalizationResumesEventPublicationAfterRestart(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	failedRecorder := &failEventRecorder{failAt: event.CommitMade}
	d := finalizationDeps(ws, repo, baseline, store, failedRecorder)
	if got, failed := callCommit(t, d); !failed || !strings.Contains(got, "finalization event publication is pending") {
		t.Fatalf("event failure = error %v, %q", failed, got)
	}
	commits := strings.TrimSpace(gitRun(t, ws, "rev-list", "--count", "HEAD"))
	if task, _ := store.Get("0001"); task.Status != docs.StatusDone {
		t.Fatalf("real commit did not retain completed task: %+v", task)
	}

	// A restarted session has a fresh event writer. It consumes the journal rather
	// than requiring a changeset from the now-moved baseline or creating a commit.
	healthy := &captureRec{}
	reopened, err := git.OpenExisting(ws)
	if err != nil {
		t.Fatal(err)
	}
	reloadedBaseline, err := reopened.LoadBaseline("finalization-test")
	if err != nil {
		t.Fatal(err)
	}
	restarted := finalizationDeps(ws, reopened, reloadedBaseline, docs.NewStore(ws), healthy)
	if got, failed := callCommit(t, restarted); failed || !strings.Contains(got, "committed ") {
		t.Fatalf("restart retry = error %v, %q", failed, got)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "rev-list", "--count", "HEAD")); got != commits {
		t.Fatalf("event retry created another commit: before=%s after=%s", commits, got)
	}
	if len(healthy.events) != 1 || healthy.events[0].Type != event.CommitMade {
		t.Fatalf("restart published wrong pending events: %+v", healthy.events)
	}
	if healthy.events[0].Data["finalization_id"] == "" {
		t.Fatal("pending event lacks stable finalization identity")
	}
}

func TestCommitFinalizationResumesInterruptedTaskTransition(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	d := finalizationDeps(ws, repo, baseline, store, &captureRec{})
	preflight, err := d.changeset("0001")
	if err != nil {
		t.Fatal(err)
	}
	completion, err := store.PrepareCompletion("0001", "Implemented and verified recovery.", "finish safely")
	if err != nil {
		t.Fatal(err)
	}
	path, err := repo.FinalizationPath("0001")
	if err != nil {
		t.Fatal(err)
	}
	record := &finalizationRecord{
		Version: finalizationRecordVersion, TaskID: "0001", Message: "finish safely",
		Outcome: "Implemented and verified recovery.", BaselineID: preflight.BaselineID, FinalizationID: preflight.ID,
		Phase: finalizationPrepared, Completion: completion,
	}
	if err := saveFinalization(path, record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyCompletion(completion, true); err != nil {
		t.Fatal(err)
	}
	// Simulate process loss before recording task_completed: rebuild repository,
	// baseline, document store, emitter, and journal state from durable storage.
	reopened, err := git.OpenExisting(ws)
	if err != nil {
		t.Fatal(err)
	}
	reloadedBaseline, err := reopened.LoadBaseline("finalization-test")
	if err != nil {
		t.Fatal(err)
	}
	restarted := finalizationDeps(ws, reopened, reloadedBaseline, docs.NewStore(ws), &captureRec{})
	if got, failed := callCommit(t, restarted); failed || !strings.Contains(got, "committed ") {
		t.Fatalf("interrupted retry = error %v, %q", failed, got)
	}
	if task, _ := store.Get("0001"); task.Status != docs.StatusDone {
		t.Fatalf("interrupted completion was not finalized: %+v", task)
	}
}

func TestCommitFinalizationRefusesRenewalFromDifferentBaseline(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	hook := filepath.Join(ws, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := finalizationDeps(ws, repo, baseline, store, &captureRec{})
	beforeHead := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD"))
	if got, failed := callCommit(t, original); !failed {
		t.Fatalf("hook failure unexpectedly succeeded: %q", got)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}

	// A new session now sees the implementation as preexisting work. It must not
	// silently renew the journal to a narrower task-document-only changeset.
	freshRepo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	freshBaseline := freshRepo.OpenBaseline()
	if freshBaseline.ID == baseline.ID {
		t.Fatal("fresh session unexpectedly captured the original baseline")
	}
	fresh := finalizationDeps(ws, freshRepo, freshBaseline, docs.NewStore(ws), &captureRec{})
	if got, failed := callCommit(t, fresh); !failed || !strings.Contains(got, "belongs to original baseline") {
		t.Fatalf("different-baseline retry = error %v, %q", failed, got)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD")); got != beforeHead {
		t.Fatalf("different-baseline retry moved HEAD: before=%s after=%s", beforeHead, got)
	}
	if task, _ := store.Get("0001"); task.Status == docs.StatusDone {
		t.Fatalf("different-baseline retry left task misleadingly done: %+v", task)
	}
	if got := gitRun(t, ws, "show", "HEAD:owned.txt"); got != "before\n" {
		t.Fatalf("implementation was unexpectedly committed: %q", got)
	}
}

func TestCommitFinalizationRestoresTaskAfterKnownHeadDivergence(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	branchRef := strings.TrimSpace(gitRun(t, ws, "symbolic-ref", "HEAD"))
	lockPath := filepath.Join(ws, ".git", filepath.FromSlash(branchRef)+".lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("locked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := finalizationDeps(ws, repo, baseline, store, &captureRec{})
	if got, failed := callCommit(t, d); !failed || !strings.Contains(got, "advance HEAD") {
		t.Fatalf("initial HEAD failure = error %v, %q", failed, got)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}

	path, err := repo.FinalizationPath("0001")
	if err != nil {
		t.Fatal(err)
	}
	record, err := loadFinalization(path)
	if err != nil || record == nil || record.Commit == nil {
		t.Fatalf("load retained commit recovery = %+v, %v", record, err)
	}
	if _, err := store.ApplyCompletion(record.Completion, true); err != nil {
		t.Fatal(err)
	}
	record.Phase = finalizationTaskCompleted
	if err := saveFinalization(path, record); err != nil {
		t.Fatal(err)
	}

	parent := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD^{tree}"))
	cmd := exec.Command("git", "-C", ws, "commit-tree", tree, "-p", parent)
	cmd.Stdin = strings.NewReader("concurrent unrelated commit\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create concurrent commit: %v: %s", err, out)
	}
	concurrent := strings.TrimSpace(string(out))
	gitRun(t, ws, "update-ref", "HEAD", concurrent, parent)

	if got, failed := callCommit(t, d); !failed || !strings.Contains(got, "was not installed because HEAD moved") {
		t.Fatalf("divergent retry = error %v, %q", failed, got)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD")); got != concurrent {
		t.Fatalf("recovery overwrote unrelated HEAD: got=%s want=%s", got, concurrent)
	}
	if task, _ := store.Get("0001"); task.Status == docs.StatusDone {
		t.Fatalf("known-uninstalled commit left task done: %+v", task)
	}
}

func TestCommitFinalizationRejectsDirtyDoneNoOpNotPresentInHead(t *testing.T) {
	ws, _, _, store := setupFinalizationRepo(t)
	if _, err := store.Update("0001", func(task *docs.Task) { task.Status = docs.StatusDone }); err != nil {
		t.Fatal(err)
	}
	// Capturing now excludes both the implementation and dirty done task. An empty
	// scoped diff is not evidence that either was accepted into HEAD.
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	d := finalizationDeps(ws, repo, repo.OpenBaseline(), store, &captureRec{})
	beforeHead := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD"))
	if got, failed := callCommit(t, d); !failed || !strings.Contains(got, "done only in preexisting/unowned worktree state") {
		t.Fatalf("dirty done no-op = error %v, %q", failed, got)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "rev-parse", "HEAD")); got != beforeHead {
		t.Fatalf("dirty done no-op moved HEAD: before=%s after=%s", beforeHead, got)
	}
}

func TestCommitFinalizationCanonicalizesTaskAlias(t *testing.T) {
	ws, repo, baseline, store := setupFinalizationRepo(t)
	recorder := &captureRec{}
	d := finalizationDeps(ws, repo, baseline, store, recorder)
	call := func(id string) (string, bool) {
		result, err := commitTool(d).Call(context.Background(), map[string]any{
			"task_id": id, "message": "finish safely", "outcome": "Implemented and verified recovery.",
		})
		if err != nil {
			t.Fatal(err)
		}
		return result.Content, result.IsError
	}
	if got, failed := call("1"); failed || !strings.Contains(got, "committed ") {
		t.Fatalf("alias commit = error %v, %q", failed, got)
	}
	beforeCount := strings.TrimSpace(gitRun(t, ws, "rev-list", "--count", "HEAD"))
	if got, failed := call("1"); failed || !strings.Contains(got, "committed ") {
		t.Fatalf("alias repeat = error %v, %q", failed, got)
	}
	if got := strings.TrimSpace(gitRun(t, ws, "rev-list", "--count", "HEAD")); got != beforeCount {
		t.Fatalf("alias repeat duplicated commit: before=%s after=%s", beforeCount, got)
	}
	if len(recorder.events) != 2 {
		t.Fatalf("canonical finalization emitted events more than once: %+v", recorder.events)
	}
	for _, ev := range recorder.events {
		if ev.Data["task"] != "0001" {
			t.Fatalf("event used noncanonical task id: %+v", ev)
		}
	}
	canonical, err := repo.FinalizationPath("0001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("canonical journal missing: %v", err)
	}
	alias, err := repo.FinalizationPath("1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(alias); !os.IsNotExist(err) {
		t.Fatalf("noncanonical alias journal exists: %v", err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
