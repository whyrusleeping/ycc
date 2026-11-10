package git

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
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
	if overlap, _, err := r.InspectChanges(b); err != nil || !reflect.DeepEqual(overlap.Preexisting, []string{"user.txt"}) {
		t.Fatalf("changed baseline-dirty file not included whole: %+v, %v", overlap, err)
	}
	writeFile(t, filepath.Join(r.Dir, "user.txt"), "baseline dirty\n")
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "newer\n")
	next, _, err := r.InspectChanges(b)
	if err != nil || next.ID == c.ID {
		t.Fatalf("edit did not change snapshot: %v", err)
	}
	commitAllForTest(t, r, "moved")
	// A fast-forward HEAD move (another session committed) is absorbed: the
	// committed paths become clean baseline state rather than a stale error.
	moved, _, err := r.InspectChanges(b)
	if err != nil || len(moved.Paths) != 0 || moved.BaselineOrigin != b.ID || moved.BaselineID == b.ID {
		t.Fatalf("fast-forward rebase: %+v, %v", moved, err)
	}
	tree := gitAt(t, r.Dir, "rev-parse", "HEAD^{tree}")
	unrelated := gitAt(t, r.Dir, "commit-tree", tree, "-m", "unrelated history")
	gitAt(t, r.Dir, "update-ref", "HEAD", unrelated)
	// A move outside the baseline's history re-anchors the baseline instead of
	// going stale, and is reported.
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "after rewrite\n")
	rewritten, _, err := r.InspectChanges(b)
	if err != nil || rewritten.MovedFrom == "" || !reflect.DeepEqual(rewritten.Paths, []string{"task.txt"}) {
		t.Fatalf("non-fast-forward re-anchor: %+v, %v", rewritten, err)
	}
}
