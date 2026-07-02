// Package workstream implements the daemon-owned, persistent registry of
// parallel agent workstreams (docs/design/parallel-workstreams.md §5, §7).
//
// A workstream is the unit of parallel work: a linked git worktree + branch
// (ycc/ws/<id>) plus the `work` session scoped to it. It is a CHILD of a
// project (referenced by the parent project's name), never a top-level project
// registry entry — so the user-facing project picker is not polluted with
// ephemeral worktrees. The registry is serialized like the project registry
// (JSON file in the daemon state dir, beside projects.json) and preserves the
// single-writer invariant: at most one active workstream per worktree path.
package workstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Status is a workstream's lifecycle state.
type Status string

const (
	// StatusActive: the worktree exists and a session is (or was) writing to it.
	StatusActive Status = "active"
	// StatusMerged: the workstream's branch was integrated back to base and the
	// worktree cleaned up (set by the merge flow, task 0083).
	StatusMerged Status = "merged"
	// StatusDiscarded: the workstream was abandoned and its worktree cleaned up
	// without merging (task 0084).
	StatusDiscarded Status = "discarded"
	// StatusStale: the worktree is gone (e.g. a crashed daemon) and the entry was
	// reconciled on startup. Terminal for the worktree; kept for history.
	StatusStale Status = "stale"
)

// Terminal reports whether a status frees the worktree path for reuse (i.e. the
// worktree is no longer a live single writer).
func (s Status) Terminal() bool {
	return s == StatusMerged || s == StatusDiscarded || s == StatusStale
}

// Workstream is one parallel unit of work: a linked worktree + branch and the
// session scoped to it, tracked as a child of a project.
type Workstream struct {
	// ID is the stable short id (ws_<8-hex>).
	ID string `json:"id"`
	// Project is the parent project's NAME (its worktree lives under this
	// project's primary tree); a workstream is never its own project entry.
	Project string `json:"project"`
	// BaseCommit is the commit the worktree branch was created from.
	BaseCommit string `json:"base_commit"`
	// Branch is the worktree's branch ref (ycc/ws/<id>[-<task>]).
	Branch string `json:"branch"`
	// WorktreePath is the absolute path of the linked worktree directory.
	WorktreePath string `json:"worktree_path"`
	// SessionID is the id of the session scoped to the worktree (empty until the
	// session is started).
	SessionID string `json:"session_id,omitempty"`
	// TaskID optionally records the backlog task this workstream targets.
	TaskID string `json:"task_id,omitempty"`
	// Status is the lifecycle state.
	Status Status `json:"status"`
	// CreatedAt is when the workstream was spawned.
	CreatedAt time.Time `json:"created_at"`
}

// ErrWorktreeInUse is returned by Add when another active workstream already
// records the same worktree path (single-writer invariant, §7).
var ErrWorktreeInUse = errors.New("worktree path already has an active workstream")

// Registry is a concurrency-safe, persistent map of workstream id → Workstream,
// backed by a JSON file (workstreams.json) in the daemon state dir. A zero path
// disables persistence (in-memory only).
type Registry struct {
	mu   sync.Mutex
	path string // file backing the registry; "" => in-memory only
	byID map[string]Workstream
}

// workstreamsFile is the on-disk JSON shape.
type workstreamsFile struct {
	Workstreams []Workstream `json:"workstreams"`
}

// Open loads (or creates) a registry backed by the JSON file at path. A missing
// file yields an empty registry; the file is created lazily on the first save.
func Open(path string) (*Registry, error) {
	r := &Registry{path: path, byID: map[string]Workstream{}}
	if path == "" {
		return r, nil
	}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

// NewMemory returns an in-memory registry with no backing file (tests and the
// one-shot path).
func NewMemory() *Registry { return &Registry{byID: map[string]Workstream{}} }

// StateFile returns the default registry path in the daemon state dir
// (XDG_STATE_HOME or ~/.local/state), i.e. <state>/ycc/workstreams.json —
// beside projects.json.
func StateFile() string {
	return filepath.Join(stateDir(), "ycc", "workstreams.json")
}

// DefaultWorktreesRoot returns the default directory under which linked
// worktrees are created: <state>/ycc/worktrees (design §5, keyed by project/id
// below this root).
func DefaultWorktreesRoot() string {
	return filepath.Join(stateDir(), "ycc", "worktrees")
}

// stateDir resolves the base state directory (mirrors project.StateFile).
func stateDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".local", "state")
		}
	}
	if dir == "" {
		dir = os.TempDir()
	}
	return dir
}

func (r *Registry) load() error {
	b, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read workstreams file: %w", err)
	}
	var wf workstreamsFile
	if len(b) > 0 {
		if err := json.Unmarshal(b, &wf); err != nil {
			return fmt.Errorf("parse workstreams file: %w", err)
		}
	}
	for _, w := range wf.Workstreams {
		if w.ID != "" {
			r.byID[w.ID] = w
		}
	}
	return nil
}

// save writes the registry to its backing file. Caller holds r.mu.
func (r *Registry) save() error {
	if r.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	wf := workstreamsFile{Workstreams: r.listLocked()}
	b, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write workstreams file: %w", err)
	}
	return os.Rename(tmp, r.path)
}

// Add records a new workstream, persisting the registry. It enforces the
// single-writer invariant: if another workstream in a non-terminal (active)
// state already records the same worktree path, it returns ErrWorktreeInUse.
func (r *Registry) Add(w Workstream) error {
	if w.ID == "" {
		return fmt.Errorf("workstream id required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[w.ID]; ok {
		return fmt.Errorf("workstream %q already exists", w.ID)
	}
	if w.WorktreePath != "" {
		for _, ex := range r.byID {
			if ex.WorktreePath == w.WorktreePath && !ex.Status.Terminal() {
				return fmt.Errorf("%w: %s", ErrWorktreeInUse, w.WorktreePath)
			}
		}
	}
	if w.Status == "" {
		w.Status = StatusActive
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now()
	}
	r.byID[w.ID] = w
	if err := r.save(); err != nil {
		delete(r.byID, w.ID)
		return err
	}
	return nil
}

// Get returns the workstream with the given id.
func (r *Registry) Get(id string) (Workstream, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.byID[id]
	return w, ok
}

// SetStatus updates a workstream's status, persisting the change. Returns an
// error if the id is unknown.
func (r *Registry) SetStatus(id string, status Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("unknown workstream %q", id)
	}
	prev := w.Status
	w.Status = status
	r.byID[id] = w
	if err := r.save(); err != nil {
		w.Status = prev
		r.byID[id] = w
		return err
	}
	return nil
}

// SetSessionID records the session scoped to a workstream, persisting it.
func (r *Registry) SetSessionID(id, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("unknown workstream %q", id)
	}
	prev := w.SessionID
	w.SessionID = sessionID
	r.byID[id] = w
	if err := r.save(); err != nil {
		w.SessionID = prev
		r.byID[id] = w
		return err
	}
	return nil
}

// Remove deletes a workstream by id, persisting the change. Removing an unknown
// id is a no-op.
func (r *Registry) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[id]; !ok {
		return nil
	}
	w := r.byID[id]
	delete(r.byID, id)
	if err := r.save(); err != nil {
		r.byID[id] = w
		return err
	}
	return nil
}

// List returns all workstreams sorted by creation time then id (stable order).
func (r *Registry) List() []Workstream {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listLocked()
}

// ListByProject returns the workstreams belonging to the named project. An empty
// name returns all workstreams.
func (r *Registry) ListByProject(name string) []Workstream {
	if name == "" {
		return r.List()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Workstream, 0)
	for _, w := range r.byID {
		if w.Project == name {
			out = append(out, w)
		}
	}
	sortWorkstreams(out)
	return out
}

func (r *Registry) listLocked() []Workstream {
	out := make([]Workstream, 0, len(r.byID))
	for _, w := range r.byID {
		out = append(out, w)
	}
	sortWorkstreams(out)
	return out
}

func sortWorkstreams(ws []Workstream) {
	sort.Slice(ws, func(i, j int) bool {
		if ws[i].CreatedAt.Equal(ws[j].CreatedAt) {
			return ws[i].ID < ws[j].ID
		}
		return ws[i].CreatedAt.Before(ws[j].CreatedAt)
	})
}
