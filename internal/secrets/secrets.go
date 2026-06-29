// Package secrets persists LLM backend API tokens in a restricted-perms file
// under the user config dir, so a token can be saved once instead of requiring
// the env var to be present in every session. Secrets are machine-local and are
// NEVER written to the project ycc.toml (which is checked into repos); they live
// in a dedicated secrets.json (mode 0600) keyed by the backend's key_env name.
package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

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
	s := &Store{Tokens: map[string]string{}}
	fp := Path()
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

// Save writes the store to the secrets file with restrictive permissions: the
// containing dir is created mode 0700 and the file is written mode 0600.
func (s *Store) Save() error {
	fp := Path()
	if fp == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(fp), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fp, data, 0o600)
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
	s, err := Load()
	if err != nil {
		return err
	}
	s.Tokens[key] = token
	return s.Save()
}

// Remove deletes the token stored under key.
func Remove(key string) error {
	s, err := Load()
	if err != nil {
		return err
	}
	delete(s.Tokens, key)
	return s.Save()
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
