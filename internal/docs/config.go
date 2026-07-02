package docs

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// specConfig is the optional per-workspace docs configuration loaded from
// <workspace>/.ycc/config.toml (task 0121). It names the spec ENTRY POINT and
// the docs SET that together make up the project's design-documentation surface.
// Both fields are optional; a missing or malformed config falls back to defaults
// (spec.md at the workspace root, no extra doc globs) and never fails Store
// construction.
type specConfig struct {
	// SpecPath is the workspace-relative path to the spec entry point (default
	// "spec.md"). A configured value that escapes the workspace (absolute, or
	// ".." after Clean) is rejected and falls back to the default.
	SpecPath string `toml:"spec_path"`
	// DocGlobs lists slash-separated globs, relative to the workspace root, that
	// name the rest of the docs set (e.g. "docs/**", "ARCHITECTURE.md", "adr/*.md").
	// An edit to any matching file — or to the entry point — is treated as a docs
	// edit (IsDoc) and fires doc_updated.
	DocGlobs []string `toml:"doc_globs"`
}

// defaultSpecPath is the workspace-relative spec entry point when none is
// configured: spec.md at the workspace root, alongside backlog/.
const defaultSpecPath = "spec.md"

// loadSpecConfig reads <workspaceRoot>/.ycc/config.toml, tolerating a missing or
// malformed file by returning defaults. The returned SpecPath is always a clean,
// workspace-relative slash path that never escapes the workspace.
func loadSpecConfig(workspaceRoot string) specConfig {
	cfg := specConfig{SpecPath: defaultSpecPath}
	data, err := os.ReadFile(filepath.Join(workspaceRoot, ".ycc", "config.toml"))
	if err != nil {
		return cfg
	}
	var raw specConfig
	if err := toml.Unmarshal(data, &raw); err != nil {
		return cfg // garbage config → defaults
	}
	if p := cleanRelPath(raw.SpecPath); p != "" {
		cfg.SpecPath = p
	}
	cfg.DocGlobs = raw.DocGlobs
	return cfg
}

// cleanRelPath normalizes a configured workspace-relative path to a clean slash
// path. It returns "" when the path is empty, absolute, or escapes the workspace
// (a leading ".." after cleaning), so callers fall back to their default.
func cleanRelPath(p string) string {
	p = strings.TrimSpace(filepath.ToSlash(p))
	if p == "" || strings.HasPrefix(p, "/") {
		return ""
	}
	cleaned := filepath.ToSlash(filepath.Clean(p))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}

// matchGlob reports whether the slash-separated path name matches the glob
// pattern. Supported wildcards:
//   - '?'  matches any single character except '/'
//   - '*'  matches any run (possibly empty) of characters except '/' (i.e. it
//     stays within a single path segment)
//   - '**' matches any run (possibly empty) of characters INCLUDING '/', so it
//     spans directories; a trailing "/" after "**" is optional, so "docs/**"
//     matches "docs/a" and "docs/a/b" alike, and "**/x" matches "x" as well as
//     "a/b/x".
//
// All other characters match literally. Both pattern and name are expected to be
// slash-separated (the caller passes workspace-relative slash paths).
func matchGlob(pattern, name string) bool {
	if pattern == "" {
		return name == ""
	}
	switch {
	case strings.HasPrefix(pattern, "**"):
		rest := pattern[2:]
		// "**/" may match zero leading directories.
		if strings.HasPrefix(rest, "/") && matchGlob(rest[1:], name) {
			return true
		}
		// '**' consumes any prefix of name, including '/'.
		for j := 0; j <= len(name); j++ {
			if matchGlob(rest, name[j:]) {
				return true
			}
		}
		return false
	case pattern[0] == '*':
		rest := pattern[1:]
		for j := 0; j <= len(name); j++ {
			if matchGlob(rest, name[j:]) {
				return true
			}
			if j < len(name) && name[j] == '/' {
				break // '*' does not cross a path separator
			}
		}
		return false
	case pattern[0] == '?':
		if name == "" || name[0] == '/' {
			return false
		}
		return matchGlob(pattern[1:], name[1:])
	default:
		if name == "" || name[0] != pattern[0] {
			return false
		}
		return matchGlob(pattern[1:], name[1:])
	}
}
