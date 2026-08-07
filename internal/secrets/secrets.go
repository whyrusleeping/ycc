// Package secrets persists LLM backend API tokens in a restricted-perms file
// under the user config dir, so a token can be saved once instead of requiring
// the env var to be present in every session. Secrets are machine-local and are
// NEVER written to the project ycc.toml (which is checked into repos); they live
// in a dedicated secrets.json (mode 0600) keyed by the backend's key_env name.
package secrets

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// mutationMu serializes complete read-modify-write transactions within this
// process. The sibling file lock provides the corresponding cross-process
// serialization.
var mutationMu sync.Mutex

// createTempFile is a variable so tests can exercise failures after a valid
// store already exists. Production always leaves it set to os.CreateTemp.
var createTempFile = os.CreateTemp

// Store maps a key_env name to its stored API token.
type Store struct {
	Tokens map[string]string `json:"tokens"`
}

// Path returns the secrets file location (best-effort; "" on error).
func Path() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "ycc", "secrets.json")
}

// Load reads the persisted secrets. A missing file yields an empty store and a
// nil error; other read/parse errors are returned. Tokens is always non-nil.
func Load() (*Store, error) {
	return load(Path())
}

func load(fp string) (*Store, error) {
	s := &Store{Tokens: map[string]string{}}
	if fp == "" {
		return s, nil
	}
	data, err := os.ReadFile(fp)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	if s.Tokens == nil {
		s.Tokens = map[string]string{}
	}
	return s, nil
}

// Save atomically writes the store to the secrets file with restrictive
// permissions. It serializes with Set and Remove in this and other processes.
func (s *Store) Save() error {
	mutationMu.Lock()
	defer mutationMu.Unlock()

	fp := Path()
	if fp == "" {
		return nil
	}
	unlock, err := lockStore(fp)
	if err != nil {
		return err
	}
	defer unlock()

	return saveAtomic(fp, s)
}

// lockStore prepares the private secrets directory and takes the lock on a
// sibling file. The lock must be held until any read-modify-write is complete.
func lockStore(fp string) (func(), error) {
	dir := filepath.Dir(fp)
	if err := prepareDir(dir); err != nil {
		return nil, err
	}
	return acquireFileLock(filepath.Join(dir, "secrets.lock"))
}

func prepareDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll does not change the mode of an existing directory.
	return os.Chmod(dir, 0o700)
}

func saveAtomic(fp string, s *Store) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(fp)
	if err := prepareDir(dir); err != nil {
		return err
	}

	tmp, err := createTempFile(dir, ".secrets.json-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpName)
	}()

	// CreateTemp currently creates mode 0600 files, but set it explicitly so
	// the invariant does not depend on its implementation or the process umask.
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if n, err := tmp.Write(data); err != nil {
		return err
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	closed = true

	// The temporary file is in the same directory, so rename is an atomic
	// replacement: readers see either the complete old store or complete new one.
	if err := os.Rename(tmpName, fp); err != nil {
		return err
	}
	if err := os.Chmod(fp, 0o600); err != nil {
		return err
	}

	// Persist the directory entry where the platform/filesystem supports it.
	// The data file itself was synced before rename; directory syncing is best
	// effort because some supported filesystems reject Sync on directories.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Lookup returns the stored token for key (best-effort). It returns ok=true only
// when a non-empty token is present.
func Lookup(key string) (string, bool) {
	s, err := Load()
	if err != nil {
		return "", false
	}
	tok, ok := s.Tokens[key]
	if !ok || tok == "" {
		return "", false
	}
	return tok, true
}

// Set stores token under key (creating the store if needed).
func Set(key, token string) error {
	return mutate(func(s *Store) {
		s.Tokens[key] = token
	})
}

// Remove deletes the token stored under key.
func Remove(key string) error {
	return mutate(func(s *Store) {
		delete(s.Tokens, key)
	})
}

func mutate(fn func(*Store)) error {
	mutationMu.Lock()
	defer mutationMu.Unlock()

	fp := Path()
	if fp == "" {
		return nil
	}
	unlock, err := lockStore(fp)
	if err != nil {
		return err
	}
	defer unlock()

	s, err := load(fp)
	if err != nil {
		return err
	}
	fn(s)
	return saveAtomic(fp, s)
}

// Keys returns the sorted list of stored key names (never the values).
func Keys() []string {
	s, err := Load()
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(s.Tokens))
	for k := range s.Tokens {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
