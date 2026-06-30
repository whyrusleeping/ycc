package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PlanInfo describes one reusable plan (runbook) in the in-repo plan library.
type PlanInfo struct {
	Name  string // file name without the .md extension (the invocation key)
	Title string // first markdown heading text, else Name
	Path  string // absolute path to the plan file
}

// PlansDir returns the in-repo plan-library directory (<workspace>/plans). Plans
// are committed, version-controlled markdown runbooks — reusable procedures that
// are distinct from the backlog (tasks are one-off work items). See task 0020.
func (s *Store) PlansDir() string {
	return filepath.Join(filepath.Dir(s.dir), "plans")
}

// ListPlans returns the plans in the library sorted by name. A missing directory
// yields a nil slice (not an error). Non-.md files are skipped.
func (s *Store) ListPlans() ([]PlanInfo, error) {
	dir := s.PlansDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var plans []PlanInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		path := filepath.Join(dir, e.Name())
		plans = append(plans, PlanInfo{Name: name, Title: planTitle(path, name), Path: path})
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].Name < plans[j].Name })
	return plans, nil
}

// ReadPlan returns the markdown content of a saved plan. The name may be given
// with or without the trailing ".md".
func (s *Store) ReadPlan(name string) (string, error) {
	path, err := s.planPath(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("no plan named %q", strings.TrimSuffix(filepath.Base(path), ".md"))
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SavePlan writes (or overwrites) a plan in the library, returning the saved name
// (the sanitized slug, without the .md extension). The name is slugified so it is
// a safe file name; content is normalized to end with a newline.
func (s *Store) SavePlan(name, content string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("plan name is required")
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("plan content is required")
	}
	slug := slugify(name)
	path, err := s.planPath(slug)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.PlansDir(), 0o755); err != nil {
		return "", err
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return slug, nil
}

// planPath resolves a plan name to its file path, guarding against path traversal
// (the resolved path must stay within PlansDir).
func (s *Store) planPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".md")
	if name == "" {
		return "", fmt.Errorf("plan name is required")
	}
	dir := s.PlansDir()
	path := filepath.Join(dir, name+".md")
	// Confine within the plans dir (defense in depth; slugify also strips separators).
	if filepath.Dir(path) != filepath.Clean(dir) {
		return "", fmt.Errorf("invalid plan name %q", name)
	}
	return path, nil
}

// planTitle returns the first markdown heading text in the file, else fallback.
func planTitle(path, fallback string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "#") {
			title := strings.TrimSpace(strings.TrimLeft(ln, "#"))
			if title != "" {
				return title
			}
		}
	}
	return fallback
}
