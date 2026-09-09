package session

import (
	"os"
	"path/filepath"
	"strings"
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

func TestNewSessionAcquiresBeforeGitInitialization(t *testing.T) {
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
	_, err = m.newSession(workspace, "contender", "chat", false, "hi", log, false, "")
	if err == nil || !strings.Contains(err.Error(), "existing session implementer") {
		t.Fatalf("newSession ownership conflict = %v", err)
	}
	for _, want := range []string{"wait", "stop", "workstream"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("newSession conflict %q missing %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(workspace, ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("git workspace mutated before ownership acquisition: %v", statErr)
	}
}
