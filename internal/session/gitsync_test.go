package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
)

func TestProjectGitStatusAndFetchFailure(t *testing.T) {
	remote := t.TempDir()
	sessionGitAt(t, remote, "init", "--bare", "--initial-branch=main")

	seed := t.TempDir()
	sessionGitAt(t, seed, "init", "--initial-branch=main")
	sessionGitAt(t, seed, "config", "user.email", "t@t")
	sessionGitAt(t, seed, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionGitAt(t, seed, "add", "-A")
	sessionGitAt(t, seed, "commit", "-m", "base")
	sessionGitAt(t, seed, "remote", "add", "origin", remote)
	sessionGitAt(t, seed, "push", "-u", "origin", "main")

	cloneParent := t.TempDir()
	checkout := filepath.Join(cloneParent, "checkout")
	sessionGitAt(t, cloneParent, "clone", remote, checkout)
	sessionGitAt(t, checkout, "config", "user.email", "t@t")
	sessionGitAt(t, checkout, "config", "user.name", "t")

	reg := config.NewRegistry(&config.Config{})
	m := NewManager(reg, checkout)
	defer m.ReclaimAll()
	m.SetGitSyncInterval(20 * time.Millisecond)

	waitForGitStatus(t, m, checkout, func(s *ProjectGitStatus) bool {
		return !s.LastFetch.IsZero() && s.FetchError == ""
	})

	if err := os.WriteFile(filepath.Join(checkout, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := m.ProjectGitStatus(checkout); got == nil || !got.Dirty {
		t.Fatalf("dirty status = %+v, want dirty", got)
	}
	sessionGitAt(t, checkout, "add", "-A")
	sessionGitAt(t, checkout, "commit", "-m", "local")

	otherParent := t.TempDir()
	other := filepath.Join(otherParent, "other")
	sessionGitAt(t, otherParent, "clone", remote, other)
	sessionGitAt(t, other, "config", "user.email", "t@t")
	sessionGitAt(t, other, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(other, "remote.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionGitAt(t, other, "add", "-A")
	sessionGitAt(t, other, "commit", "-m", "remote")
	sessionGitAt(t, other, "push", "origin", "main")

	waitForGitStatus(t, m, checkout, func(s *ProjectGitStatus) bool {
		return s.HasUpstream && s.Ahead == 1 && s.Behind == 1 && !s.Dirty
	})

	missingRemote := filepath.Join(t.TempDir(), "missing.git")
	sessionGitAt(t, checkout, "remote", "set-url", "origin", missingRemote)
	failed := waitForGitStatus(t, m, checkout, func(s *ProjectGitStatus) bool {
		return s.FetchError != ""
	})
	if failed.LastFetch.IsZero() {
		t.Fatal("failed fetch discarded last successful fetch timestamp")
	}

	if got := m.ProjectGitStatus(t.TempDir()); got != nil {
		t.Fatalf("non-repository status = %+v, want nil", got)
	}
}

func waitForGitStatus(t *testing.T, m *Manager, path string, ok func(*ProjectGitStatus) bool) *ProjectGitStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := m.ProjectGitStatus(path)
		if status != nil && ok(status) {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := m.ProjectGitStatus(path)
	t.Fatalf("timed out waiting for git status; last = %+v", status)
	return nil
}
