package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/project"
)

func createTasks(t *testing.T, ws string, n int, status docs.Status) []string {
	t.Helper()
	store := docs.NewStore(ws)
	var ids []string
	for i := 0; i < n; i++ {
		task, err := store.CreateWithStatus(fmt.Sprintf("task %d", i), "body", 3, nil, nil, status)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	return ids
}

func waitNoBacklogGroom(t *testing.T, m *Manager, primary string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.groomMu.Lock()
		_, running := m.backlogGrooms[primary]
		m.groomMu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("automatic backlog groom did not finish")
}

func TestBacklogGroomRunsOnChurnOncePerProjectWithPersistedCooldown(t *testing.T) {
	m, _, _ := presetTestManager(t, "b")
	ws, _ := filepath.Abs(t.TempDir())
	t.Cleanup(m.ReclaimAll)
	var fakeStarts int
	fake := func(string, string) (*Session, error) {
		fakeStarts++
		return nil, os.ErrInvalid
	}

	// The first check records a baseline: an existing backlog is not churn.
	createTasks(t, ws, 3, docs.StatusTodo)
	m.startBacklogGroom = fake
	m.maybeGroomBacklog(ws)
	if st := loadBacklogGroomState(ws); st.BaselineAt.IsZero() || len(st.Baseline) != 3 || fakeStarts != 0 {
		t.Fatalf("first check: starts=%d state=%+v", fakeStarts, st)
	}

	// Below the churn threshold nothing starts; in_progress tasks do not count.
	created := createTasks(t, ws, backlogGroomChurn-1, docs.StatusTodo)
	createTasks(t, ws, 1, docs.StatusInProgress)
	m.maybeGroomBacklog(ws)
	if fakeStarts != 0 {
		t.Fatalf("churn below threshold started a groom")
	}

	// Past it, starting any session launches one unattended groom with the
	// changed tasks as a bounded batch, oldest first.
	created = append(created, createTasks(t, ws, backlogGroomBatch+2-len(created), docs.StatusTodo)...)
	m.startBacklogGroom = nil
	chat, err := m.Start(Config{Workspace: ws, Mode: "chat", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := m.BacklogGroomInfo(filepath.Base(ws))
	if err != nil {
		t.Fatal(err)
	}
	groomID := info.RunningSession
	if groomID == "" || groomID == chat.ID || !info.Enabled {
		t.Fatalf("expected a running backlog groom, got %+v", info)
	}
	groom, ok := m.Get(groomID)
	if !ok {
		t.Fatal("groom session is not live")
	}
	if groom.Mode != "pm" || !groom.inter.unattended || groom.preset != backlogGroomPreset || groom.origin != OriginAutomation || groom.coordinator != "a" {
		t.Fatalf("groom session = mode %q unattended %v preset %q origin %q coordinator %q",
			groom.Mode, groom.inter.unattended, groom.preset, groom.origin, groom.coordinator)
	}
	st := loadBacklogGroomState(ws)
	if st.LastSession != groomID || st.Changed != len(created) || len(st.Batch) != backlogGroomBatch || st.Deferred != 2 || st.Batch[0] != created[0] {
		t.Fatalf("persisted state = %+v (created %v)", st, created)
	}
	if !strings.Contains(groom.prompt, strings.Join(st.Batch, ", ")) || !strings.Contains(groom.prompt, "2 more changed tasks") {
		t.Fatalf("groom prompt = %q", groom.prompt)
	}

	// At most one per project: another session start does not add a second.
	m.maybeGroomBacklog(ws)
	if n := len(m.ListByProject(filepath.Base(ws))); n != 2 {
		t.Fatalf("expected the chat + one groom session, got %d", n)
	}

	// A groom that did not finish records its outcome but keeps the baseline.
	if err := m.Stop(groomID); err != nil {
		t.Fatal(err)
	}
	waitNoBacklogGroom(t, m, ws)
	st = loadBacklogGroomState(ws)
	if st.LastFinished.IsZero() || st.Outcome == "" || st.Outcome == "finished" || len(st.Baseline) != 3 {
		t.Fatalf("stopped groom state = %+v", st)
	}

	// The cooldown is persisted: a fresh manager (daemon restart) honours it.
	m.startBacklogGroom = fake
	m.maybeGroomBacklog(ws)
	restarted, _, _ := presetTestManager(t, "b")
	t.Cleanup(restarted.ReclaimAll)
	restarted.startBacklogGroom = fake
	restarted.maybeGroomBacklog(ws)
	if fakeStarts != 0 {
		t.Fatalf("cooldown did not hold: starts=%d", fakeStarts)
	}
	// Past it, the unfinished run's churn is still due.
	st.LastStarted = time.Now().Add(-2 * backlogGroomCooldown)
	saveBacklogGroomState(ws, st)
	restarted.maybeGroomBacklog(ws)
	if fakeStarts != 1 || len(restarted.backlogGrooms) != 0 {
		t.Fatalf("past cooldown: starts=%d grooms=%v", fakeStarts, restarted.backlogGrooms)
	}
}

func TestBacklogGroomAdvanceBaselineAcknowledgesOnlyReviewedTasks(t *testing.T) {
	old := map[string]string{"a.md": "1", "b.md": "1", "c.md": "1", "e.md": "1", "gone.md": "1"}
	start := map[string]int{"a.md": 0, "b.md": 0, "c.md": 0, "e.md": 1}
	files := []backlogFile{
		{name: "a.md", hash: "2", grooms: 1}, // changed and reviewed this run
		{name: "b.md", hash: "2"},            // changed, but skipped or deferred
		{name: "c.md", hash: "1"},            // unchanged
		{name: "d.md", hash: "1"},            // created during the run by someone else
		{name: "e.md", hash: "2", grooms: 1}, // groomed earlier, edited by another session during the run
		{name: "f.md", hash: "1", grooms: 1}, // filed and reviewed by the run itself
	}
	got := advanceBaseline(old, files, start)
	want := map[string]string{"a.md": "2", "b.md": "1", "c.md": "1", "e.md": "1", "f.md": "1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("baseline = %v, want %v", got, want)
	}
	churn := backlogChurn(got, files)
	var names []string
	for _, f := range churn {
		names = append(names, f.name)
	}
	if strings.Join(names, ",") != "b.md,d.md,e.md" {
		t.Fatalf("unreviewed changes should remain churn, got %v", names)
	}
}

func TestBacklogGroomHonoursProjectAndDaemonSwitches(t *testing.T) {
	m, _, _ := presetTestManager(t, "b")
	ws, _ := filepath.Abs(t.TempDir())
	t.Cleanup(m.ReclaimAll)
	var starts int
	m.startBacklogGroom = func(string, string) (*Session, error) {
		starts++
		return nil, os.ErrInvalid
	}
	createTasks(t, ws, 1, docs.StatusTodo)
	if _, err := m.projects.EnsureWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	m.maybeGroomBacklog(ws) // baseline
	createTasks(t, ws, backlogGroomChurn, docs.StatusTodo)

	// Per project: [backlog] auto_groom = false in .ycc/config.toml.
	if err := os.MkdirAll(filepath.Join(ws, ".ycc"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(ws, ".ycc", "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[backlog]\nauto_groom = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.maybeGroomBacklog(ws)
	if info, err := m.BacklogGroomInfo(filepath.Base(ws)); err != nil || starts != 0 || info.Enabled {
		t.Fatalf("project switch: starts=%d enabled=%v err=%v", starts, info.Enabled, err)
	}

	// Daemon-wide: backlog.auto_groom = false.
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	off := false
	cfg := &config.Config{
		Models:  map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:   config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
		Backlog: config.Backlog{AutoGroom: &off},
	}
	disabled := NewManager(config.NewRegistry(cfg), "")
	disabled.SetProjects(project.NewMemory())
	t.Cleanup(disabled.ReclaimAll)
	disabled.startBacklogGroom = m.startBacklogGroom
	disabled.maybeGroomBacklog(ws)
	if starts != 0 {
		t.Fatalf("daemon switch: starts=%d", starts)
	}

	// With both on, the same churn is due; a failed launch leaves no marker.
	m.maybeGroomBacklog(ws)
	if starts != 1 || len(m.backlogGrooms) != 0 {
		t.Fatalf("enabled: starts=%d grooms=%v", starts, m.backlogGrooms)
	}
}
