package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/notify"
)

func TestWorkLoopPreflightListsUncommittedInReviewWork(t *testing.T) {
	var ws string
	factory := func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(ctx context.Context) (loopSessRec, bool, error) {
			store := docs.NewStore(ws)
			tasks, _ := store.List()
			id := topReadyTask(tasks)
			if id == "" {
				return loopSessRec{}, false, nil
			}
			_, _ = store.Update(id, func(tk *docs.Task) { tk.Status = docs.StatusDone })
			return loopSessRec{id: "sess_" + id, focus: id}, false, nil
		}
	}
	m, sink, dir := loopTestManager(t, factory)
	ws = dir
	defer m.ReclaimAll()

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = ws
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@example.invalid")
	git("config", "user.name", "t")

	store := docs.NewStore(ws)
	var ids []string
	for _, title := range []string{"own file dirty", "claimed work", "committed review", "ready"} {
		task, err := store.Create(title, "", 3, nil, nil)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		ids = append(ids, task.ID)
	}
	own, claimed, clean, ready := ids[0], ids[1], ids[2], ids[3]
	for _, id := range []string{claimed, clean} {
		if _, err := store.Update(id, func(tk *docs.Task) { tk.Status = docs.StatusInReview }); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "-q", "-m", "fixture")

	// own: set in_review but never committed, so its backlog file is dirty.
	ownTask, err := store.Update(own, func(tk *docs.Task) { tk.Status = docs.StatusInReview })
	if err != nil {
		t.Fatal(err)
	}
	// claimed: a session that focused it wrote src/b.go and left it uncommitted.
	if err := os.MkdirAll(filepath.Join(ws, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"src/b.go", "unattributed.txt"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := newStopSession(t)
	s.ID = "sess_reviewer"
	s.Workspace = ws
	// The session focused the committed review task before the one whose
	// work it left uncommitted; only the latter is charged with src/b.go.
	s.emitter.Emit(event.TaskFocus, map[string]any{"task": clean})
	s.emitter.Emit(event.TaskFocus, map[string]any{"task": claimed})
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	m.ownership.RecordWrites(ws, []string{"src/b.go"}, m.ownership.NewScopedToken(s.ID, "test"))

	ownRel := "backlog/" + filepath.Base(ownTask.Path)
	pre, err := m.WorkLoopPreflight("demo")
	if err != nil {
		t.Fatalf("WorkLoopPreflight: %v", err)
	}
	check := func(label string, got []WorkLoopDigestTask, paths int) {
		t.Helper()
		if len(got) != 2 || got[0].ID != own || got[1].ID != claimed {
			t.Fatalf("%s: uncommitted in_review = %+v, want %s and %s (not %s)", label, got, own, claimed, clean)
		}
		if got[0].Reason != "uncommitted: "+ownRel || got[1].Reason != "uncommitted: src/b.go" {
			t.Fatalf("%s: reasons = %q, %q", label, got[0].Reason, got[1].Reason)
		}
		if paths != 2 {
			t.Fatalf("%s: paths = %d, want 2", label, paths)
		}
	}
	check("preflight", pre.InReview, pre.Paths)
	if pre.Parallel != 1 {
		t.Fatalf("preflight parallel = %d, want the default 1", pre.Parallel)
	}

	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatalf("StartWorkLoop: %v", err)
	}
	final := waitLoopFinished(t, m, "demo")
	m.mu.Lock()
	delete(m.sessions, s.ID)
	m.mu.Unlock()
	if len(final.Completed) != 1 || final.Completed[0].ID != ready {
		t.Fatalf("completed = %+v, want %s", final.Completed, ready)
	}
	check("digest", final.UncommittedInReview, final.UncommittedPaths)

	persisted, ok := readPersistedWorkLoop(ws)
	if !ok {
		t.Fatal("no persisted loop")
	}
	check("persisted", restoredTasks(persisted.UncommittedInReview), persisted.UncommittedPaths)

	// Finished state is published before notification; join the driver first.
	m.loopWG.Wait()
	m.notifier.Flush()
	var found bool
	for _, r := range sink.all() {
		if r.tags == notify.KindDigest && strings.Contains(r.body, "2 in-review tasks were uncommitted at start") {
			found = true
		}
	}
	if !found {
		t.Fatalf("digest notification lacks the uncommitted in-review count: %+v", sink.all())
	}
}

func TestTaskFocusedAt(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	marks := []focusMark{{"0001", t0}, {"0002", t0.Add(time.Minute + 300*time.Millisecond)}}
	cases := []struct {
		since time.Time
		want  string
	}{
		{t0.Add(-time.Second), ""},                           // written before any focus
		{t0.Add(30 * time.Second), "0001"},                   // during the first focus
		{t0.Add(time.Minute + 100*time.Millisecond), "0001"}, // live claim just before the switch
		{t0.Add(time.Minute + 500*time.Millisecond), "0002"}, // live claim just after it
		{t0.Add(time.Minute), ""},                            // reloaded whole second spanning the switch
		{t0.Add(2 * time.Minute), "0002"},                    // reloaded, well after the switch
		{time.Time{}, ""},                                    // unknown time, several tasks: ambiguous
	}
	for _, c := range cases {
		if got := taskFocusedAt(marks, c.since); got != c.want {
			t.Errorf("taskFocusedAt(%v) = %q, want %q", c.since, got, c.want)
		}
	}
	// A→B→A within one reloaded second: the write could belong to either.
	back := []focusMark{{"A", t0.Add(-time.Second)}, {"B", t0.Add(100 * time.Millisecond)}, {"A", t0.Add(900 * time.Millisecond)}}
	if got := taskFocusedAt(back, t0); got != "" {
		t.Errorf("whole second spanning A→B→A = %q, want unattributed", got)
	}
	if got := taskFocusedAt(back, t0.Add(400*time.Millisecond)); got != "B" {
		t.Errorf("live claim during B = %q, want B", got)
	}
	if got := taskFocusedAt(marks[:1], time.Time{}); got != "0001" {
		t.Errorf("unknown time with one focused task = %q, want 0001", got)
	}
}

func TestUncommittedReasonBoundsPaths(t *testing.T) {
	got := uncommittedReason([]string{"a", "b", "c", "d", "e", "f", "g"})
	if want := "uncommitted: a, b, c, d, e (+2 more)"; got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}
