//go:build !unix

package daemon

// Non-Unix targets run without the cross-process daemon lock. The project
// currently targets Unix platforms, where lock_unix.go adds flock.
func acquireStateLock(string) (func(), error) {
	return func() {}, nil
}
