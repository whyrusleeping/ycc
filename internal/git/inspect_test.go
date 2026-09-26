package git

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectChangesReadOnlyScopedAndStale(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"user.txt": "old\n", "task.txt": "old\n"})
	writeFile(t, filepath.Join(r.Dir, "user.txt"), "baseline dirty\n")
	b, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PersistBaseline("s_test", b); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "new\n")
	writeFile(t, filepath.Join(r.Dir, "large.txt"), strings.Repeat("added\n", 200000))
	index := readIndex(t, r)
	c, excluded, err := r.InspectChanges(b)
	if err != nil {
		t.Fatal(err)
	}
	if excluded != 1 || len(c.Paths) != 2 || !strings.Contains(c.Diff, "+added") || strings.Contains(c.Diff, "baseline dirty") {
		t.Fatalf("scope=%v excluded=%d", c.Paths, excluded)
	}
	if !bytes.Equal(index, readIndex(t, r)) {
		t.Fatal("real index modified")
	}
	record := filepath.Join(r.Dir, ".git", "ycc", "changeset-records", c.ID+".json")
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("inspection retained changeset record: %v", err)
	}
	if out, err := r.run("show-ref", "--verify", "refs/ycc/changesets/"+c.ID); err == nil {
		t.Fatalf("inspection retained ref: %s", out)
	}
	if _, err := r.LoadBaselineReadOnly("s_test"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "user.txt"), "overlapping edit\n")
	if _, _, err := r.InspectChanges(b); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected baseline ownership overlap: %v", err)
	}
	writeFile(t, filepath.Join(r.Dir, "user.txt"), "baseline dirty\n")
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "newer\n")
	next, _, err := r.InspectChanges(b)
	if err != nil || next.ID == c.ID {
		t.Fatalf("edit did not change snapshot: %v", err)
	}
	commitAllForTest(t, r, "moved")
	if _, _, err := r.InspectChanges(b); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected stale baseline: %v", err)
	}
}
