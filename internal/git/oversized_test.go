package git

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func lowerSnapshotLimit(t *testing.T, limit int64) {
	t.Helper()
	old := maxUntrackedSnapshotBytes
	maxUntrackedSnapshotBytes = limit
	t.Cleanup(func() { maxUntrackedSnapshotBytes = old })
}

// assertNoObject fails if path's content was hashed into r's object store.
func assertNoObject(t *testing.T, r *Repo, path string) {
	t.Helper()
	sha := gitAt(t, r.Dir, "hash-object", "--", path)
	if err := exec.Command("git", "-C", r.Dir, "cat-file", "-e", sha).Run(); err == nil {
		t.Fatalf("%s (%s) was written to the object store", path, sha)
	}
}

func TestSnapshotsLeaveOutOversizedUntrackedFiles(t *testing.T) {
	lowerSnapshotLimit(t, 64)
	big := strings.Repeat("x", 100) + "\n"
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"tracked.bin": big})
	writeFile(t, filepath.Join(r.Dir, "data", "huge.csv"), big+"untracked\n")
	writeFile(t, filepath.Join(r.Dir, "staged.bin"), big+"staged\n")
	gitAt(t, r.Dir, "add", "staged.bin")

	baseline, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := baseline.Oversized(), []string{"data/huge.csv"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("baseline oversized = %q, want %q", got, want)
	}
	assertNoObject(t, r, "data/huge.csv")

	// Tracked and already-staged large files are still snapshotted; a new
	// oversized untracked file is reported, never included.
	writeFile(t, filepath.Join(r.Dir, "tracked.bin"), big+"edit\n")
	writeFile(t, filepath.Join(r.Dir, "small.txt"), "small\n")
	writeFile(t, filepath.Join(r.Dir, "out.bin"), big+"generated\n")
	changes, err := r.Changes(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"small.txt", "tracked.bin"}; !reflect.DeepEqual(changes.Paths, want) {
		t.Fatalf("paths = %q, want %q", changes.Paths, want)
	}
	if want := []string{"data/huge.csv", "out.bin"}; !reflect.DeepEqual(changes.Oversized, want) {
		t.Fatalf("changeset oversized = %q, want %q", changes.Oversized, want)
	}
	assertNoObject(t, r, "out.bin")
}

func TestInitialCommitLeavesOutOversizedUntrackedFiles(t *testing.T) {
	lowerSnapshotLimit(t, 64)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "huge.csv"), strings.Repeat("x", 100))
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitAt(t, dir, "ls-tree", "-r", "--name-only", "HEAD"); strings.Contains(got, "huge.csv") || !strings.Contains(got, "main.go") {
		t.Fatalf("initial commit tree = %q", got)
	}
	if got, want := r.OpenBaseline().Oversized(), []string{"huge.csv"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("baseline oversized = %q, want %q", got, want)
	}
	assertNoObject(t, r, "huge.csv")
}
