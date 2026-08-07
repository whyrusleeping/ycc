package git

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newIntegrationRepo(t *testing.T) (*Repo, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r, baseBranch(t, r)
}

func commitFile(t *testing.T, r *Repo, dir, name, content, message string) string {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), content)
	gitAt(t, dir, "add", "-A")
	gitAt(t, dir, "commit", "-m", message)
	return gitAt(t, dir, "rev-parse", "HEAD")
}

func TestDefaultBranchResolution(t *testing.T) {
	t.Run("origin HEAD", func(t *testing.T) {
		r, _ := newIntegrationRepo(t)
		gitAt(t, r.Dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
		got, err := r.DefaultBranch()
		if err != nil || got != "trunk" {
			t.Fatalf("DefaultBranch = %q, %v; want trunk", got, err)
		}
	})

	t.Run("local main", func(t *testing.T) {
		r, current := newIntegrationRepo(t)
		gitAt(t, r.Dir, "branch", "-m", current, "topic")
		gitAt(t, r.Dir, "branch", "main")
		got, err := r.DefaultBranch()
		if err != nil || got != "main" {
			t.Fatalf("DefaultBranch = %q, %v; want main", got, err)
		}
	})

	t.Run("local master", func(t *testing.T) {
		r, current := newIntegrationRepo(t)
		gitAt(t, r.Dir, "branch", "-m", current, "topic")
		gitAt(t, r.Dir, "branch", "master")
		got, err := r.DefaultBranch()
		if err != nil || got != "master" {
			t.Fatalf("DefaultBranch = %q, %v; want master", got, err)
		}
	})

	t.Run("current branch", func(t *testing.T) {
		r, current := newIntegrationRepo(t)
		gitAt(t, r.Dir, "branch", "-m", current, "topic")
		got, err := r.DefaultBranch()
		if err != nil || got != "topic" {
			t.Fatalf("DefaultBranch = %q, %v; want topic", got, err)
		}
	})
}

func TestIsAncestor(t *testing.T) {
	r, base := newIntegrationRepo(t)
	before := gitAt(t, r.Dir, "rev-parse", base)
	commitFile(t, r, r.Dir, "next.txt", "next\n", "next")
	after := gitAt(t, r.Dir, "rev-parse", base)
	if ok, err := r.IsAncestor(before, after); err != nil || !ok {
		t.Fatalf("IsAncestor(before, after) = %v, %v; want true", ok, err)
	}
	if ok, err := r.IsAncestor(after, before); err != nil || ok {
		t.Fatalf("IsAncestor(after, before) = %v, %v; want false", ok, err)
	}
}

func TestRebaseOntoCleanAndConflict(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		r, base := newIntegrationRepo(t)
		wt := filepath.Join(t.TempDir(), "ws")
		if err := r.AddWorktree(wt, "feature", base); err != nil {
			t.Fatal(err)
		}
		defer r.RemoveWorktree(wt)
		commitFile(t, r, wt, "feature.txt", "feature\n", "feature")
		commitFile(t, r, r.Dir, "base.txt", "base\n", "base")

		res, err := r.RebaseOnto(wt, base)
		if err != nil || !res.Clean {
			t.Fatalf("RebaseOnto = %+v, %v", res, err)
		}
		if ok, err := r.IsAncestor(base, "feature"); err != nil || !ok {
			t.Fatalf("rebased feature is not descendant of base: %v, %v", ok, err)
		}
	})

	t.Run("conflict aborts", func(t *testing.T) {
		r, base := newIntegrationRepo(t)
		commitFile(t, r, r.Dir, "shared.txt", "shared\n", "shared")
		wt := filepath.Join(t.TempDir(), "ws")
		if err := r.AddWorktree(wt, "feature", base); err != nil {
			t.Fatal(err)
		}
		defer r.RemoveWorktree(wt)
		commitFile(t, r, wt, "shared.txt", "feature\n", "feature edit")
		before := gitAt(t, wt, "rev-parse", "HEAD")
		commitFile(t, r, r.Dir, "shared.txt", "base\n", "base edit")

		res, err := r.RebaseOnto(wt, base)
		if err != nil || res.Clean || len(res.Conflicts) == 0 || res.Conflicts[0] != "shared.txt" {
			t.Fatalf("RebaseOnto conflict = %+v, %v", res, err)
		}
		if got := gitAt(t, wt, "rev-parse", "HEAD"); got != before {
			t.Fatalf("rebase abort did not restore HEAD: %s -> %s", before, got)
		}
		if got := gitAt(t, wt, "status", "--porcelain"); got != "" {
			t.Fatalf("rebase abort left dirty worktree: %q", got)
		}
	})
}

func TestAdvanceBranchCheckedOutCleanAndDirty(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		r, base := newIntegrationRepo(t)
		wt := filepath.Join(t.TempDir(), "ws")
		if err := r.AddWorktree(wt, "feature", base); err != nil {
			t.Fatal(err)
		}
		defer r.RemoveWorktree(wt)
		want := commitFile(t, r, wt, "feature.txt", "feature\n", "feature")
		if err := r.CheckBaseClean(base); err != nil {
			t.Fatalf("CheckBaseClean(clean): %v", err)
		}

		got, err := r.AdvanceBranch(base, "feature")
		if err != nil {
			t.Fatalf("AdvanceBranch: %v", err)
		}
		if full := gitAt(t, r.Dir, "rev-parse", base); full != want {
			t.Fatalf("base = %s, want %s (short result %s)", full, want, got)
		}
		if _, err := os.Stat(filepath.Join(r.Dir, "feature.txt")); err != nil {
			t.Fatalf("checked-out base tree not updated: %v", err)
		}
	})

	t.Run("dirty", func(t *testing.T) {
		r, base := newIntegrationRepo(t)
		wt := filepath.Join(t.TempDir(), "ws")
		if err := r.AddWorktree(wt, "feature", base); err != nil {
			t.Fatal(err)
		}
		defer r.RemoveWorktree(wt)
		commitFile(t, r, wt, "feature.txt", "feature\n", "feature")
		before := gitAt(t, r.Dir, "rev-parse", base)
		dirty := filepath.Join(r.Dir, "dirty.txt")
		writeFile(t, dirty, "keep me\n")

		if err := r.CheckBaseClean(base); !errors.Is(err, ErrBaseTreeDirty) {
			t.Fatalf("CheckBaseClean error = %v, want ErrBaseTreeDirty", err)
		}
		_, err := r.AdvanceBranch(base, "feature")
		if !errors.Is(err, ErrBaseTreeDirty) {
			t.Fatalf("AdvanceBranch error = %v, want ErrBaseTreeDirty", err)
		}
		if got := gitAt(t, r.Dir, "rev-parse", base); got != before {
			t.Fatalf("dirty base ref changed: %s -> %s", before, got)
		}
		if got, err := os.ReadFile(dirty); err != nil || string(got) != "keep me\n" {
			t.Fatalf("dirty file changed: %q, %v", got, err)
		}
	})
}

func TestAdvanceBranchWhenBaseNotCheckedOut(t *testing.T) {
	r, base := newIntegrationRepo(t)
	wt := filepath.Join(t.TempDir(), "ws")
	if err := r.AddWorktree(wt, "feature", base); err != nil {
		t.Fatal(err)
	}
	defer r.RemoveWorktree(wt)
	want := commitFile(t, r, wt, "feature.txt", "feature\n", "feature")

	gitAt(t, r.Dir, "checkout", "-b", "unrelated")
	unrelatedBefore := gitAt(t, r.Dir, "rev-parse", "HEAD")
	if _, err := r.AdvanceBranch(base, "feature"); err != nil {
		t.Fatalf("AdvanceBranch: %v", err)
	}
	if got := gitAt(t, r.Dir, "rev-parse", base); got != want {
		t.Fatalf("base = %s, want %s", got, want)
	}
	if got := gitAt(t, r.Dir, "rev-parse", "HEAD"); got != unrelatedBefore {
		t.Fatalf("unrelated checkout moved: %s -> %s", unrelatedBefore, got)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("unrelated worktree was updated, stat err = %v", err)
	}
}

func TestAdvanceBranchCheckedOutInLinkedWorktree(t *testing.T) {
	r, original := newIntegrationRepo(t)
	gitAt(t, r.Dir, "branch", "integration-base")
	baseTree := filepath.Join(t.TempDir(), "base")
	if err := r.AddWorktree(baseTree, "base-holder", original); err != nil {
		t.Fatal(err)
	}
	// Replace the fresh branch with the integration base in this linked worktree.
	gitAt(t, baseTree, "checkout", "integration-base")
	gitAt(t, r.Dir, "branch", "-D", "base-holder")
	defer r.RemoveWorktree(baseTree)

	featureTree := filepath.Join(t.TempDir(), "feature")
	if err := r.AddWorktree(featureTree, "feature", "integration-base"); err != nil {
		t.Fatal(err)
	}
	defer r.RemoveWorktree(featureTree)
	want := commitFile(t, r, featureTree, "feature.txt", "feature\n", "feature")

	if _, err := r.AdvanceBranch("integration-base", "feature"); err != nil {
		t.Fatalf("AdvanceBranch: %v", err)
	}
	if got := gitAt(t, r.Dir, "rev-parse", "integration-base"); got != want {
		t.Fatalf("base = %s, want %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(baseTree, "feature.txt")); err != nil {
		t.Fatalf("linked base worktree not updated: %v", err)
	}
}

func TestAdvanceBranchRefusesNonAncestor(t *testing.T) {
	r, base := newIntegrationRepo(t)
	wt := filepath.Join(t.TempDir(), "ws")
	if err := r.AddWorktree(wt, "feature", base); err != nil {
		t.Fatal(err)
	}
	defer r.RemoveWorktree(wt)
	commitFile(t, r, wt, "feature.txt", "feature\n", "feature")
	commitFile(t, r, r.Dir, "base.txt", "base\n", "base")
	before := gitAt(t, r.Dir, "rev-parse", base)

	if _, err := r.AdvanceBranch(base, "feature"); err == nil {
		t.Fatal("AdvanceBranch unexpectedly accepted divergent history")
	}
	if got := gitAt(t, r.Dir, "rev-parse", base); got != before {
		t.Fatalf("base changed after non-ff refusal: %s -> %s", before, got)
	}
}
