package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitAt runs a git command in dir and fails the test on error.
func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeFile writes content to path, creating parent dirs.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// baseBranch returns the current branch name of the repo.
func baseBranch(t *testing.T, r *Repo) string {
	t.Helper()
	return gitAt(t, r.Dir, "rev-parse", "--abbrev-ref", "HEAD")
}

func TestWorktreeLifecycle(t *testing.T) {
	base := t.TempDir()
	r, err := Open(base)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Seed a tracked file on base so branches have content to diverge from.
	writeFile(t, filepath.Join(base, "file.txt"), "line1\nline2\n")
	if _, err := r.Commit("seed file"); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	baseName := baseBranch(t, r)

	// --- AddWorktree + ListWorktrees ---
	wtDir := filepath.Join(t.TempDir(), "ws")
	branch := "ycc/ws/test-0001"
	if err := r.AddWorktree(wtDir, branch, "HEAD"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	trees, err := r.ListWorktrees()
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(trees) != 2 {
		t.Fatalf("expected 2 worktrees, got %d: %+v", len(trees), trees)
	}
	found := false
	for _, wt := range trees {
		if strings.HasSuffix(wt.Branch, branch) {
			found = true
		}
	}
	if !found {
		t.Fatalf("worktree branch %q not listed: %+v", branch, trees)
	}

	// --- commit a change on the branch inside the worktree ---
	writeFile(t, filepath.Join(wtDir, "feature.txt"), "hello from branch\n")
	gitAt(t, wtDir, "add", "-A")
	gitAt(t, wtDir, "commit", "-m", "add feature")

	// --- TrialMerge (clean) does not mutate base ---
	headBefore := gitAt(t, base, "rev-parse", "HEAD")
	res, err := r.TrialMerge(branch)
	if err != nil {
		t.Fatalf("TrialMerge (clean): %v", err)
	}
	if !res.Clean {
		t.Fatalf("expected clean trial merge, got conflicts %v", res.Conflicts)
	}
	if got := gitAt(t, base, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("base HEAD mutated by trial merge: %s -> %s", headBefore, got)
	}
	if status := gitAt(t, base, "status", "--porcelain"); status != "" {
		t.Fatalf("base working tree dirty after trial merge: %q", status)
	}
	// After TrialMerge the throwaway worktree should be gone.
	if trees, _ := r.ListWorktrees(); len(trees) != 2 {
		t.Fatalf("trial merge leaked worktree: %+v", trees)
	}

	// --- Merge (clean) integrates the branch ---
	mres, err := r.Merge(branch, MergeNoFF)
	if err != nil {
		t.Fatalf("Merge (clean): %v", err)
	}
	if !mres.Clean || mres.Commit == "" {
		t.Fatalf("expected clean merge with commit, got %+v", mres)
	}
	if _, err := os.Stat(filepath.Join(base, "feature.txt")); err != nil {
		t.Fatalf("base missing merged file: %v", err)
	}
	if got := gitAt(t, base, "rev-parse", "--abbrev-ref", "HEAD"); got != baseName {
		t.Fatalf("merge changed base branch: %s -> %s", baseName, got)
	}

	// --- RemoveWorktree + PruneWorktrees ---
	if err := r.RemoveWorktree(wtDir); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if err := r.PruneWorktrees(); err != nil {
		t.Fatalf("PruneWorktrees: %v", err)
	}
	trees, err = r.ListWorktrees()
	if err != nil {
		t.Fatalf("ListWorktrees after cleanup: %v", err)
	}
	if len(trees) != 1 {
		t.Fatalf("expected only primary tree after cleanup, got %+v", trees)
	}
}

func TestTrialMergeAndMergeConflict(t *testing.T) {
	base := t.TempDir()
	r, err := Open(base)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	writeFile(t, filepath.Join(base, "file.txt"), "original\n")
	if _, err := r.Commit("seed file"); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	// Branch that edits file.txt.
	wtDir := filepath.Join(t.TempDir(), "ws")
	branch := "ycc/ws/conflict"
	if err := r.AddWorktree(wtDir, branch, "HEAD"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	writeFile(t, filepath.Join(wtDir, "file.txt"), "branch change\n")
	gitAt(t, wtDir, "add", "-A")
	gitAt(t, wtDir, "commit", "-m", "branch edit")

	// Conflicting edit to the same file on base.
	writeFile(t, filepath.Join(base, "file.txt"), "base change\n")
	if _, err := r.Commit("base edit"); err != nil {
		t.Fatalf("base commit: %v", err)
	}

	headBefore := gitAt(t, base, "rev-parse", "HEAD")

	// TrialMerge reports the conflict without mutating base.
	res, err := r.TrialMerge(branch)
	if err != nil {
		t.Fatalf("TrialMerge (conflict): %v", err)
	}
	if res.Clean {
		t.Fatal("expected conflicting trial merge, got clean")
	}
	if len(res.Conflicts) == 0 || res.Conflicts[0] != "file.txt" {
		t.Fatalf("expected file.txt conflict, got %v", res.Conflicts)
	}
	if got := gitAt(t, base, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("base HEAD mutated by trial merge: %s -> %s", headBefore, got)
	}
	if status := gitAt(t, base, "status", "--porcelain"); status != "" {
		t.Fatalf("base working tree dirty after trial merge: %q", status)
	}

	// Merge reports the conflict and leaves base clean (aborted).
	mres, err := r.Merge(branch, MergeNoFF)
	if err != nil {
		t.Fatalf("Merge (conflict): %v", err)
	}
	if mres.Clean {
		t.Fatal("expected conflicting merge, got clean")
	}
	if len(mres.Conflicts) == 0 || mres.Conflicts[0] != "file.txt" {
		t.Fatalf("expected file.txt conflict, got %v", mres.Conflicts)
	}
	if got := gitAt(t, base, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("base HEAD changed by aborted merge: %s -> %s", headBefore, got)
	}
	if status := gitAt(t, base, "status", "--porcelain"); status != "" {
		t.Fatalf("base left mid-merge/dirty after conflict: %q", status)
	}
}

func TestCountCommits(t *testing.T) {
	base := t.TempDir()
	r, err := Open(base)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	writeFile(t, filepath.Join(base, "file.txt"), "seed\n")
	if _, err := r.Commit("seed file"); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	baseCommit := gitAt(t, base, "rev-parse", "HEAD")

	wtDir := filepath.Join(t.TempDir(), "ws")
	branch := "ycc/ws/count"
	if err := r.AddWorktree(wtDir, branch, "HEAD"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// No commits yet on the branch.
	if n, err := r.CountCommits(baseCommit, branch); err != nil || n != 0 {
		t.Fatalf("CountCommits (empty) = %d, %v; want 0, nil", n, err)
	}

	// Two commits on the branch.
	writeFile(t, filepath.Join(wtDir, "a.txt"), "a\n")
	gitAt(t, wtDir, "add", "-A")
	gitAt(t, wtDir, "commit", "-m", "add a")
	writeFile(t, filepath.Join(wtDir, "b.txt"), "b\n")
	gitAt(t, wtDir, "add", "-A")
	gitAt(t, wtDir, "commit", "-m", "add b")

	if n, err := r.CountCommits(baseCommit, branch); err != nil || n != 2 {
		t.Fatalf("CountCommits (two) = %d, %v; want 2, nil", n, err)
	}
}
