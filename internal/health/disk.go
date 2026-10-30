package health

import (
	"os"

	"golang.org/x/sys/unix"
)

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func deviceID(dir string) (uint64, bool) {
	var st unix.Stat_t
	if err := unix.Stat(dir, &st); err != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

// freeBytes is the space available to the daemon's (unprivileged) user.
func freeBytes(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
