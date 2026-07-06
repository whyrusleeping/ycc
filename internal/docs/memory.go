package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// memoryBudget is the hard size ceiling (bytes) for the committed memory.md.
// Memory must stay small enough to inject wholesale into every agent's system
// prompt (spec §6.5) — there is no retrieval machinery. When the existing file
// is already at or over this budget, AppendMemory refuses and asks the caller to
// consolidate first (dedupe, prune, promote to spec/plans/backlog).
const memoryBudget = 4096

// memoryHeader is written when memory.md is first created. It states the
// advisory, non-normative contract (design doc §5.1): memory is empirical agent
// notes about WORKING ON the project, not design truth.
const memoryHeader = `# Project memory

> Agent-maintained operational notes. Advisory, not normative — verify before relying.
> Design truth belongs in spec.md; procedures in plans/; work items in backlog/.
`

// memoryCategories maps a remember-tool category to its markdown section header.
// The default category is "lesson"; an unknown category is an error.
var memoryCategories = map[string]string{
	"environment": "## Environment & tooling",
	"gotcha":      "## Codebase gotchas",
	"preference":  "## User preferences",
	"lesson":      "## Lessons learned",
}

// MemoryPath returns the absolute path to the committed project memory file —
// memory.md at the workspace root, beside spec.md and backlog/ (spec §6.5). The
// location is fixed, not configurable.
func (s *Store) MemoryPath() string {
	return filepath.Join(filepath.Dir(s.dir), "memory.md")
}

// ReadMemory returns the full contents of memory.md, or "" if it does not exist.
func (s *Store) ReadMemory() (string, error) {
	data, err := os.ReadFile(s.MemoryPath())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// IsMemory reports whether absPath is the project memory file. Memory joins the
// docs set for eventing (writes emit doc_updated) but is explicitly NOT spec: the
// spec doctor / spec-check must never treat its entries as normative claims.
func (s *Store) IsMemory(absPath string) bool {
	return absPath == s.MemoryPath()
}

// AppendMemory appends a dated bullet entry to memory.md under the section for
// the given category (default "lesson"; an unknown category is an error). It
// creates the file with the advisory header and/or the section when missing.
// The write is refused when the existing file is already at/over memoryBudget,
// with actionable guidance to consolidate first. Empty notes are rejected.
func (s *Store) AppendMemory(note, category string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	note = strings.TrimSpace(strings.ReplaceAll(note, "\n", " "))
	if note == "" {
		return fmt.Errorf("note is required")
	}
	category = strings.TrimSpace(category)
	if category == "" {
		category = "lesson"
	}
	header, ok := memoryCategories[category]
	if !ok {
		return fmt.Errorf("unknown category %q (want environment, gotcha, preference, or lesson)", category)
	}

	path := s.MemoryPath()
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(existing) >= memoryBudget {
		return fmt.Errorf("memory.md is over budget (%d bytes ≥ %d) — consolidate first: dedupe, prune stale entries, "+
			"merge repeats, and promote hardened observations to spec/plans/backlog before adding more", len(existing), memoryBudget)
	}

	body := string(existing)
	if strings.TrimSpace(body) == "" {
		body = memoryHeader
	}
	entry := fmt.Sprintf("- %s: %s", time.Now().Format("2006-01-02"), note)
	body = appendMemoryEntry(body, header, entry)

	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

// appendMemoryEntry inserts entry as the last bullet of the section identified by
// header, creating the section (appended at the end) when it does not yet exist.
func appendMemoryEntry(body, header, entry string) string {
	lines := strings.Split(body, "\n")
	start := -1
	for i, ln := range lines {
		if strings.TrimRight(ln, " \t") == header {
			start = i
			break
		}
	}
	if start < 0 {
		// No such section: append it at the end.
		out := strings.TrimRight(body, "\n")
		return out + "\n\n" + header + "\n" + entry + "\n"
	}
	// Find the end of this section: the next "## " header, or EOF.
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	// Insert the entry after the last non-blank line of the section.
	insert := start
	for i := start + 1; i < end; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			insert = i
		}
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:insert+1]...)
	out = append(out, entry)
	out = append(out, lines[insert+1:]...)
	return strings.Join(out, "\n")
}
