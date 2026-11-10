package docs

import (
	"os"
	"path/filepath"
	"strings"
)

// IsDocsLayer reports whether absPath is project bookkeeping prose rather than
// build input: a backlog task file, memory.md, a plan, the spec entry point or
// docs set (doc_globs), or any other Markdown file inside the workspace.
//
// Raw Edit/Write of these files additionally holds the docs store lock (see
// DocsWriteLock) so it serializes with the structured backlog/memory/plan
// writers and neither side can lose an update.
func (s *Store) IsDocsLayer(absPath string) bool {
	if absPath == "" {
		return false
	}
	absPath = filepath.Clean(absPath)
	root := filepath.Dir(s.dir)
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	if rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return false
	}
	if within(absPath, s.dir) || within(absPath, s.PlansDir()) {
		return true
	}
	if s.IsMemory(absPath) || s.IsDoc(absPath) {
		return true
	}
	switch strings.ToLower(filepath.Ext(absPath)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

// IsPlanningDoc reports whether absPath is one of the files a plan-only chat
// may edit: a backlog task file, a plan, memory.md, or the configured design
// docs (the spec entry point and doc_globs). Unlike IsDocsLayer, other
// Markdown (READMEs, code-adjacent docs) is not included.
func (s *Store) IsPlanningDoc(absPath string) bool {
	if absPath == "" {
		return false
	}
	absPath = filepath.Clean(absPath)
	return within(absPath, s.dir) || within(absPath, s.PlansDir()) || s.IsMemory(absPath) || s.IsDoc(absPath)
}

// IsPlanningDestination is IsPlanningDoc for a write: the path must name a
// planning doc and, with symlinks in its existing prefix resolved, still land
// on one, so a link under plans/ or backlog/ cannot redirect a write to code.
// An existing link that cannot be resolved is refused.
func (s *Store) IsPlanningDestination(absPath string) bool {
	if !s.IsPlanningDoc(absPath) {
		return false
	}
	real, ok := resolveExisting(absPath)
	if !ok {
		return false
	}
	for _, dir := range []string{s.dir, s.PlansDir()} {
		if r, ok := resolveExisting(dir); ok && within(real, r) {
			return true
		}
	}
	if r, ok := resolveExisting(s.MemoryPath()); ok && real == r {
		return true
	}
	root := filepath.Dir(s.dir)
	realRoot, ok := resolveExisting(root)
	if !ok {
		return false
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return s.IsDoc(filepath.Join(root, rel))
}

// resolveExisting resolves symlinks in the longest existing prefix of path and
// re-joins the rest. ok is false when an existing entry cannot be resolved
// (for example a dangling link).
func resolveExisting(path string) (string, bool) {
	path = filepath.Clean(path)
	rest := ""
	for p := path; ; p = filepath.Dir(p) {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest), true
		}
		if _, err := os.Lstat(p); err == nil {
			return "", false
		}
		if filepath.Dir(p) == p {
			return "", false
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

// DocsWriteLock serializes a raw file-tool write to a docs-layer path with the
// structured backlog/memory/plan writers, which all hold the same daemon-wide
// per-backlog-dir mutex. ok is false (and nothing is locked) for any other
// path. Holding the store lock across the read-modify-write means a concurrent
// update_task or remember on the same file cannot silently lose either side.
func (s *Store) DocsWriteLock(absPath string) (unlock func(), ok bool) {
	if !s.IsDocsLayer(absPath) {
		return nil, false
	}
	s.mu.Lock()
	return s.mu.Unlock, true
}

// IsBookkeeping reports whether absPath is shared project bookkeeping — a
// backlog task file or memory.md. Every session writes these through the
// structured backlog/memory tools as well as raw edits, so they are not
// attributed to the writing session: they travel with whichever session commits
// next, and a task's own file is always adopted by that task's commit.
func (s *Store) IsBookkeeping(absPath string) bool {
	absPath = filepath.Clean(absPath)
	return within(absPath, s.dir) || s.IsMemory(absPath)
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
