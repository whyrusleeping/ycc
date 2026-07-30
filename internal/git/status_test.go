package git

import (
	"testing"
)

// TestStatusDirty verifies the local dirty flag reflects uncommitted changes and
// that a repo with no upstream reports HasUpstream=false with zero ahead/behind.
func TestStatusDirty(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Fresh repo (Open makes an initial commit) with no remote: clean, no upstream.
	s, err := r.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if s.Dirty {
		t.Fatalf("fresh repo reported dirty")
	}
	if s.HasUpstream {
		t.Fatalf("repo with no remote reported HasUpstream=true")
	}
	if s.Branch == "" {
		t.Fatalf("expected a branch name, got detached")
	}

	// Add an untracked file -> dirty.
	writeFile(t, dir+"/new.txt", "x\n")
	if s, _ = r.Status(); !s.Dirty {
		t.Fatalf("untracked file did not mark repo dirty")
	}
}

// TestStatusAheadBehind sets up a local "remote" clone and verifies ahead/behind
// counts against the upstream tracking branch after diverging commits.
func TestStatusAheadBehind(t *testing.T) {
	remoteDir := t.TempDir()
	// Bare remote.
	gitAt(t, remoteDir, "init", "--bare", "--initial-branch=main")

	workDir := t.TempDir()
	gitAt(t, workDir, "init", "--initial-branch=main")
	gitAt(t, workDir, "config", "user.email", "t@t")
	gitAt(t, workDir, "config", "user.name", "t")
	writeFile(t, workDir+"/a.txt", "1\n")
	gitAt(t, workDir, "add", "-A")
	gitAt(t, workDir, "commit", "-m", "c1")
	gitAt(t, workDir, "remote", "add", "origin", remoteDir)
	gitAt(t, workDir, "push", "-u", "origin", "main")

	r := &Repo{Dir: workDir}

	// In sync.
	s, err := r.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !s.HasUpstream || s.Ahead != 0 || s.Behind != 0 {
		t.Fatalf("in-sync = %+v, want upstream with 0/0", s)
	}

	// Local commit -> ahead 1.
	writeFile(t, workDir+"/b.txt", "2\n")
	gitAt(t, workDir, "add", "-A")
	gitAt(t, workDir, "commit", "-m", "c2")
	if s, _ = r.Status(); s.Ahead != 1 || s.Behind != 0 {
		t.Fatalf("after local commit = %+v, want ahead 1 behind 0", s)
	}

	// Advance the remote via a second clone, then Fetch and expect behind.
	otherDir := t.TempDir()
	gitAt(t, otherDir, "clone", remoteDir, ".")
	gitAt(t, otherDir, "config", "user.email", "t@t")
	gitAt(t, otherDir, "config", "user.name", "t")
	writeFile(t, otherDir+"/c.txt", "3\n")
	gitAt(t, otherDir, "add", "-A")
	gitAt(t, otherDir, "commit", "-m", "c3")
	gitAt(t, otherDir, "push", "origin", "main")

	if err := r.Fetch(); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if s, _ = r.Status(); s.Ahead != 1 || s.Behind != 1 {
		t.Fatalf("after remote advance = %+v, want ahead 1 behind 1", s)
	}
}
