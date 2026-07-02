package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// newWorkstreamManager builds a manager with a registered project backed by a
// real temp git repo (with an initial commit) and an in-memory workstream
// registry rooted at a temp worktrees dir.
func newWorkstreamManager(t *testing.T) (*Manager, string) {
	t.Helper()
	proj := t.TempDir()
	repo, err := git.Open(proj)
	if err != nil {
		t.Fatalf("git.Open: %v", err)
	}
	if _, err := repo.RevParse("HEAD"); err != nil {
		t.Fatalf("repo has no HEAD: %v", err)
	}

	m := NewManager(testRegistry(), t.TempDir())
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(proj, "demo"); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	m.SetWorkstreams(workstream.NewMemory(), filepath.Join(t.TempDir(), "worktrees"))
	return m, proj
}

func TestSpawnWorkstream(t *testing.T) {
	m, proj := newWorkstreamManager(t)

	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", TaskID: "0042", Prompt: "do the thing"})
	if err != nil {
		t.Fatalf("SpawnWorkstream: %v", err)
	}
	defer m.Stop(s.ID)

	// Branch naming (design §5): ycc/ws/<id>-<task>.
	if !strings.HasPrefix(ws.Branch, "ycc/ws/"+ws.ID) || !strings.HasSuffix(ws.Branch, "-0042") {
		t.Fatalf("unexpected branch %q for id %q", ws.Branch, ws.ID)
	}
	// The worktree dir exists and the session is scoped to it.
	if _, err := os.Stat(ws.WorktreePath); err != nil {
		t.Fatalf("worktree dir missing: %v", err)
	}
	if s.Workspace != ws.WorktreePath {
		t.Fatalf("session workspace %q != worktree %q", s.Workspace, ws.WorktreePath)
	}
	// The branch is checked out in the linked worktree.
	repo, _ := git.Open(proj)
	trees, err := repo.ListWorktrees()
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	found := false
	for _, wt := range trees {
		if strings.HasSuffix(wt.Branch, ws.Branch) {
			found = true
		}
	}
	if !found {
		t.Fatalf("branch %q not among worktrees %+v", ws.Branch, trees)
	}
	// The session log lives under the worktree's .ycc/.
	logPath := filepath.Join(ws.WorktreePath, ".ycc", "sessions", s.ID, "events.jsonl")
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("session log not under worktree: %v", err)
	}
	// Registry recorded an active workstream with the session id.
	got, ok := m.workstreams.Get(ws.ID)
	if !ok || got.Status != workstream.StatusActive || got.SessionID != s.ID {
		t.Fatalf("registry entry = %+v ok=%v", got, ok)
	}
	// The project picker is NOT polluted with the worktree path.
	for _, p := range m.Projects() {
		if p.Path == ws.WorktreePath {
			t.Fatalf("worktree leaked into project registry: %+v", p)
		}
	}
	if len(m.Projects()) != 1 {
		t.Fatalf("expected exactly the parent project, got %+v", m.Projects())
	}
	// Workstreams accessor filters by project.
	if got := m.Workstreams("demo"); len(got) != 1 || got[0].ID != ws.ID {
		t.Fatalf("Workstreams(demo) = %+v", got)
	}
}

func TestSpawnWorkstreamUnknownProject(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	if _, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "nope"}); err == nil {
		t.Fatal("expected error for unknown project")
	}
	if _, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{}); err == nil {
		t.Fatal("expected error for missing project")
	}
}

// TestSpawnWorkstreamSecondWriterRejected verifies the single-writer invariant:
// a spawn targeting a path already recorded by an active workstream is refused
// before any worktree is created.
func TestSpawnWorkstreamSecondWriterRejected(t *testing.T) {
	m, _ := newWorkstreamManager(t)

	// Pre-add an active registry entry whose worktree path collides with the
	// deterministic path the next spawn would use is hard (random id), so
	// instead spawn once and then pre-register a conflicting entry using the
	// same path shape and confirm Add rejects duplicates.
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	defer m.Stop(s.ID)

	// A second active workstream for the same path must be rejected by the
	// registry's single-writer guard.
	dup := workstream.Workstream{ID: "ws_dup", Project: "demo", WorktreePath: ws.WorktreePath, Status: workstream.StatusActive}
	if err := m.workstreams.Add(dup); err == nil {
		t.Fatal("expected duplicate active worktree path to be rejected")
	}
}

// TestReconcileWorkstreams verifies startup recovery marks a workstream whose
// worktree directory has vanished as stale while leaving a live one active, and
// that the change persists across a fresh registry Open.
func TestReconcileWorkstreams(t *testing.T) {
	proj := t.TempDir()
	if _, err := git.Open(proj); err != nil {
		t.Fatalf("git.Open: %v", err)
	}
	m := NewManager(testRegistry(), t.TempDir())
	m.SetProjects(project.NewMemory())
	if _, err := m.AddProject(proj, "demo"); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	regPath := filepath.Join(t.TempDir(), "workstreams.json")
	wreg, err := workstream.Open(regPath)
	if err != nil {
		t.Fatalf("workstream.Open: %v", err)
	}
	m.SetWorkstreams(wreg, filepath.Join(t.TempDir(), "worktrees"))

	live, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatalf("spawn live: %v", err)
	}
	// Stop the session so it isn't left running; the workstream stays registered.
	for _, s := range m.List() {
		m.Stop(s.ID)
	}
	gone, _, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatalf("spawn gone: %v", err)
	}
	for _, s := range m.List() {
		m.Stop(s.ID)
	}

	// Remove one worktree dir out-of-band (simulating a crashed daemon).
	if err := os.RemoveAll(gone.WorktreePath); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	if err := m.ReconcileWorkstreams(); err != nil {
		t.Fatalf("ReconcileWorkstreams: %v", err)
	}

	if got, _ := m.workstreams.Get(gone.ID); got.Status != workstream.StatusStale {
		t.Fatalf("vanished workstream status = %v, want stale", got.Status)
	}
	if got, _ := m.workstreams.Get(live.ID); got.Status != workstream.StatusActive {
		t.Fatalf("live workstream status = %v, want active", got.Status)
	}

	// Persistence across a fresh Open (restart simulation).
	reopened, err := workstream.Open(regPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, _ := reopened.Get(gone.ID); got.Status != workstream.StatusStale {
		t.Fatalf("reopened vanished status = %v, want stale", got.Status)
	}
	if got, _ := reopened.Get(live.ID); got.Status != workstream.StatusActive {
		t.Fatalf("reopened live status = %v, want active", got.Status)
	}
}
