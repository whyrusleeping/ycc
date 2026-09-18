//go:build !unix

package git

// Snapshot writers remain available on platforms without flock, but destructive
// cleanup refuses to run because it cannot be serialized against those writers.
func snapshotFileLockSupported() bool { return false }

func acquireSnapshotFileLock(string) (func(), error) {
	return func() {}, nil
}
