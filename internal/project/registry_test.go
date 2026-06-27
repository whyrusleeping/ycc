package project

import (
	"path/filepath"
	"testing"
)

// TestPersistAcrossReload verifies the registry survives a reload: projects
// written by one Registry are seen by a fresh one opened on the same file.
func TestPersistAcrossReload(t *testing.T) {
	file := filepath.Join(t.TempDir(), "projects.json")
	r, err := Open(file)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	a := t.TempDir()
	b := t.TempDir()
	if _, err := r.Add(a, "alpha"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := r.Add(b, ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	r2, err := Open(file)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := r2.List()
	if len(got) != 2 {
		t.Fatalf("List len = %d, want 2", len(got))
	}
	if p, ok := r2.Resolve("alpha"); !ok || p != a {
		t.Fatalf("Resolve(alpha) = %q,%v want %q", p, ok, a)
	}
	// The auto-named project takes its directory basename.
	if p, ok := r2.Resolve(filepath.Base(b)); !ok || p != b {
		t.Fatalf("Resolve(%s) = %q,%v want %q", filepath.Base(b), p, ok, b)
	}
}

// TestAddDedupesPath verifies registering the same path twice returns the
// existing project rather than a duplicate.
func TestAddDedupesPath(t *testing.T) {
	r := NewMemory()
	dir := t.TempDir()
	p1, _ := r.Add(dir, "x")
	p2, _ := r.Add(dir, "y") // same path, different name
	if p1.Name != p2.Name {
		t.Fatalf("re-add changed name: %q vs %q", p1.Name, p2.Name)
	}
	if len(r.List()) != 1 {
		t.Fatalf("List len = %d, want 1", len(r.List()))
	}
}

// TestNameCollisionDedupe verifies that two different paths requesting the same
// name get distinct names.
func TestNameCollisionDedupe(t *testing.T) {
	r := NewMemory()
	a := t.TempDir()
	b := t.TempDir()
	p1, _ := r.Add(a, "proj")
	p2, _ := r.Add(b, "proj")
	if p1.Name == p2.Name {
		t.Fatalf("expected distinct names, both %q", p1.Name)
	}
	if p2.Name != "proj-2" {
		t.Fatalf("second name = %q, want proj-2", p2.Name)
	}
}

// TestEnsureWorkspaceAutoRegisters verifies auto-registration of an unknown
// workspace and idempotence on repeat.
func TestEnsureWorkspaceAutoRegisters(t *testing.T) {
	r := NewMemory()
	dir := t.TempDir()
	p, err := r.EnsureWorkspace(dir)
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if p.Path != dir {
		t.Fatalf("path = %q, want %q", p.Path, dir)
	}
	if _, err := r.EnsureWorkspace(dir); err != nil {
		t.Fatalf("EnsureWorkspace repeat: %v", err)
	}
	if len(r.List()) != 1 {
		t.Fatalf("List len = %d, want 1 (idempotent)", len(r.List()))
	}
}

// TestRemove verifies a project can be deregistered.
func TestRemove(t *testing.T) {
	r := NewMemory()
	dir := t.TempDir()
	p, _ := r.Add(dir, "gone")
	if err := r.Remove(p.Name); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := r.Resolve(p.Name); ok {
		t.Fatal("project still resolvable after Remove")
	}
}
