package docs

import (
	"os"
	"path/filepath"
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
