package docs

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SpecPath returns the absolute path to the spec ENTRY POINT — the single
// well-known orientation document for the project (spec §6.1). It defaults to
// spec.md at the workspace root (alongside backlog/), and may be pointed at a
// different file via the `spec_path` setting in <workspace>/.ycc/config.toml.
func (s *Store) SpecPath() string {
	rel := s.cfg.SpecPath
	if rel == "" {
		rel = defaultSpecPath
	}
	return filepath.Join(filepath.Dir(s.dir), filepath.FromSlash(rel))
}

// ReadSpec returns the full spec entry point, or "" if it does not exist yet.
func (s *Store) ReadSpec() (string, error) {
	data, err := os.ReadFile(s.SpecPath())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// IsDoc reports whether absPath is part of the project's docs set (spec §6.1):
// the spec entry point itself, or any file matching one of the configured
// `doc_globs`. Globs are matched against the workspace-relative slash path; a
// path outside the workspace never matches. This drives doc_updated so an edit
// anywhere in the docs set — not just to the entry point — is surfaced.
func (s *Store) IsDoc(absPath string) bool {
	if absPath == s.SpecPath() {
		return true
	}
	root := filepath.Dir(s.dir)
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return false // outside the workspace
	}
	for _, g := range s.cfg.DocGlobs {
		g = strings.TrimSpace(filepath.ToSlash(g))
		if g == "" {
			continue
		}
		if matchGlob(g, rel) {
			return true
		}
	}
	return false
}

// DocFiles enumerates the project's docs set (spec §6.1) as absolute paths: the
// spec entry point (when it exists) plus every workspace file matching a
// configured `doc_glob`. It walks the workspace once (skipping .git), matches the
// workspace-relative slash path with the same glob rules as IsDoc, dedupes, and
// returns a stable (sorted) order. Directories and the backlog are not included.
func (s *Store) DocFiles() ([]string, error) {
	root := filepath.Dir(s.dir)
	seen := map[string]bool{}
	var out []string
	add := func(abs string) {
		if !seen[abs] {
			seen[abs] = true
			out = append(out, abs)
		}
	}

	if sp := s.SpecPath(); sp != "" {
		if fi, err := os.Stat(sp); err == nil && !fi.IsDir() {
			add(sp)
		}
	}

	globs := make([]string, 0, len(s.cfg.DocGlobs))
	for _, g := range s.cfg.DocGlobs {
		if g = strings.TrimSpace(filepath.ToSlash(g)); g != "" {
			globs = append(globs, g)
		}
	}
	if len(globs) > 0 {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			for _, g := range globs {
				if matchGlob(g, rel) {
					add(path)
					break
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	sort.Strings(out)
	return out, nil
}
