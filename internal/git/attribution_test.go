package git

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Two scopes share one worktree: each changeset holds only its own writes plus
// unclaimed output, the other scope's commit does not invalidate the
// survivor's baseline, and both commits land on one linear history.
func TestChangesForSharedWorktree(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"x.go": "x0\n", "shared.go": "s0\n"})
	baseA, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	// A has in-progress work before B's session starts.
	writeFile(t, filepath.Join(r.Dir, "x.go"), "x-a1\n")
	baseB, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(r.Dir, "x.go"), "x-a2\n")      // A keeps editing its pre-dirty file
	writeFile(t, filepath.Join(r.Dir, "a.go"), "a\n")         // A only
	writeFile(t, filepath.Join(r.Dir, "b.go"), "b\n")         // B only
	writeFile(t, filepath.Join(r.Dir, "shared.go"), "s-ab\n") // both
	writeFile(t, filepath.Join(r.Dir, "gen.txt"), "shell\n")  // unclaimed

	attrB := &Attribution{
		Mine:    map[string]bool{"b.go": true, "shared.go": true},
		Foreign: map[string]bool{"x.go": true, "a.go": true, "shared.go": true},
	}
	cb, err := r.ChangesFor(baseB, attrB)
	if err != nil {
		t.Fatalf("B changeset refused despite foreign edits: %v", err)
	}
	if want := []string{"b.go", "gen.txt", "shared.go"}; !reflect.DeepEqual(cb.Paths, want) {
		t.Fatalf("B paths = %v, want %v", cb.Paths, want)
	}
	if !reflect.DeepEqual(cb.Foreign, []string{"a.go"}) || !reflect.DeepEqual(cb.Shared, []string{"shared.go"}) ||
		!reflect.DeepEqual(cb.Deferred, []string{"x.go"}) || len(cb.Preexisting) != 0 {
		t.Fatalf("B attribution foreign=%v shared=%v deferred=%v preexisting=%v", cb.Foreign, cb.Shared, cb.Deferred, cb.Preexisting)
	}
	// Strict (unattributed) inspection keeps refusing the ambiguous overlap.
	if _, err := r.Changes(baseB); err == nil || !strings.Contains(err.Error(), "already dirty") {
		t.Fatalf("strict changeset accepted ambiguous overlap: %v", err)
	}

	// A commits first.
	attrA := &Attribution{
		Mine:    map[string]bool{"x.go": true, "a.go": true, "shared.go": true},
		Foreign: map[string]bool{"b.go": true, "shared.go": true},
	}
	ca, err := r.ChangesFor(baseA, attrA)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.go", "gen.txt", "shared.go", "x.go"}; !reflect.DeepEqual(ca.Paths, want) {
		t.Fatalf("A paths = %v, want %v", ca.Paths, want)
	}
	if _, err := r.Commit(ca, "A work"); err != nil {
		t.Fatal(err)
	}

	// B's baseline predates A's commit; it is rebased instead of going stale.
	writeFile(t, filepath.Join(r.Dir, "b.go"), "b2\n")
	cb2, err := r.ChangesFor(baseB, attrB)
	if err != nil {
		t.Fatalf("B changeset after A's commit: %v", err)
	}
	if !reflect.DeepEqual(cb2.Paths, []string{"b.go"}) || cb2.BaselineOrigin != baseB.ID || len(cb2.Deferred) != 0 {
		t.Fatalf("B after rebase paths=%v origin=%s deferred=%v", cb2.Paths, cb2.BaselineOrigin, cb2.Deferred)
	}
	if strings.Contains(cb2.Diff, "x-a2") || strings.Contains(cb2.Diff, "s-ab") {
		t.Fatalf("B diff re-attributes A's committed work:\n%s", cb2.Diff)
	}
	if _, err := r.Commit(cb2, "B work"); err != nil {
		t.Fatal(err)
	}
	if got := gitAt(t, r.Dir, "status", "--porcelain"); got != "" {
		t.Fatalf("worktree not clean after both commits:\n%s", got)
	}
	if got := gitAt(t, r.Dir, "log", "--format=%s", "-3"); got != "B work\nA work\nseed" {
		t.Fatalf("history = %q", got)
	}
}

// A scope that edits a path already dirty at its baseline adopts it (the
// earlier changes ride along and are reported) instead of being refused.
func TestChangesForAdoptsOwnEditsToPreexistingPaths(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"p.go": "p0\n"})
	writeFile(t, filepath.Join(r.Dir, "p.go"), "p-before\n")
	base, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "p.go"), "p-before\np-mine\n")
	c, err := r.ChangesFor(base, &Attribution{Mine: map[string]bool{"p.go": true}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Paths, []string{"p.go"}) || !reflect.DeepEqual(c.Preexisting, []string{"p.go"}) {
		t.Fatalf("paths=%v preexisting=%v", c.Paths, c.Preexisting)
	}
	if _, err := r.Commit(c, "adopt"); err != nil {
		t.Fatal(err)
	}
}

// A user committing only part of a baseline-dirty file (its staged hunks) must
// not turn the leftover unstaged changes into this session's work.
func TestRebaseKeepsLeftoverOfPartiallyCommittedDirtyPath(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"p.go": "one\ntwo\n"})
	writeFile(t, filepath.Join(r.Dir, "p.go"), "ONE\nTWO\n")
	base, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "q.go"), "session work\n")
	// The user stages and commits only the first hunk from a terminal.
	writeFile(t, filepath.Join(r.Dir, "p.go"), "ONE\ntwo\n")
	gitAt(t, r.Dir, "add", "p.go")
	gitAt(t, r.Dir, "commit", "-m", "user partial")
	writeFile(t, filepath.Join(r.Dir, "p.go"), "ONE\nTWO\n")

	c, err := r.ChangesFor(base, &Attribution{Mine: map[string]bool{"q.go": true}, Foreign: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Paths, []string{"q.go"}) {
		t.Fatalf("paths = %v; the user's leftover p.go change was re-attributed", c.Paths)
	}
}

// Index entries staged by someone else while a commit's hooks run survive the
// commit's index publication.
func TestCommitPreservesIndexChangesMadeDuringHooks(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"task.txt": "old\n"})
	base, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "task.txt"), "new\n")
	writeFile(t, filepath.Join(r.Dir, "other.txt"), "someone else's staged work\n")
	hook := filepath.Join(r.Dir, ".git", "hooks", "pre-commit")
	writeFile(t, hook, "#!/bin/sh\nunset GIT_INDEX_FILE\ngit add other.txt\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := r.ChangesFor(base, &Attribution{Mine: map[string]bool{"task.txt": true}, Foreign: map[string]bool{"other.txt": true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(c, "task"); err != nil {
		t.Fatal(err)
	}
	if staged := gitAt(t, r.Dir, "diff", "--cached", "--name-only"); staged != "other.txt" {
		t.Fatalf("staged after commit = %q; concurrent git add was lost", staged)
	}
	if got := gitAt(t, r.Dir, "show", "--name-only", "--format=", "HEAD"); got != "task.txt" {
		t.Fatalf("commit files = %q", got)
	}
}

// A baseline-dirty path changed by an unclaimed writer (e.g. this session's own
// `go mod tidy` rewriting an already-dirty go.sum) is still refused as
// ambiguous rather than silently dropped from the commit.
func TestChangesForRefusesUnclaimedChangeToPreexistingPath(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedTracked(t, r, map[string]string{"go.sum": "a\n"})
	writeFile(t, filepath.Join(r.Dir, "go.sum"), "a\nuser\n")
	base, err := r.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(r.Dir, "go.sum"), "a\nuser\ntidy\n")
	_, err = r.ChangesFor(base, &Attribution{Mine: map[string]bool{}, Foreign: map[string]bool{}})
	if err == nil || !strings.Contains(err.Error(), "go.sum") {
		t.Fatalf("unclaimed change to a baseline-dirty path = %v", err)
	}
}
