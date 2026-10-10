package workspacelease

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Path claims attribute writes to the scope (session) that made them. They
// replace lifetime worktree ownership as the basis of change attribution: a
// session's changeset excludes paths that only other scopes wrote, so
// concurrent sessions in one worktree neither block each other nor sweep each
// other's work into their commits. Claims are advisory bookkeeping — they
// never refuse a write. They are retired when the claimed path is committed or
// reverted (it matches HEAD again), and outlive the writing session so its
// uncommitted work stays attributed to it. With PersistClaims they also
// survive a daemon restart, stored per worktree in its git directory.

// claimSet maps a scope to the generation at which it last claimed a path.
type claimSet map[string]uint64

// PersistClaims makes claims durable: each worktree's claims are loaded from,
// and saved to, <git-dir>/ycc/claims.json. Call once before use.
func (s *Service) PersistClaims() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persist = true
}

// Generation returns the current claim generation. ReleaseAll with this value
// retires only claims recorded at or before it, so a write that lands while a
// caller decides what to retire is never dropped.
func (s *Service) Generation() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// RecordWrite attributes a write of path to token's scope. Unscoped tokens and
// paths outside a canonical worktree are ignored.
func (s *Service) RecordWrite(path, fallback string, token *Token) {
	if s == nil || token == nil || token.scope == "" {
		return
	}
	key, rel, ok := relativeToWorktree(path, fallback)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordLocked(key, []string{rel}, token.scope)
}

// RecordWrites attributes writes of repository-relative paths in the worktree
// containing root to token's scope (one canonicalization for many paths).
func (s *Service) RecordWrites(root string, paths []string, token *Token) {
	if s == nil || token == nil || token.scope == "" || len(paths) == 0 {
		return
	}
	key, err := Canonical(root)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordLocked(key, paths, token.scope)
}

func (s *Service) recordLocked(key string, paths []string, scope string) {
	byPath := s.claimsLocked(key)
	added := false
	s.gen++
	for _, rel := range paths {
		set := byPath[rel]
		if set == nil {
			set = make(claimSet)
			byPath[rel] = set
		}
		if _, ok := set[scope]; !ok {
			added = true
		}
		set[scope] = s.gen
	}
	if added {
		s.saveLocked(key)
	}
}

// Claims returns, for the worktree containing root, each claimed
// repository-relative path and the sorted scopes that wrote it.
func (s *Service) Claims(root string) map[string][]string {
	if s == nil {
		return nil
	}
	key, err := Canonical(root)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byPath := s.claimsLocked(key)
	out := make(map[string][]string, len(byPath))
	for path, set := range byPath {
		list := make([]string, 0, len(set))
		for scope := range set {
			list = append(list, scope)
		}
		sort.Strings(list)
		out[path] = list
	}
	return out
}

// Split partitions the claims in the worktree containing root into paths scope
// wrote (mine) and paths any other scope wrote (foreign); a path both wrote is
// in both. It returns nils for an unscoped caller.
func (s *Service) Split(root, scope string) (mine, foreign map[string]bool) {
	if s == nil || scope == "" {
		return nil, nil
	}
	mine, foreign = map[string]bool{}, map[string]bool{}
	for path, scopes := range s.Claims(root) {
		for _, owner := range scopes {
			if owner == scope {
				mine[path] = true
			} else {
				foreign[path] = true
			}
		}
	}
	return mine, foreign
}

// ReleaseClaims drops scope's claims on the given repository-relative paths in
// the worktree containing root (all of scope's claims there when paths is nil).
func (s *Service) ReleaseClaims(root, scope string, paths []string) {
	if s == nil || scope == "" {
		return
	}
	key, err := Canonical(root)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byPath := s.claimsLocked(key)
	if paths == nil {
		for path := range byPath {
			paths = append(paths, path)
		}
	}
	changed := false
	for _, path := range paths {
		if set := byPath[path]; set != nil {
			if _, ok := set[scope]; ok {
				delete(set, scope)
				changed = true
			}
			if len(set) == 0 {
				delete(byPath, path)
			}
		}
	}
	if changed {
		s.saveLocked(key)
	}
}

// ReleaseAll drops every scope's claims on the given repository-relative paths
// in the worktree containing root — but only claims recorded at or before
// generation before (see Generation), so a write that landed after the caller
// observed the paths clean keeps its claim.
func (s *Service) ReleaseAll(root string, paths []string, before uint64) {
	if s == nil || len(paths) == 0 {
		return
	}
	key, err := Canonical(root)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byPath := s.claimsLocked(key)
	changed := false
	for _, path := range paths {
		set := byPath[path]
		for scope, gen := range set {
			if gen <= before {
				delete(set, scope)
				changed = true
			}
		}
		if set != nil && len(set) == 0 {
			delete(byPath, path)
		}
	}
	if changed {
		s.saveLocked(key)
	}
}

// ReleaseScope drops every claim held by scope in every loaded worktree.
func (s *Service) ReleaseScope(scope string) {
	if s == nil || scope == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, byPath := range s.claims {
		changed := false
		for path, set := range byPath {
			if _, ok := set[scope]; ok {
				delete(set, scope)
				changed = true
			}
			if len(set) == 0 {
				delete(byPath, path)
			}
		}
		if changed {
			s.saveLocked(key)
		}
	}
}

// claimsLocked returns key's claim map, loading persisted claims on first use.
func (s *Service) claimsLocked(key string) map[string]claimSet {
	byPath := s.claims[key]
	if byPath != nil {
		return byPath
	}
	byPath = make(map[string]claimSet)
	s.claims[key] = byPath
	if !s.persist {
		return byPath
	}
	path := claimsFile(key)
	if path == "" {
		return byPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return byPath
	}
	var record claimsRecord
	if json.Unmarshal(data, &record) != nil || record.Version != claimsRecordVersion {
		return byPath
	}
	for rel, scopes := range record.Claims {
		set := make(claimSet, len(scopes))
		for _, scope := range scopes {
			set[scope] = 0
		}
		byPath[rel] = set
	}
	return byPath
}

const claimsRecordVersion = 1

type claimsRecord struct {
	Version int                 `json:"version"`
	Claims  map[string][]string `json:"claims"`
}

// saveLocked persists key's claims (best effort: attribution degrades to
// unclaimed on failure, it never blocks a write).
func (s *Service) saveLocked(key string) {
	if !s.persist {
		return
	}
	path := claimsFile(key)
	if path == "" {
		return
	}
	record := claimsRecord{Version: claimsRecordVersion, Claims: map[string][]string{}}
	for rel, set := range s.claims[key] {
		scopes := make([]string, 0, len(set))
		for scope := range set {
			scopes = append(scopes, scope)
		}
		sort.Strings(scopes)
		record.Claims[rel] = scopes
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claims-*")
	if err != nil {
		return
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil || os.Rename(tmp.Name(), path) != nil {
		os.Remove(tmp.Name())
	}
}

// claimsFile locates the per-worktree claims record in the worktree's git dir.
func claimsFile(key string) string {
	out, err := exec.Command("git", "-C", key, "rev-parse", "--path-format=absolute", "--git-path", "ycc/claims.json").Output()
	if err != nil {
		return ""
	}
	return filepath.Clean(strings.TrimSpace(string(out)))
}

// relativeToWorktree maps path to its canonical worktree and slash-separated
// path relative to that worktree's top level (Git's path namespace).
func relativeToWorktree(path, fallback string) (key, rel string, ok bool) {
	key, err := CanonicalContaining(path, fallback)
	if err != nil {
		return "", "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", false
	}
	// Resolve the longest existing parent so a symlinked workspace path maps
	// into the canonical (resolved) worktree namespace.
	resolved := abs
	suffix := ""
	for {
		if r, err := filepath.EvalSymlinks(resolved); err == nil {
			resolved = filepath.Join(r, suffix)
			break
		}
		parent := filepath.Dir(resolved)
		if parent == resolved {
			return "", "", false
		}
		suffix = filepath.Join(filepath.Base(resolved), suffix)
		resolved = parent
	}
	rel, err = filepath.Rel(key, resolved)
	if err != nil {
		return "", "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", "", false
	}
	return key, rel, true
}
