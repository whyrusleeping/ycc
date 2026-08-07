package docs

import (
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

func TestIDAllocatorSeedsAndRefloorsFromPrimaryBacklog(t *testing.T) {
	primary := t.TempDir()
	writeRawTask(t, primary, "0007-existing.md", "0007", "existing", "2026-01-01")
	allocator := newIDAllocator("")

	id, err := allocator.NextID(filepath.Join(primary, "backlog"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "0008" {
		t.Fatalf("first allocated id = %q, want 0008", id)
	}

	// A task added outside the allocator raises the floor on the next call.
	writeRawTask(t, primary, "0015-manual.md", "0015", "manual", "2026-01-02")
	id, err = allocator.NextID(filepath.Join(primary, "backlog"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "0016" {
		t.Fatalf("id after manual task = %q, want 0016", id)
	}
}

func TestIDAllocatorSerializesStoresInDifferentWorktrees(t *testing.T) {
	primary := t.TempDir()
	writeRawTask(t, primary, "0007-existing.md", "0007", "existing", "2026-01-01")
	allocator := newIDAllocator("")
	primaryBacklog := filepath.Join(primary, "backlog")

	stores := []*Store{NewStore(t.TempDir()), NewStore(t.TempDir())}
	for _, store := range stores {
		store.SetIDSource(func() (string, error) { return allocator.NextID(primaryBacklog) })
	}

	start := make(chan struct{})
	ids := make(chan string, len(stores))
	errs := make(chan error, len(stores))
	var wg sync.WaitGroup
	for i, store := range stores {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			<-start
			task, err := store.CreateWithStatus("parallel task", "", i+1, nil, nil, StatusTodo)
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
		t.Errorf("CreateWithStatus: %v", err)
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

func TestIDAllocatorPersistsReservations(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state", "backlog-ids.json")
	primaryBacklog := filepath.Join(t.TempDir(), "backlog")

	first := newIDAllocator(stateFile)
	id, err := first.NextID(primaryBacklog)
	if err != nil {
		t.Fatal(err)
	}
	if id != "0001" {
		t.Fatalf("first id = %q, want 0001", id)
	}

	// Simulate a daemon restart. No task was written to the primary tree, so only
	// the durable reservation can prevent 0001 from being issued again.
	restarted := newIDAllocator(stateFile)
	id, err = restarted.NextID(primaryBacklog)
	if err != nil {
		t.Fatal(err)
	}
	if id != "0002" {
		t.Fatalf("id after restart = %q, want 0002", id)
	}
}
