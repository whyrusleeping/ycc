package workspacelease

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
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

// claimSet maps a scope to its claim on a path.
type claimSet map[string]claimEntry

// claimEntry is one scope's claim: the generation of its latest write and when
// it first claimed the path. overlap marks a claim recorded only because the
// scope's shell changed a path another scope already claimed (RecordOverlaps).
type claimEntry struct {
	gen     uint64
	since   time.Time
	overlap bool
}

// Claim is one scope's claim on a path, for display. Gen is the claim
// generation of the scope's latest write of the path (0 when loaded from disk).
type Claim struct {
	Scope string
	Since time.Time // zero when unknown (claims persisted before it was recorded)
	Gen   uint64
}

// Overlap reports a write to a path that other scopes had already claimed.
type Overlap struct {
	Worktree string   // canonical worktree
	Path     string   // repository-relative slash path
	Writer   string   // the writing scope
	Others   []string // the other claiming scopes, sorted
}

// SetOverlapNotifier installs fn to hear about overlapping writes (RecordWrite
// and NotifyOverlaps). It returns text for the writer's tool result, or "".
// fn runs outside the service lock.
func (s *Service) SetOverlapNotifier(fn func(Overlap) string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onOverlap = fn
}

// NotifyOverlaps passes each overlap to the notifier and joins the texts it
// returns (one per line).
func (s *Service) NotifyOverlaps(overlaps []Overlap) string {
	if s == nil || len(overlaps) == 0 {
		return ""
	}
	s.mu.Lock()
	fn := s.onOverlap
	s.mu.Unlock()
	if fn == nil {
		return ""
	}
	var notes []string
	for _, o := range overlaps {
		if text := fn(o); text != "" {
			notes = append(notes, text)
		}
	}
	return strings.Join(notes, "\n")
}

// SetSharedHeadPeers installs fn to name the live sessions, other than scope,
// whose work shares canonical worktree's HEAD, index and stash. fn runs
// outside the service lock.
func (s *Service) SetSharedHeadPeers(fn func(worktree, scope string) []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sharedHead = fn
}

// SharedHeadPeers names the other live sessions working in canonical
// worktree (SetSharedHeadPeers), or nil without that hook.
func (s *Service) SharedHeadPeers(worktree, scope string) []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	fn := s.sharedHead
	s.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(worktree, scope)
}

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
// paths outside a canonical worktree are ignored. When other scopes already
// claim the path, the overlap notifier is told and its text returned.
func (s *Service) RecordWrite(path, fallback string, token *Token) string {
	if s == nil || token == nil || token.scope == "" {
		return ""
	}
	key, rel, ok := relativeToWorktree(path, fallback)
	if !ok {
		return ""
	}
	s.mu.Lock()
	var others []string
	for scope := range s.claimsLocked(key)[rel] {
		if scope != token.scope {
			others = append(others, scope)
		}
	}
	s.recordLocked(key, []string{rel}, token.scope)
	s.mu.Unlock()
	if len(others) == 0 {
		return ""
	}
	sort.Strings(others)
	return s.NotifyOverlaps([]Overlap{{Worktree: key, Path: rel, Writer: token.scope, Others: others}})
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
	now := time.Now()
	for _, rel := range paths {
		set := byPath[rel]
		if set == nil {
			set = make(claimSet)
			byPath[rel] = set
		}
		entry, ok := set[scope]
		if !ok || entry.overlap {
			added = true
		}
		if !ok {
			entry.since = now
		}
		entry.gen, entry.overlap = s.gen, false
		set[scope] = entry
	}
	if added {
		s.saveLocked(key)
	}
}

// RecordOverlaps records that token's scope's shell changed repository-relative
// paths that other scopes already claimed, in the worktree containing root. An
// overlap claim leaves the path out of the scope's own changeset (Split does not
// count it as the scope's) but counts as another scope's claim for everyone
// else, so their commits see that a live session also changed it. A scope that
// already claims a path keeps its claim.
func (s *Service) RecordOverlaps(root string, paths []string, token *Token) {
	if s == nil || token == nil || token.scope == "" || len(paths) == 0 {
		return
	}
	key, err := Canonical(root)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byPath := s.claimsLocked(key)
	s.gen++
	added := false
	for _, rel := range paths {
		set := byPath[rel]
		if set == nil {
			set = make(claimSet)
			byPath[rel] = set
		}
		if entry, ok := set[token.scope]; ok {
			if entry.overlap {
				entry.gen = s.gen
				set[token.scope] = entry
			}
			continue
		}
		set[token.scope] = claimEntry{gen: s.gen, since: time.Now(), overlap: true}
		added = true
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

// ClaimDetails returns, for the worktree containing root, each claimed
// repository-relative path and its claims sorted by scope.
func (s *Service) ClaimDetails(root string) map[string][]Claim {
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
	out := make(map[string][]Claim, len(byPath))
	for path, set := range byPath {
		list := make([]Claim, 0, len(set))
		for scope, entry := range set {
			list = append(list, Claim{Scope: scope, Since: entry.since, Gen: entry.gen})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Scope < list[j].Scope })
		out[path] = list
	}
	return out
}

// PathClaims resolves path (relative paths against fallback's worktree) to its
// canonical worktree and repository-relative form and returns its claims
// sorted by scope. ok is false when path is not inside a git worktree.
func (s *Service) PathClaims(path, fallback string) (worktree, rel string, claims []Claim, ok bool) {
	if s == nil {
		return "", "", nil, false
	}
	worktree, rel, ok = relativeToWorktree(path, fallback)
	if !ok {
		return "", "", nil, false
	}
	return worktree, rel, s.ClaimDetails(worktree)[rel], true
}

// Split partitions the claims in the worktree containing root into paths scope
// wrote (mine) and paths any other scope wrote (foreign); a path both wrote is
// in both. scope's overlap claims are in neither. It returns nils for an
// unscoped caller.
func (s *Service) Split(root, scope string) (mine, foreign map[string]bool) {
	if s == nil || scope == "" {
		return nil, nil
	}
	mine, foreign = map[string]bool{}, map[string]bool{}
	key, err := Canonical(root)
	if err != nil {
		return mine, foreign
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for path, set := range s.claimsLocked(key) {
		for owner, entry := range set {
			switch {
			case owner != scope:
				foreign[path] = true
			case !entry.overlap:
				mine[path] = true
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
		for scope, entry := range set {
			if entry.gen <= before {
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
			var entry claimEntry
			if unix := record.Since[rel][scope]; unix > 0 {
				entry.since = time.Unix(unix, 0)
			}
			set[scope] = entry
		}
		byPath[rel] = set
	}
	for rel, scopes := range record.Overlap {
		for _, scope := range scopes {
			if entry, ok := byPath[rel][scope]; ok {
				entry.overlap = true
				byPath[rel][scope] = entry
			}
		}
	}
	return byPath
}

const claimsRecordVersion = 1

// claimsRecord is the persisted form. Since (path -> scope -> unix seconds of
// the first claim) and Overlap (path -> scopes whose claim is an overlap claim)
// are optional so older records and daemons stay compatible.
type claimsRecord struct {
	Version int                         `json:"version"`
	Claims  map[string][]string         `json:"claims"`
	Since   map[string]map[string]int64 `json:"since,omitempty"`
	Overlap map[string][]string         `json:"overlap,omitempty"`
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
	record := claimsRecord{Version: claimsRecordVersion, Claims: map[string][]string{}, Since: map[string]map[string]int64{}, Overlap: map[string][]string{}}
	for rel, set := range s.claims[key] {
		scopes := make([]string, 0, len(set))
		for scope, entry := range set {
			scopes = append(scopes, scope)
			if entry.overlap {
				record.Overlap[rel] = append(record.Overlap[rel], scope)
				sort.Strings(record.Overlap[rel])
			}
			if !entry.since.IsZero() {
				if record.Since[rel] == nil {
					record.Since[rel] = map[string]int64{}
				}
				record.Since[rel][scope] = entry.since.Unix()
			}
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
