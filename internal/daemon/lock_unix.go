//go:build unix

package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// acquireStateLock takes the exclusive, non-blocking daemon lock at path
// (<state-dir>/daemon.lock) so two persistent daemons never share a state
// directory. The lock lives as long as the returned release, or the process.
func acquireStateLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create daemon state dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("another ycc daemon already owns %s", filepath.Dir(path))
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
