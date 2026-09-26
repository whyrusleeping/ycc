package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
)

func TestDaemonBacklogReadDefersDuplicateRepairWhileOwned(t *testing.T) {
	workspace := t.TempDir()
	seed := docs.NewStore(workspace)
	task, err := seed.Create("first claimant", "", 3, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := filepath.Join(filepath.Dir(task.Path), task.ID+"-second-claimant.md")
	if err := os.WriteFile(duplicate, data, 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(testRegistry(), workspace)
	store := m.backlogStore(workspace)
	owner := m.ownership.NewToken("active implementer")
	lease, err := m.ownership.Acquire(workspace, owner)
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan []*docs.Task, 1)
	errCh := make(chan error, 1)
	go func() {
		tasks, readErr := store.ListMetadata()
		if readErr != nil {
			errCh <- readErr
			return
		}
		result <- tasks
	}()
	var tasks []*docs.Task
	select {
	case err := <-errCh:
		t.Fatalf("read while owned: %v", err)
	case tasks = <-result:
	case <-time.After(time.Second):
		t.Fatal("ordinary backlog read blocked on mutation ownership")
	}
	if len(tasks) != 2 || tasks[0].ID != task.ID || tasks[1].ID != task.ID {
		t.Fatalf("read while repair deferred = %+v, want two raw duplicate claimants", tasks)
	}
	if _, err := os.Stat(duplicate); err != nil {
		t.Fatalf("duplicate was rewritten while another scope owned the tree: %v", err)
	}
	lease.Release()

	tasks, err = store.ListMetadata()
	if err != nil {
		t.Fatalf("repair after release: %v", err)
	}
	if len(tasks) != 2 || tasks[0].ID == tasks[1].ID {
		t.Fatalf("deferred duplicate repair did not self-heal: %+v", tasks)
	}
	if _, err := os.Stat(duplicate); !os.IsNotExist(err) {
		t.Fatalf("old duplicate path still exists after repair: %v", err)
	}
}

// Session startup takes no worktree lease: another scope mid-write (e.g. a
// subagent running its tools) must never prevent a new session from starting.
func TestNewSessionStartsWhileAnotherScopeHoldsLease(t *testing.T) {
	workspace := t.TempDir()
	m := NewManager(testRegistry(), workspace)
	lease, err := m.ownership.Acquire(workspace, m.ownership.NewToken("existing session implementer"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	log, err := event.OpenLog(filepath.Join(workspace, ".ycc", "sessions", "contender", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	s, err := m.newSession(workspace, "contender", "chat", false, "hi", log, false, "")
	if err != nil {
		t.Fatalf("newSession refused while another scope held the lease: %v", err)
	}
	if s == nil {
		t.Fatal("newSession returned no session")
	}
}
