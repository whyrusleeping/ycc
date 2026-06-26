package docs

import (
	"os"
	"path/filepath"
	"strings"
)

// SpecPath returns the spec.md path (workspace root, alongside backlog/).
func (s *Store) SpecPath() string {
	return filepath.Join(filepath.Dir(s.dir), "spec.md")
}

// ReadSpec returns the full spec.md, or "" if it does not exist yet.
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

// SpecSections returns the level-2 ("## ") section titles in spec.md, in order.
func (s *Store) SpecSections() ([]string, error) {
	body, err := s.ReadSpec()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if t, ok := sectionTitle(line); ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// UpdateSpecSection replaces the body of the named "## " section with content,
// appending the section if it does not exist. The header line itself is
// preserved/created; content is the markdown that follows it. This keeps edits
// section-scoped so concurrent edits don't clobber the whole file (spec §6.1).
func (s *Store) UpdateSpecSection(section, content string) error {
	section = strings.TrimSpace(section)
	existing, err := s.ReadSpec()
	if err != nil {
		return err
	}
	if existing == "" {
		existing = "# Spec\n"
	}
	lines := strings.Split(existing, "\n")

	start := -1
	for i, line := range lines {
		if t, ok := sectionTitle(line); ok && strings.EqualFold(t, section) {
			start = i
			break
		}
	}

	body := strings.TrimRight(content, "\n")
	if start == -1 {
		// Append a new section.
		out := strings.TrimRight(existing, "\n") + "\n\n## " + section + "\n\n" + body + "\n"
		return os.WriteFile(s.SpecPath(), []byte(out), 0o644)
	}
	// Find the end of this section (next "## " header or EOF).
	end := len(lines)
	for j := start + 1; j < len(lines); j++ {
		if _, ok := sectionTitle(lines[j]); ok {
			end = j
			break
		}
	}
	newLines := append([]string{}, lines[:start+1]...)
	newLines = append(newLines, "", body, "")
	newLines = append(newLines, lines[end:]...)
	out := strings.TrimRight(strings.Join(newLines, "\n"), "\n") + "\n"
	return os.WriteFile(s.SpecPath(), []byte(out), 0o644)
}

// sectionTitle returns the title of a "## " header line, if it is one.
func sectionTitle(line string) (string, bool) {
	if strings.HasPrefix(line, "## ") {
		return strings.TrimSpace(line[3:]), true
	}
	return "", false
}
