package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// memorySoftBudget is the size (bytes) beyond which memory.md is considered
// "over budget" and due for grooming. Memory is injected wholesale into every
// agent's system prompt (spec §6.5) — there is no retrieval machinery — so it
// must stay small. Crucially, crossing the soft budget does NOT block a write:
// AppendMemory still records the note (a learning is never lost mid-task) and
// returns an escalating nudge to groom. A hard refusal only fires at
// memoryHardBudget, so there is generous runway before any write is blocked.
const memorySoftBudget = 4096

// memoryHardBudget is the absolute size ceiling (bytes). Only when the existing
// file is already at/over this does AppendMemory refuse, with actionable
// guidance to consolidate first. This catches a genuinely runaway file while
// keeping the common "jot a note" path from failing at the soft boundary.
const memoryHardBudget = 12288

// memoryEntryHint is the soft per-entry length (bytes) over which AppendMemory
// nudges toward a terser note. Long, prose-y entries burn the budget fast; the
// note is still recorded exactly as written.
const memoryEntryHint = 240

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

// MemoryWrite reports the outcome of an AppendMemory call so the caller can
// surface an advisory nudge to the model. The note is always recorded on
// success; Advice is a human-readable grooming/terseness hint, or "" when
// nothing is worth flagging.
type MemoryWrite struct {
	Size   int    // total bytes of memory.md after the append
	Advice string // grooming / terseness nudge to surface, or ""
}

// AppendMemory appends a dated bullet entry to memory.md under the section for
// the given category (default "lesson"; an unknown category is an error). It
// creates the file with the advisory header and/or the section when missing.
// Empty notes are rejected. The write is refused only when the existing file is
// already at/over memoryHardBudget (a runaway file); crossing the soft budget
// still records the note and returns grooming Advice instead of failing, so a
// learning is never lost mid-task.
func (s *Store) AppendMemory(note, category string) (MemoryWrite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	note = strings.TrimSpace(strings.ReplaceAll(note, "\n", " "))
	if note == "" {
		return MemoryWrite{}, fmt.Errorf("note is required")
	}
	category = strings.TrimSpace(category)
	if category == "" {
		category = "lesson"
	}
	header, ok := memoryCategories[category]
	if !ok {
		return MemoryWrite{}, fmt.Errorf("unknown category %q (want environment, gotcha, preference, or lesson)", category)
	}

	path := s.MemoryPath()
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return MemoryWrite{}, err
	}
	if len(existing) >= memoryHardBudget {
		return MemoryWrite{}, fmt.Errorf("memory.md is over the hard ceiling (%d bytes ≥ %d) — consolidate first: dedupe, prune stale entries, "+
			"merge repeats, and promote hardened observations to spec/plans/backlog before adding more", len(existing), memoryHardBudget)
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
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return MemoryWrite{}, err
	}
	return MemoryWrite{Size: len(body), Advice: memoryAdvice(len(body), len(note))}, nil
}

// memoryAdvice builds the advisory nudge returned by AppendMemory: a grooming
// prompt once the file crosses the soft budget, and/or a terseness prompt when a
// single entry ran long. Returns "" when neither applies.
func memoryAdvice(size, noteLen int) string {
	var parts []string
	if size >= memorySoftBudget {
		parts = append(parts, fmt.Sprintf("memory.md is now %d bytes, over its %d-byte soft budget — "+
			"please run the memory-groom flow soon (dedupe, prune stale entries, promote hardened notes to spec/plans/backlog) "+
			"to keep it small enough to inject into every session", size, memorySoftBudget))
	}
	if noteLen > memoryEntryHint {
		parts = append(parts, fmt.Sprintf("that entry was long (%d chars) — memory entries are cheapest when terse (aim for ≤%d chars)", noteLen, memoryEntryHint))
	}
	return strings.Join(parts, "; ")
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
