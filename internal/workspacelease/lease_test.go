package workspacelease

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCanonicalAliasAndScopedReentrancy(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	s := NewService()
	worker := s.NewToken("session one implementer")
	first, err := s.Acquire(root, worker)
	if err != nil {
		t.Fatal(err)
	}
	reentrant, err := s.Acquire(alias, worker)
	if err != nil {
		t.Fatalf("worker's own command did not reenter: %v", err)
	}
	other := s.NewToken("session two coordinator")
	if _, err := s.Acquire(alias, other); err == nil {
		t.Fatal("path alias admitted a second session")
	} else {
		var conflict *Conflict
		if !errors.As(err, &conflict) || conflict.Owner != worker.Owner() {
			t.Fatalf("conflict = %v", err)
		}
		for _, hint := range []string{"session one implementer", "wait", "stop", "workstream"} {
			if !strings.Contains(err.Error(), hint) {
				t.Fatalf("conflict %q missing %q", err, hint)
			}
		}
	}
	first.Release()
	if _, err := s.Acquire(root, other); err == nil {
		t.Fatal("release of one reentrant claim released the scope too early")
	}
	reentrant.Release()
	lease, err := s.Acquire(root, other)
	if err != nil {
		t.Fatalf("acquire after final release: %v", err)
	}
	lease.Release()
}

func TestAsyncChildBlocksParentReentryUntilRelease(t *testing.T) {
	root := t.TempDir()
	s := NewService()
	token := s.NewToken("session one implementer")
	lifetime, err := s.Acquire(root, token)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.AcquireChild(root, token, "session one implementer background Bash")
	if err != nil {
		t.Fatal(err)
	}
	lifetime.Release()
	if _, err := s.Acquire(root, token); err == nil || !strings.Contains(err.Error(), "background Bash") {
		t.Fatalf("parent reentry while child runs = %v", err)
	}
	if _, err := s.AcquireChild(root, token, "second child"); err == nil {
		t.Fatal("concurrent asynchronous child acquired the worktree")
	}
	child.Release()
	lease, err := s.Acquire(root, token)
	if err != nil {
		t.Fatalf("parent acquire after child exit: %v", err)
	}
	lease.Release()
}

func TestCanonicalUsesGitWorktreeTopLevel(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	if err := os.MkdirAll(left, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(right, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewService()
	lease, err := s.Acquire(left, s.NewToken("left project"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := s.Acquire(right, s.NewToken("right project")); err == nil {
		t.Fatal("subdirectories of one Git worktree acquired independent leases")
	}
}

func TestCanonicalContainingUsesNonexistentDestinationWorktree(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	s := NewService()
	lease, err := s.Acquire(root, s.NewToken("worktree owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	missing := filepath.Join(root, "new", "nested", "file.txt")
	if _, err := s.AcquirePath(missing, root, s.NewToken("destination writer")); err == nil {
		t.Fatal("nonexistent destination did not resolve to its containing worktree")
	}
}

func TestAcquisitionAtomicAndSeparateWorktrees(t *testing.T) {
	s := NewService()
	root := t.TempDir()
	otherRoot := t.TempDir()
	start := make(chan struct{})
	results := make(chan *Lease, 2)
	var wg sync.WaitGroup
	for _, owner := range []string{"one", "two"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			<-start
			lease, _ := s.Acquire(root, s.NewToken(owner))
			results <- lease
		}(owner)
	}
	close(start)
	wg.Wait()
	close(results)
	var won int
	for lease := range results {
		if lease != nil {
			won++
			lease.Release()
		}
	}
	if won != 1 {
		t.Fatalf("atomic contenders admitted %d owners, want 1", won)
	}

	one, err := s.Acquire(root, s.NewToken("root one"))
	if err != nil {
		t.Fatal(err)
	}
	defer one.Release()
	two, err := s.Acquire(otherRoot, s.NewToken("root two"))
	if err != nil {
		t.Fatalf("separate worktree was blocked: %v", err)
	}
	two.Release()
}
