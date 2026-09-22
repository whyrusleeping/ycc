// Package workspacelease serializes potentially mutating execution by canonical worktree.
package workspacelease

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// Service owns daemon-wide worktree mutation leases.
type Service struct {
	mu     sync.Mutex
	next   atomic.Uint64
	owners map[string]*entry
}

type entry struct {
	token *Token
	refs  int
	// children holds live asynchronous child claims, keyed by claim id.
	// Children keep the entry alive (and foreign tokens out) after the owning
	// scope releases its own claims, until the child actually exits.
	children map[uint64]*child
}

type child struct {
	owner string
	// detached children no longer belong to their creating scope (for
	// example a job handed off to a parent), so they refuse that scope too.
	detached bool
}

// conflictOwner describes who holds e for a refused foreign acquisition. A
// live child is more actionable than its scope because it names the running
// process the caller must wait for or stop.
func (e *entry) conflictOwner() string {
	if owner, ok := e.earliestChild(false); ok {
		return owner
	}
	return e.token.Owner()
}

// earliestChild returns the owner of the earliest-created live child,
// restricted to detached children when detachedOnly is set.
func (e *entry) earliestChild(detachedOnly bool) (string, bool) {
	var (
		first uint64
		owner string
	)
	for id, c := range e.children {
		if detachedOnly && !c.detached {
			continue
		}
		if first == 0 || id < first {
			first, owner = id, c.owner
		}
	}
	return owner, first != 0
}

// Token identifies one execution scope. Reuse the same token only for commands
// executed by that scope; delegated workers receive a distinct token.
type Token struct {
	service *Service
	id      uint64
	owner   string
}

// Lease is one retained claim on a worktree. Release is idempotent.
type Lease struct {
	service *Service
	key     string
	token   *Token
	childID uint64
	once    sync.Once
}

// Conflict describes the execution scope currently owning a worktree.
type Conflict struct {
	Owner string
}

func (e *Conflict) Error() string {
	return fmt.Sprintf("worktree mutation is owned by %s; wait for it to finish or stop it, or use a separate workstream", e.Owner)
}

// NewService returns an empty ownership service.
func NewService() *Service { return &Service{owners: make(map[string]*entry)} }

// NewToken creates an isolated execution scope with a human-readable owner.
func (s *Service) NewToken(owner string) *Token {
	if s == nil {
		return nil
	}
	return &Token{service: s, id: s.next.Add(1), owner: owner}
}

// Owner returns the token's human-readable owner.
func (t *Token) Owner() string {
	if t == nil {
		return "unknown execution"
	}
	return t.owner
}

// Acquire atomically claims root for token. Calls using the same token are
// reentrant, allowing a delegated worker to run its own file and shell tools.
func (s *Service) Acquire(root string, token *Token) (*Lease, error) {
	if s == nil || token == nil || token.service != s {
		return nil, fmt.Errorf("workspace mutation ownership is not configured")
	}
	key, err := Canonical(root)
	if err != nil {
		return nil, err
	}
	return s.acquireKey(key, token)
}

// AcquireChild retains ownership for one asynchronous child. The claim survives
// release of token's other claims until actual process exit, so no other scope
// can mutate the worktree while the child runs, even after a kill request. The
// owning scope itself may keep acquiring (reentrantly) and may start further
// children: overlapping an execution scope's own background work is that
// scope's coordination responsibility, not a cross-actor write conflict.
// Detach withdraws that same-scope admission when the child changes hands.
func (s *Service) AcquireChild(root string, token *Token, owner string) (*Lease, error) {
	if s == nil || token == nil || token.service != s {
		return nil, fmt.Errorf("workspace mutation ownership is not configured")
	}
	key, err := Canonical(root)
	if err != nil {
		return nil, err
	}
	return s.acquireChildKey(key, token, owner)
}

// AcquirePath claims the worktree containing path. The longest existing parent
// is symlink-resolved before Git discovery, so a not-yet-created destination in
// an extra write root is keyed to that destination's actual checkout. fallback
// supplies the ownership root when the destination is not in a Git worktree.
func (s *Service) AcquirePath(path, fallback string, token *Token) (*Lease, error) {
	if s == nil || token == nil || token.service != s {
		return nil, fmt.Errorf("workspace mutation ownership is not configured")
	}
	key, err := CanonicalContaining(path, fallback)
	if err != nil {
		return nil, err
	}
	return s.acquireKey(key, token)
}

func (s *Service) acquireKey(key string, token *Token) (*Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.owners[key]; current != nil {
		if current.token != token {
			return nil, &Conflict{Owner: current.conflictOwner()}
		}
		if owner, detached := current.earliestChild(true); detached {
			return nil, &Conflict{Owner: owner}
		}
		current.refs++
	} else {
		s.owners[key] = &entry{token: token, refs: 1}
	}
	return &Lease{service: s, key: key, token: token}, nil
}

func (s *Service) acquireChildKey(key string, token *Token, owner string) (*Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.owners[key]
	if current != nil && current.token != token {
		return nil, &Conflict{Owner: current.conflictOwner()}
	}
	if current != nil {
		if detachedOwner, detached := current.earliestChild(true); detached {
			return nil, &Conflict{Owner: detachedOwner}
		}
	}
	if current == nil {
		current = &entry{token: token}
		s.owners[key] = current
	}
	if current.children == nil {
		current.children = make(map[uint64]*child)
	}
	childID := s.next.Add(1)
	current.refs++
	current.children[childID] = &child{owner: owner}
	return &Lease{service: s, key: key, token: token, childID: childID}, nil
}

// Detach marks a live child claim as no longer belonging to its creating
// scope, e.g. when its job is handed off to a parent. Until the child exits it
// then refuses every acquisition on the worktree, including by the creating
// scope's token. It is a no-op for non-child or released leases.
func (l *Lease) Detach() {
	if l == nil || l.service == nil || l.childID == 0 {
		return
	}
	l.service.mu.Lock()
	defer l.service.mu.Unlock()
	current := l.service.owners[l.key]
	if current == nil || current.token != l.token {
		return
	}
	if c := current.children[l.childID]; c != nil {
		c.detached = true
	}
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
		if l.childID != 0 {
			delete(current.children, l.childID)
		}
		current.refs--
		if current.refs == 0 {
			delete(l.service.owners, l.key)
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
