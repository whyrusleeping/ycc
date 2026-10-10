// Package workspacelease coordinates mutation of a canonical worktree across
// sessions and agents. Leases are short, operation-scoped exclusive sections (a
// file write, a commit, a workstream merge); they are never held across an
// agent's lifetime. Long-lived attribution of who wrote what is tracked
// separately as per-scope path claims (see claims.go), so several sessions and
// agents can work in one worktree concurrently.
package workspacelease

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// Service owns daemon-wide worktree mutation leases and path claims.
type Service struct {
	mu     sync.Mutex
	next   atomic.Uint64
	owners map[string]*entry
	// released is closed (and replaced) whenever a lease is fully released so
	// waiting acquirers re-check without polling.
	released chan struct{}
	// claims: canonical worktree -> repository-relative path -> scope -> the
	// generation of its latest claim (see claims.go).
	claims  map[string]map[string]claimSet
	gen     uint64
	persist bool
}

type entry struct {
	token *Token
	refs  int
}

// Token identifies one execution scope. Reuse the same token only for commands
// executed by that scope; delegated workers receive a distinct token. scope
// names the attribution owner (a session id) shared by a coordinator and the
// agents it delegates to; it is empty for unattributed operations.
type Token struct {
	service *Service
	id      uint64
	owner   string
	scope   string
}

// Lease is one retained claim on a worktree. Release is idempotent.
type Lease struct {
	service *Service
	key     string
	token   *Token
	once    sync.Once
}

// Conflict describes the execution scope currently owning a worktree.
type Conflict struct {
	Owner string
}

func (e *Conflict) Error() string {
	return fmt.Sprintf("worktree is busy with %s; retry shortly", e.Owner)
}

// NewService returns an empty ownership service.
func NewService() *Service {
	return &Service{
		owners:   make(map[string]*entry),
		released: make(chan struct{}),
		claims:   make(map[string]map[string]claimSet),
	}
}

// NewToken creates an isolated, unattributed execution scope with a
// human-readable owner.
func (s *Service) NewToken(owner string) *Token {
	return s.NewScopedToken("", owner)
}

// NewScopedToken creates an execution token whose writes are attributed to
// scope (normally a session id).
func (s *Service) NewScopedToken(scope, owner string) *Token {
	if s == nil {
		return nil
	}
	return &Token{service: s, id: s.next.Add(1), owner: owner, scope: scope}
}

// Child creates a distinct token for a delegated worker that shares parent's
// attribution scope.
func (s *Service) Child(parent *Token, owner string) *Token {
	scope := ""
	if parent != nil {
		scope = parent.scope
		owner = parent.owner + " / " + owner
	}
	return s.NewScopedToken(scope, owner)
}

// Owner returns the token's human-readable owner.
func (t *Token) Owner() string {
	if t == nil {
		return "unknown execution"
	}
	return t.owner
}

// Scope returns the token's attribution scope ("" when unattributed).
func (t *Token) Scope() string {
	if t == nil {
		return ""
	}
	return t.scope
}

// Acquire atomically claims root for token without waiting. Calls using the
// same token are reentrant.
func (s *Service) Acquire(root string, token *Token) (*Lease, error) {
	if s == nil || token == nil || token.service != s {
		return nil, fmt.Errorf("workspace mutation ownership is not configured")
	}
	key, err := Canonical(root)
	if err != nil {
		return nil, err
	}
	lease, _, err := s.acquireKey(key, token)
	return lease, err
}

// AcquireWait is Acquire that waits for a conflicting operation to release the
// worktree, until ctx is done. Leases are operation-scoped, so waits are short.
func (s *Service) AcquireWait(ctx context.Context, root string, token *Token) (*Lease, error) {
	if s == nil || token == nil || token.service != s {
		return nil, fmt.Errorf("workspace mutation ownership is not configured")
	}
	key, err := Canonical(root)
	if err != nil {
		return nil, err
	}
	return s.acquireKeyWait(ctx, key, token)
}

// AcquirePath claims the worktree containing path without waiting. The longest
// existing parent is symlink-resolved before Git discovery, so a
// not-yet-created destination in an extra write root is keyed to that
// destination's actual checkout. fallback supplies the ownership root when the
// destination is not in a Git worktree.
func (s *Service) AcquirePath(path, fallback string, token *Token) (*Lease, error) {
	if s == nil || token == nil || token.service != s {
		return nil, fmt.Errorf("workspace mutation ownership is not configured")
	}
	key, err := CanonicalContaining(path, fallback)
	if err != nil {
		return nil, err
	}
	lease, _, err := s.acquireKey(key, token)
	return lease, err
}

// AcquirePathWait is AcquirePath that waits for a conflicting operation.
func (s *Service) AcquirePathWait(ctx context.Context, path, fallback string, token *Token) (*Lease, error) {
	if s == nil || token == nil || token.service != s {
		return nil, fmt.Errorf("workspace mutation ownership is not configured")
	}
	key, err := CanonicalContaining(path, fallback)
	if err != nil {
		return nil, err
	}
	return s.acquireKeyWait(ctx, key, token)
}

func (s *Service) acquireKeyWait(ctx context.Context, key string, token *Token) (*Lease, error) {
	for {
		lease, wake, err := s.acquireKey(key, token)
		if err == nil {
			return lease, nil
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return nil, err
		}
	}
}

// acquireKey returns the lease, or the conflict plus a channel closed at the
// next release so a waiter cannot miss the wake-up.
func (s *Service) acquireKey(key string, token *Token) (*Lease, <-chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.owners[key]; current != nil {
		if current.token != token {
			return nil, s.released, &Conflict{Owner: current.token.Owner()}
		}
		current.refs++
	} else {
		s.owners[key] = &entry{token: token, refs: 1}
	}
	return &Lease{service: s, key: key, token: token}, nil, nil
}

// Release drops this retained claim.
func (l *Lease) Release() {
	if l == nil || l.service == nil {
		return
	}
	l.once.Do(func() {
		l.service.mu.Lock()
		defer l.service.mu.Unlock()
		current := l.service.owners[l.key]
		if current == nil || current.token != l.token {
			return
		}
		current.refs--
		if current.refs == 0 {
			delete(l.service.owners, l.key)
			close(l.service.released)
			l.service.released = make(chan struct{})
		}
	})
}

// CanonicalContaining returns the canonical worktree containing path. Missing
// destination components are handled by resolving the longest existing parent.
// Outside Git, fallback is the ownership identity so separate files below one
// configured writable root do not become independent leases.
func CanonicalContaining(path, fallback string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("canonicalize destination: %w", err)
	}
	existing := abs
	for {
		info, statErr := os.Stat(existing)
		if statErr == nil {
			resolved, evalErr := filepath.EvalSymlinks(existing)
			if evalErr != nil {
				return "", fmt.Errorf("canonicalize destination %q: %w", path, evalErr)
			}
			if !info.IsDir() {
				resolved = filepath.Dir(resolved)
			}
			existing = resolved
			break
		}
		if !os.IsNotExist(statErr) {
			return "", fmt.Errorf("canonicalize destination %q: %w", path, statErr)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("canonicalize destination %q: no existing parent", path)
		}
		existing = parent
	}
	if top, ok := gitTop(existing); ok {
		return top, nil
	}
	return Canonical(fallback)
}

// Canonical returns an absolute, symlink-resolved worktree identity. Paths
// inside a Git checkout resolve to its top level so projects configured at
// different subdirectories cannot claim independent ownership of one checkout.
func Canonical(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize worktree: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("canonicalize worktree %q: %w", root, err)
	}
	if top, ok := gitTop(resolved); ok {
		resolved = top
	}
	return filepath.Clean(resolved), nil
}

func gitTop(dir string) (string, bool) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", false
	}
	top := strings.TrimSpace(string(out))
	resolved, err := filepath.EvalSymlinks(top)
	if err != nil {
		return "", false
	}
	return filepath.Clean(resolved), true
}
