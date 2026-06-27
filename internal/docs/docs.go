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
	"time"

	"gopkg.in/yaml.v3"
)

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
}

// NewStore returns a Store for the backlog under workspaceRoot.
func NewStore(workspaceRoot string) *Store {
	return &Store{dir: filepath.Join(workspaceRoot, "backlog")}
}

// Dir returns the backlog directory path.
func (s *Store) Dir() string { return s.dir }

// List returns all tasks sorted by id. Files without YAML frontmatter are skipped.
func (s *Store) List() ([]*Task, error) {
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
	id = normalizeID(id)
	tasks, err := s.List()
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
	t, err := s.Get(id)
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
	return s.Update(id, func(t *Task) {
		t.Body = ensureWorkLog(t.Body)
		if !strings.HasSuffix(t.Body, "\n") {
			t.Body += "\n"
		}
		t.Body += fmt.Sprintf("- %s %s\n", time.Now().Format("2006-01-02"), strings.TrimSpace(line))
	})
}

// RenderIndex regenerates backlog.md (in the workspace root) from the task files.
func (s *Store) RenderIndex() error {
	tasks, err := s.List()
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
	tasks, err := s.List()
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
