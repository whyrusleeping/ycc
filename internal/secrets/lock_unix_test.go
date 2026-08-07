//go:build unix

package secrets

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileLockContention(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "secrets.lock")
	unlockFirst, err := acquireFileLock(lockPath)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	firstLocked := true
	defer func() {
		if firstLocked {
			unlockFirst()
		}
	}()

	type result struct {
		unlock func()
		err    error
	}
	started := make(chan struct{})
	acquired := make(chan result, 1)
	go func() {
		close(started)
		unlock, err := acquireFileLock(lockPath)
		acquired <- result{unlock: unlock, err: err}
	}()
	<-started

	select {
	case second := <-acquired:
		if second.unlock != nil {
			second.unlock()
		}
		t.Fatalf("second lock acquired while first was held (error %v)", second.err)
	case <-time.After(150 * time.Millisecond):
		// The second independent open is blocked by the first flock.
	}

	unlockFirst()
	firstLocked = false
	select {
	case second := <-acquired:
		if second.err != nil {
			t.Fatalf("acquire second lock after release: %v", second.err)
		}
		second.unlock()
	case <-time.After(2 * time.Second):
		t.Fatal("second lock remained blocked after first was released")
	}

	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("Stat lock file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("lock file perm = %o, want 600", perm)
	}
}
