// Package docs implements the structured backlog (spec §6.2): one markdown file
// per task with YAML frontmatter under backlog/, plus a generated backlog.md
// index. It is the canonical store the coordinator reads and updates.
package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// dirLocks serializes backlog mutations per directory. A work session and a
// capture agent (task 0016) use SEPARATE Store instances over the same backlog
// dir, so a per-instance mutex would not serialize them; a package-level
// registry of per-directory locks does. This keeps the generated index from
// being regenerated from a half-written set of task files.
var (
	dirLocksMu sync.Mutex
	dirLocks   = map[string]*sync.Mutex{}
)

func lockFor(dir string) *sync.Mutex {
	dirLocksMu.Lock()
	defer dirLocksMu.Unlock()
	l := dirLocks[dir]
	if l == nil {
		l = &sync.Mutex{}
		dirLocks[dir] = l
	}
	return l
}

// Status is a task's lifecycle state.
type Status string

const (
	StatusTodo       Status = "todo"
	StatusInProgress Status = "in_progress"
	StatusInReview   Status = "in_review"
	StatusDone       Status = "done"
	StatusBlocked    Status = "blocked"
)

// Task is one backlog item. The frontmatter fields round-trip through YAML; Body
// is the markdown after the frontmatter; Path/Slug are filesystem metadata.
type Task struct {
	ID        string   `yaml:"id"`
	Title     string   `yaml:"title"`
	Status    Status   `yaml:"status"`
	Priority  int      `yaml:"priority"`
	Created   string   `yaml:"created"`
	Updated   string   `yaml:"updated"`
	DependsOn []string `yaml:"depends_on"`
	SpecRefs  []string `yaml:"spec_refs"`

	Body string `yaml:"-"`
	Path string `yaml:"-"`
	Slug string `yaml:"-"`
}

// Store is the backlog directory accessor.
type Store struct {
	dir string // <workspace>/backlog
	// mu serializes mutations (and index regeneration) for this backlog dir. It is
	// shared across all Store instances for the same dir via lockFor, so concurrent
	// sessions (e.g. a work session and a quick-add capture agent, task 0016)
	// serialize their writes. It is NON-reentrant: public methods acquire it once
	// and delegate to lock-free *Locked helpers to avoid self-deadlock.
	mu *sync.Mutex
	// cfg is the workspace docs configuration (task 0121): the spec entry-point
	// path and docs-set globs, loaded once at construction from
	// <workspace>/.ycc/config.toml (defaults when absent/malformed).
	cfg specConfig
}

// NewStore returns a Store for the backlog under workspaceRoot.
func NewStore(workspaceRoot string) *Store {
	dir := filepath.Clean(filepath.Join(workspaceRoot, "backlog"))
	return &Store{dir: dir, mu: lockFor(dir), cfg: loadSpecConfig(workspaceRoot)}
}

// Dir returns the backlog directory path.
func (s *Store) Dir() string { return s.dir }

// List returns all tasks sorted by id. Files without YAML frontmatter are skipped.
func (s *Store) List() ([]*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Store) listLocked() ([]*Task, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []*Task
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		t, err := parseFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if t != nil {
			tasks = append(tasks, t)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

// StatusByID indexes tasks by their (normalized) id for dependency lookups.
func StatusByID(tasks []*Task) map[string]Status {
	m := make(map[string]Status, len(tasks))
	for _, t := range tasks {
		m[t.ID] = t.Status
	}
	return m
}

// BlockingDeps returns the ids of t's dependencies that are not yet done,
// according to byID (build it with StatusByID). A dependency id missing from
// byID is treated as blocking — it names a task that does not exist. The result
// is nil when every dependency is done, i.e. t is ready to start.
func BlockingDeps(t *Task, byID map[string]Status) []string {
	var blocking []string
	for _, dep := range t.DependsOn {
		if byID[normalizeID(dep)] != StatusDone {
			blocking = append(blocking, dep)
		}
	}
	return blocking
}

// Get returns the task with the given id.
func (s *Store) Get(id string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(id)
}

func (s *Store) getLocked(id string) (*Task, error) {
	id = normalizeID(id)
	tasks, err := s.listLocked()
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, fmt.Errorf("no task with id %q", id)
}

// Create writes a new task file, assigning the next id and a slug from the title.
func (s *Store) Create(title, body string, priority int, dependsOn, specRefs []string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createLocked(title, body, priority, dependsOn, specRefs)
}

func (s *Store) createLocked(title, body string, priority int, dependsOn, specRefs []string) (*Task, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	id, err := s.nextID()
	if err != nil {
		return nil, err
	}
	today := time.Now().Format("2006-01-02")
	if strings.TrimSpace(body) == "" {
		body = "## Description\n\n## Acceptance criteria\n\n## Work log\n"
	}
	t := &Task{
		ID: id, Title: title, Status: StatusTodo, Priority: priority,
		Created: today, Updated: today,
		DependsOn: dependsOn, SpecRefs: specRefs,
		Body: ensureWorkLog(body), Slug: slugify(title),
	}
	t.Path = filepath.Join(s.dir, id+"-"+t.Slug+".md")
	if err := s.write(t); err != nil {
		return nil, err
	}
	return t, nil
}

// Update loads a task, applies mut, bumps Updated, and writes it back.
func (s *Store) Update(id string, mut func(*Task)) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(id, mut)
}

func (s *Store) updateLocked(id string, mut func(*Task)) (*Task, error) {
	t, err := s.getLocked(id)
	if err != nil {
		return nil, err
	}
	mut(t)
	t.Updated = time.Now().Format("2006-01-02")
	if err := s.write(t); err != nil {
		return nil, err
	}
	return t, nil
}

// AppendWorkLog appends a dated bullet under the task's "## Work log" section.
func (s *Store) AppendWorkLog(id, line string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Delegate to the lock-free helper (NOT the exported Update, which would
	// re-lock the non-reentrant mutex and deadlock).
	return s.updateLocked(id, func(t *Task) {
		t.Body = ensureWorkLog(t.Body)
		if !strings.HasSuffix(t.Body, "\n") {
			t.Body += "\n"
		}
		t.Body += fmt.Sprintf("- %s %s\n", time.Now().Format("2006-01-02"), strings.TrimSpace(line))
	})
}

// SetPlan upserts a "## Plan" section into the task body, persisting the FULL
// coordinator plan next to its task (task 0020). The section is placed just above
// "## Work log" when present, else appended. Repeated calls REPLACE the section's
// content rather than appending duplicate sections.
func (s *Store) SetPlan(id, plan string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(id, func(t *Task) {
		t.Body = upsertSection(t.Body, "## Plan", plan)
	})
}

// upsertSection inserts or replaces a markdown section (identified by its header
// line, e.g. "## Plan") in body with the given content. When the section already
// exists its content is replaced in place; otherwise the section is inserted just
// above a "## Work log" section if one exists, else appended at the end.
func upsertSection(body, header, content string) string {
	content = strings.Trim(content, "\n")
	section := header + "\n\n" + content + "\n"
	lines := strings.Split(body, "\n")
	start := -1
	for i, ln := range lines {
		if strings.TrimRight(ln, " \t") == header {
			start = i
			break
		}
	}
	if start >= 0 {
		// Find the end of this section: the next top-level "## " header, or EOF.
		end := len(lines)
		for i := start + 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "## ") {
				end = i
				break
			}
		}
		before := strings.Join(lines[:start], "\n")
		after := strings.Join(lines[end:], "\n")
		var b strings.Builder
		b.WriteString(strings.TrimRight(before, "\n"))
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(section)
		rest := strings.TrimLeft(after, "\n")
		if rest != "" {
			b.WriteString("\n")
			b.WriteString(rest)
		}
		return b.String()
	}
	// No existing section. Insert above "## Work log" if present, else append.
	wl := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## Work log") {
			wl = i
			break
		}
	}
	if wl >= 0 {
		before := strings.TrimRight(strings.Join(lines[:wl], "\n"), "\n")
		after := strings.Join(lines[wl:], "\n")
		var b strings.Builder
		if before != "" {
			b.WriteString(before)
			b.WriteString("\n\n")
		}
		b.WriteString(section)
		b.WriteString("\n")
		b.WriteString(after)
		return b.String()
	}
	out := strings.TrimRight(body, "\n")
	if out != "" {
		out += "\n\n"
	}
	return out + section
}

// RenderIndex regenerates backlog.md (in the workspace root) from the task files.
func (s *Store) RenderIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renderIndexLocked()
}

func (s *Store) renderIndexLocked() error {
	tasks, err := s.listLocked()
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Backlog\n\n> Generated index. Canonical task data lives in `backlog/<id>-<slug>.md`.\n\n")
	b.WriteString("| id | title | status | pri | depends on |\n|----|-------|--------|-----|------------|\n")
	for _, t := range tasks {
		dep := strings.Join(t.DependsOn, ", ")
		if dep == "" {
			dep = "—"
		}
		rel := filepath.Join("backlog", filepath.Base(t.Path))
		b.WriteString(fmt.Sprintf("| [%s](%s) | %s | %s | %d | %s |\n", t.ID, rel, t.Title, t.Status, t.Priority, dep))
	}
	indexPath := filepath.Join(filepath.Dir(s.dir), "backlog.md")
	return os.WriteFile(indexPath, []byte(b.String()), 0o644)
}

func (s *Store) nextID() (string, error) {
	tasks, err := s.listLocked()
	if err != nil {
		return "", err
	}
	max := 0
	for _, t := range tasks {
		if n, err := strconv.Atoi(t.ID); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("%04d", max+1), nil
}

func (s *Store) write(t *Task) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	front, err := yaml.Marshal(t)
	if err != nil {
		return err
	}
	body := t.Body
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	content := "---\n" + string(front) + "---\n\n" + strings.TrimLeft(body, "\n")
	return os.WriteFile(t.Path, []byte(content), 0o644)
}

var frontmatterRe = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n?(.*)\z`)

func parseFile(path string) (*Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := frontmatterRe.FindSubmatch(data)
	if m == nil {
		return nil, nil // not a task file
	}
	var t Task
	if err := yaml.Unmarshal(m[1], &t); err != nil {
		return nil, fmt.Errorf("invalid frontmatter: %w", err)
	}
	t.Body = strings.TrimLeft(string(m[2]), "\n")
	t.Path = path
	t.Slug = strings.TrimSuffix(filepath.Base(path), ".md")
	if i := strings.IndexByte(t.Slug, '-'); i >= 0 {
		t.Slug = t.Slug[i+1:]
	}
	return &t, nil
}

func ensureWorkLog(body string) string {
	if strings.Contains(body, "## Work log") {
		return body
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + "\n## Work log\n"
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = strings.Trim(s[:50], "-")
	}
	if s == "" {
		s = "task"
	}
	return s
}

func normalizeID(id string) string {
	id = strings.TrimSpace(id)
	if n, err := strconv.Atoi(id); err == nil {
		return fmt.Sprintf("%04d", n)
	}
	return id
}
