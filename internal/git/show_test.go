package git

import (
	"strings"
	"testing"
)

// TestShow verifies the commit-diff helper returns a `git show` patch for a real
// sha and rejects anything that isn't a bare hex commit id (flag/ref injection).
func TestShow(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	writeFile(t, dir+"/hello.txt", "hello\nworld\n")
	sha, err := r.Commit("add hello")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	out, err := r.Show(sha)
	if err != nil {
		t.Fatalf("show %q: %v", sha, err)
	}
	if !strings.Contains(out, "diff --git") {
		t.Fatalf("show output missing diff header:\n%s", out)
	}
	if !strings.Contains(out, "hello.txt") {
		t.Fatalf("show output missing file path:\n%s", out)
	}

	// Non-hex arguments must be rejected before ever reaching git.
	for _, bad := range []string{"--all", "main", "HEAD", "", "abc", "a/b", sha + " --stat"} {
		if _, err := r.Show(bad); err == nil {
			t.Errorf("Show(%q) = nil error, want rejection", bad)
		}
	}
}
