//go:build !unix

package secrets

// Non-Unix targets retain process-local serialization through mutationMu. The
// project currently targets Unix platforms, where lock_unix.go adds flock.
func acquireFileLock(string) (func(), error) {
	return func() {}, nil
}
