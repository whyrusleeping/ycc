//go:build unix

package secrets

import (
	"os"

	"golang.org/x/sys/unix"
)

// acquireFileLock takes an exclusive advisory lock on path. Each caller opens
// the file independently, so flock serializes both separate processes and
// separate callers in one process.
func acquireFileLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
