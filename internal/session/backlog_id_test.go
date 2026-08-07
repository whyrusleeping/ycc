package session

import (
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

func TestManagerBacklogStoresShareProjectIDAllocator(t *testing.T) {
	primary := t.TempDir()
	seed := docs.NewStore(primary)
	for i := 0; i < 7; i++ {
		if _, err := seed.Create("existing", "", 1, nil, nil); err != nil {
			t.Fatalf("seed backlog: %v", err)
		}
	}

	m := NewManager(testRegistry(), "")
	projects := project.NewMemory()
	if _, err := projects.Add(primary, "demo"); err != nil {
		t.Fatalf("add project: %v", err)
	}
	m.SetProjects(projects)
	root := filepath.Join(t.TempDir(), "worktrees")
	workstreams := workstream.NewMemory()
	m.SetWorkstreams(workstreams, root)

	// registeredPath exercises registry resolution; inferredPath exercises the
	// root/project-dir fallback used while SpawnWorkstream is starting a session.
	registeredPath := filepath.Join(t.TempDir(), "registered-worktree")
	if err := workstreams.Add(workstream.Workstream{
		ID:           "ws_registered",
		Project:      "demo",
		WorktreePath: registeredPath,
		Status:       workstream.StatusActive,
	}); err != nil {
		t.Fatalf("add workstream: %v", err)
	}
	inferredPath := filepath.Join(root, workstream.SafeProjectDir("demo"), "ws_starting")

	if got := m.primaryTreeFor(registeredPath); got != primary {
		t.Fatalf("registered worktree primary = %q, want %q", got, primary)
	}
	if got := m.primaryTreeFor(inferredPath); got != primary {
		t.Fatalf("inferred worktree primary = %q, want %q", got, primary)
	}

	stores := []*docs.Store{m.backlogStore(registeredPath), m.backlogStore(inferredPath)}
	start := make(chan struct{})
	ids := make(chan string, len(stores))
	errs := make(chan error, len(stores))
	var wg sync.WaitGroup
	for i, store := range stores {
		wg.Add(1)
		go func(i int, store *docs.Store) {
			defer wg.Done()
			<-start
			task, err := store.Create("parallel", "", i+1, nil, nil)
			if err != nil {
				errs <- err
				return
			}
			ids <- task.ID
		}(i, store)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("create task: %v", err)
	}
	close(ids)
	var got []string
	for id := range ids {
		got = append(got, id)
	}
	sort.Strings(got)
	if len(got) != 2 || got[0] != "0008" || got[1] != "0009" {
		t.Fatalf("parallel ids = %v, want [0008 0009]", got)
	}
}
