// Package project implements the persistent multi-project registry for a ycc
// daemon (spec §3.1). A project is a named workspace (name → absolute path). The
// registry is durable state in the daemon's state dir
// (e.g. ~/.local/state/ycc/projects.json), separate from each project's own
// per-workspace .ycc/. Projects are registered explicitly (`ycc project add` /
// AddProject RPC) and auto-registered when a session starts in a not-yet-known
// workspace.
package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Project is one registered workspace: a stable name and its absolute path.
type Project struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Registry is a concurrency-safe, persistent map of project name → absolute path.
// It is loaded from and saved to a JSON file (projects.json) in the daemon state
// dir. A zero path disables persistence (in-memory only).
type Registry struct {
	mu     sync.Mutex
	path   string            // file backing the registry; "" => in-memory only
	byName map[string]string // name -> absolute path
}

// projectsFile is the on-disk JSON shape.
type projectsFile struct {
	Projects []Project `json:"projects"`
}

// Open loads (or creates) a registry backed by the JSON file at path. A missing
// file yields an empty registry; the file is created lazily on the first Save.
func Open(path string) (*Registry, error) {
	r := &Registry{path: path, byName: map[string]string{}}
	if path == "" {
		return r, nil
	}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

// NewMemory returns an in-memory registry with no backing file (for tests and
// the one-shot path).
func NewMemory() *Registry { return &Registry{byName: map[string]string{}} }

// StateFile returns the default registry path in the daemon state dir
// (XDG_STATE_HOME or ~/.local/state), i.e. <state>/ycc/projects.json.
func StateFile() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".local", "state")
		}
	}
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "ycc", "projects.json")
}

func (r *Registry) load() error {
	b, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read projects file: %w", err)
	}
	var pf projectsFile
	if len(b) > 0 {
		if err := json.Unmarshal(b, &pf); err != nil {
			return fmt.Errorf("parse projects file: %w", err)
		}
	}
	for _, p := range pf.Projects {
		if p.Name != "" && p.Path != "" {
			r.byName[p.Name] = p.Path
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
	pf := projectsFile{Projects: r.listLocked()}
	b, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write projects file: %w", err)
	}
	return os.Rename(tmp, r.path)
}

// Add registers the workspace at path under an optional name, persisting the
// registry. The path is absolutized; when name is empty it is derived from the
// directory basename. If the path is already registered, the existing project is
// returned unchanged. A name collision with a different path is de-duplicated by
// appending a numeric suffix. The resolved Project is returned.
func (r *Registry) Add(path, name string) (Project, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Project{}, fmt.Errorf("resolve project path: %w", err)
	}
	abs = filepath.Clean(abs)

	r.mu.Lock()
	defer r.mu.Unlock()

	// Already registered (same path) => return the existing entry.
	for n, p := range r.byName {
		if p == abs {
			return Project{Name: n, Path: p}, nil
		}
	}

	if name == "" {
		name = filepath.Base(abs)
	}
	name = r.uniqueNameLocked(name)
	r.byName[name] = abs
	if err := r.save(); err != nil {
		delete(r.byName, name)
		return Project{}, err
	}
	return Project{Name: name, Path: abs}, nil
}

// uniqueNameLocked returns a name not already in use, appending -2, -3, … on
// collision. Caller holds r.mu.
func (r *Registry) uniqueNameLocked(name string) string {
	if _, ok := r.byName[name]; !ok {
		return name
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", name, i)
		if _, ok := r.byName[cand]; !ok {
			return cand
		}
	}
}

// EnsureWorkspace auto-registers the absolute workspace path if it isn't already
// registered, returning its project. Used on session start (spec §3.1).
func (r *Registry) EnsureWorkspace(absPath string) (Project, error) {
	return r.Add(absPath, "")
}

// Remove deletes a project by name, persisting the change. Removing an unknown
// name is a no-op.
func (r *Registry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byName[name]; !ok {
		return nil
	}
	delete(r.byName, name)
	return r.save()
}

// Resolve returns the absolute path registered under name.
func (r *Registry) Resolve(name string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byName[name]
	return p, ok
}

// List returns all projects sorted by name (stable order).
func (r *Registry) List() []Project {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listLocked()
}

func (r *Registry) listLocked() []Project {
	out := make([]Project, 0, len(r.byName))
	for n, p := range r.byName {
		out = append(out, Project{Name: n, Path: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
