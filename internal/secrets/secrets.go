// Package secrets persists LLM backend API tokens in a restricted-perms file
// under the user config dir, so a token can be saved once instead of requiring
// the env var to be present in every session. Secrets are machine-local and are
// NEVER written to the project ycc.toml (which is checked into repos); they live
// in a dedicated secrets.json (mode 0600) keyed by the backend's key_env name.
package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// mutationMu serializes complete read-modify-write transactions within this
// process. The sibling file lock provides the corresponding cross-process
// serialization.
var mutationMu sync.Mutex

// createTempFile is a variable so tests can exercise failures after a valid
// store already exists. Production always leaves it set to os.CreateTemp.
var createTempFile = os.CreateTemp

// Store maps a key_env name to its stored API token and retains reference-only
// authorization audit records. Authorization records never contain token values.
type Store struct {
	Tokens         map[string]string `json:"tokens"`
	Authorizations []Authorization   `json:"authorizations,omitempty"`
}

// Authorization permits one model-visible tool invocation to use a named secret
// in one canonical workspace. UsedAt is set before the secret is released to the
// caller, leaving a value-free local audit record of both grants and uses.
type Authorization struct {
	Key       string    `json:"key"`
	Workspace string    `json:"workspace"`
	Tool      string    `json:"tool"`
	GrantedAt time.Time `json:"granted_at"`
	UsedAt    time.Time `json:"used_at,omitempty"`
}

var ErrNotAuthorized = errors.New("secret use is not authorized")

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

// Remove deletes the token stored under key and any authorization records for it.
func Remove(key string) error {
	return mutate(func(s *Store) {
		delete(s.Tokens, key)
		kept := s.Authorizations[:0]
		for _, auth := range s.Authorizations {
			if auth.Key != key {
				kept = append(kept, auth)
			}
		}
		s.Authorizations = kept
	})
}

// CanonicalWorkspace returns the stable absolute workspace identity used by
// single-use authorizations.
func CanonicalWorkspace(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("workspace is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs, nil
}

// Authorize permits one future use of key by tool in workspace. Each call adds
// one grant; the token value is never copied into the authorization record.
func Authorize(key, workspace, tool string) error {
	key = strings.TrimSpace(key)
	tool = strings.TrimSpace(tool)
	if key == "" || tool == "" {
		return errors.New("secret name and tool are required")
	}
	workspace, err := CanonicalWorkspace(workspace)
	if err != nil {
		return err
	}
	return mutateErr(func(s *Store) error {
		if strings.TrimSpace(s.Tokens[key]) == "" {
			return fmt.Errorf("no stored token for %s", key)
		}
		s.Authorizations = append(s.Authorizations, Authorization{
			Key: key, Workspace: workspace, Tool: tool, GrantedAt: time.Now().UTC(),
		})
		return nil
	})
}

// ConsumeAuthorized atomically consumes one matching grant and returns its
// secret to the named tool implementation. Model-facing callers must not return
// the value in results or errors.
func ConsumeAuthorized(key, workspace, tool string) (string, error) {
	workspace, err := CanonicalWorkspace(workspace)
	if err != nil {
		return "", err
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()

	fp := Path()
	if fp == "" {
		return "", ErrNotAuthorized
	}
	unlock, err := lockStore(fp)
	if err != nil {
		return "", err
	}
	defer unlock()

	s, err := load(fp)
	if err != nil {
		return "", err
	}
	for i := range s.Authorizations {
		auth := &s.Authorizations[i]
		if auth.Key == key && auth.Workspace == workspace && auth.Tool == tool && auth.UsedAt.IsZero() {
			token := strings.TrimSpace(s.Tokens[key])
			if token == "" {
				return "", ErrNotAuthorized
			}
			auth.UsedAt = time.Now().UTC()
			if err := saveAtomic(fp, s); err != nil {
				return "", err
			}
			return token, nil
		}
	}
	return "", ErrNotAuthorized
}

// Authorizations returns reference-only authorization audit records.
func Authorizations() []Authorization {
	s, err := Load()
	if err != nil {
		return nil
	}
	out := append([]Authorization(nil), s.Authorizations...)
	sort.Slice(out, func(i, j int) bool { return out[i].GrantedAt.Before(out[j].GrantedAt) })
	return out
}

func mutate(fn func(*Store)) error {
	return mutateErr(func(s *Store) error {
		fn(s)
		return nil
	})
}

func mutateErr(fn func(*Store) error) error {
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
	if err := fn(s); err != nil {
		return err
	}
	return saveAtomic(fp, s)
}

// RedactKnown replaces exact values from the local secret store in presentation
// text. It is deliberately best-effort: it does not guess arbitrary credentials
// and must never be used to alter durable event history or model replay.
func RedactKnown(text string) (string, bool) {
	s, err := Load()
	if err != nil {
		return text, false
	}
	values := make([]string, 0, len(s.Tokens))
	for _, value := range s.Tokens {
		if len(value) >= 8 && strings.Contains(text, value) {
			values = append(values, value)
		}
	}
	// Replace longer values first in case one stored value contains another.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		text = strings.ReplaceAll(text, value, "[REDACTED STORED SECRET]")
	}
	return text, len(values) > 0
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
