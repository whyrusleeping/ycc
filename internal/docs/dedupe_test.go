package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRawTask drops a task file into the backlog dir behind the Store's back,
// the way a git merge of two branches does.
func writeRawTask(t *testing.T, ws, name, id, title, created string) string {
	t.Helper()
	dir := filepath.Join(ws, "backlog")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	content := "---\n" +
		"id: \"" + id + "\"\n" +
		"title: " + title + "\n" +
		"status: todo\n" +
		"priority: 3\n" +
		"created: " + created + "\n" +
		"updated: " + created + "\n" +
		"---\n\n## Description\n" + title + "\n\n## Work log\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListHealsDuplicateIDs(t *testing.T) {
	ws := t.TempDir()
	older := writeRawTask(t, ws, "0195-alpha.md", "0195", "alpha", "2026-07-01")
	newer := writeRawTask(t, ws, "0195-beta.md", "0195", "beta", "2026-07-20")
	writeRawTask(t, ws, "0196-gamma.md", "0196", "gamma", "2026-07-02")

	s := NewStore(ws)
	tasks, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks, want 3", len(tasks))
	}
	if hasDuplicateIDs(tasks) {
		t.Fatalf("duplicate ids survived List: %v", ids(tasks))
	}
	// The older claimant keeps 0195; the younger one moves to max+1 = 0197.
	got, err := s.Get("0195")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "alpha" {
		t.Fatalf("0195 = %q, want alpha (oldest claimant keeps the id)", got.Title)
	}
	moved, err := s.Get("0197")
	if err != nil {
		t.Fatalf("renumbered task not reachable: %v", err)
	}
	if moved.Title != "beta" {
		t.Fatalf("0197 = %q, want beta", moved.Title)
	}
	if !strings.Contains(moved.Body, "renumbered 0195 → 0197") {
		t.Fatalf("no work-log breadcrumb:\n%s", moved.Body)
	}
	// The file was renamed, not copied.
	if _, err := os.Stat(newer); !os.IsNotExist(err) {
		t.Fatalf("old file %s still present", newer)
	}
	if _, err := os.Stat(older); err != nil {
		t.Fatalf("survivor's file gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "backlog", "0197-beta.md")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}
	// Healing is idempotent: a second pass changes nothing.
	changes, err := s.DedupeIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("second pass renumbered again: %v", changes)
	}
}

func TestDedupeIDsReportsChangesAndHandlesTriples(t *testing.T) {
	ws := t.TempDir()
	writeRawTask(t, ws, "0007-a.md", "0007", "a", "2026-01-01")
	writeRawTask(t, ws, "0007-b.md", "0007", "b", "2026-01-02")
	writeRawTask(t, ws, "0007-c.md", "0007", "c", "2026-01-03")

	s := NewStore(ws)
	changes, err := s.DedupeIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %v, want 2", changes)
	}
	if changes[0].NewID != "0008" || changes[1].NewID != "0009" {
		t.Fatalf("new ids = %v", changes)
	}
	if changes[0].Title != "b" || changes[1].Title != "c" {
		t.Fatalf("renumbered wrong tasks: %v", changes)
	}
	tasks, _ := s.List()
	if got := strings.Join(ids(tasks), ","); got != "0007,0008,0009" {
		t.Fatalf("ids = %s", got)
	}
}

// A duplicate id must not derail the next Create: the fresh id continues after
// the highest id in use once the collision is resolved.
func TestCreateAfterDuplicateHealsAndAssignsFreshID(t *testing.T) {
	ws := t.TempDir()
	writeRawTask(t, ws, "0003-a.md", "0003", "a", "2026-01-01")
	writeRawTask(t, ws, "0003-b.md", "0003", "b", "2026-01-02")

	s := NewStore(ws)
	created, err := s.Create("new work", "", 3, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "0005" {
		t.Fatalf("new id = %s, want 0005 (0003 kept, 0004 taken by the healed duplicate)", created.ID)
	}
	tasks, _ := s.List()
	if hasDuplicateIDs(tasks) {
		t.Fatalf("duplicates after create: %v", ids(tasks))
	}
}

// An unpadded `id: "195"` is the same task id as "0195" and must be healed too.
func TestUnpaddedIDNormalizesAndDedupes(t *testing.T) {
	ws := t.TempDir()
	writeRawTask(t, ws, "0195-alpha.md", "0195", "alpha", "2026-07-01")
	writeRawTask(t, ws, "195-beta.md", "195", "beta", "2026-07-05")

	s := NewStore(ws)
	tasks, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids(tasks), ","); got != "0195,0196" {
		t.Fatalf("ids = %s", got)
	}
}

func ids(tasks []*Task) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.ID)
	}
	return out
}
