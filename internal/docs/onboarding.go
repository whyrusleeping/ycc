package docs

import (
	"os"
	"path/filepath"
	"strings"
)

// NeedsOnboarding reports whether a workspace has neither substantive content
// in its configured spec entry point nor any backlog task files. It is
// conservative: an empty workspace path or an unexpected spec read error is
// treated as already onboarded so clients do not recommend onboarding
// spuriously.
func NeedsOnboarding(workspace string) bool {
	if strings.TrimSpace(workspace) == "" {
		return false
	}
	return SpecIsEmpty(workspace) && !HasBacklogTasks(workspace)
}

// SpecIsEmpty reports whether the configured spec entry point is missing or
// contains only blank lines and Markdown headings.
func SpecIsEmpty(workspace string) bool {
	data, err := os.ReadFile(NewStore(workspace).SpecPath())
	if err != nil {
		if os.IsNotExist(err) {
			return true
		}
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return false
	}
	return true
}

// HasBacklogTasks reports whether backlog/ contains at least one task file
// matching the NNNN-*.md naming convention. Other Markdown files do not count.
func HasBacklogTasks(workspace string) bool {
	entries, err := os.ReadDir(filepath.Join(workspace, "backlog"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		stem := strings.TrimSuffix(name, ".md")
		dash := strings.IndexByte(stem, '-')
		if dash > 0 && allDigits(stem[:dash]) {
			return true
		}
	}
	return false
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
