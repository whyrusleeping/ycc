package workstream

import (
	"path/filepath"
	"testing"
)

func mk(id, proj, path string) Workstream {
	return Workstream{ID: id, Project: proj, Branch: "ycc/ws/" + id, WorktreePath: path, Status: StatusActive}
}

func TestRegistryPersistenceAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workstreams.json")
	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := r.Add(mk("ws_a", "proj", "/tmp/wt/a")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Reopen simulates a daemon restart.
	r2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := r2.Get("ws_a")
	if !ok {
		t.Fatal("workstream not persisted across restart")
	}
	if got.Project != "proj" || got.WorktreePath != "/tmp/wt/a" || got.Status != StatusActive {
		t.Fatalf("unexpected reopened workstream: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not set on Add")
	}
}

func TestDuplicateActiveWorktreeRejected(t *testing.T) {
	r := NewMemory()
	if err := r.Add(mk("ws_a", "proj", "/tmp/wt/shared")); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	err := r.Add(mk("ws_b", "proj", "/tmp/wt/shared"))
	if err == nil {
		t.Fatal("expected ErrWorktreeInUse for duplicate active path")
	}
	// A terminal (discarded) entry frees the path for a new active workstream.
	if err := r.SetStatus("ws_a", StatusDiscarded); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if err := r.Add(mk("ws_c", "proj", "/tmp/wt/shared")); err != nil {
		t.Fatalf("Add after discard should succeed: %v", err)
	}
}

func TestSetStatusPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workstreams.json")
	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := r.Add(mk("ws_a", "proj", "/tmp/wt/a")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := r.SetStatus("ws_a", StatusMerged); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	r2, _ := Open(path)
	got, _ := r2.Get("ws_a")
	if got.Status != StatusMerged {
		t.Fatalf("status not persisted: %v", got.Status)
	}
	if err := r.SetStatus("nope", StatusStale); err == nil {
		t.Fatal("expected error for unknown id")
	}
}

func TestListByProjectFilters(t *testing.T) {
	r := NewMemory()
	r.Add(mk("ws_a", "p1", "/tmp/a"))
	r.Add(mk("ws_b", "p2", "/tmp/b"))
	r.Add(mk("ws_c", "p1", "/tmp/c"))
	p1 := r.ListByProject("p1")
	if len(p1) != 2 {
		t.Fatalf("expected 2 workstreams for p1, got %d", len(p1))
	}
	for _, w := range p1 {
		if w.Project != "p1" {
			t.Fatalf("ListByProject returned wrong project: %+v", w)
		}
	}
	if len(r.ListByProject("")) != 3 {
		t.Fatal("empty project should return all")
	}
}

func TestSetSessionIDAndRemove(t *testing.T) {
	r := NewMemory()
	r.Add(mk("ws_a", "p", "/tmp/a"))
	if err := r.SetSessionID("ws_a", "s_123"); err != nil {
		t.Fatalf("SetSessionID: %v", err)
	}
	got, _ := r.Get("ws_a")
	if got.SessionID != "s_123" {
		t.Fatalf("session id not set: %+v", got)
	}
	if err := r.Remove("ws_a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := r.Get("ws_a"); ok {
		t.Fatal("workstream not removed")
	}
	if err := r.Remove("nope"); err != nil {
		t.Fatalf("Remove unknown should be no-op: %v", err)
	}
}
