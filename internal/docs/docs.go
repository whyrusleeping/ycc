// Package docs implements the structured backlog: one markdown file
// per task with YAML frontmatter under backlog/. It is the canonical store the
// coordinator reads and updates.
package docs

import (
	"bufio"
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
// capture agent use SEPARATE Store instances over the same backlog
// dir, so a per-instance mutex would not serialize them; a package-level
// registry of per-directory locks does. This keeps concurrent mutations (e.g.
// next-id assignment) from racing and corrupting the task files.
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
	// StatusProposed marks an idea captured during ideation that the user has
	// not (yet) accepted as real scope. Proposed tasks are durable backlog
	// entries but sit BEFORE todo in the lifecycle: they are never "ready",
	// so the work pipeline does not pick them up. Promotion to todo is the
	// explicit acceptance act.
	StatusProposed   Status = "proposed"
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
	// mu serializes mutations for this backlog dir. It is
	// shared across all Store instances for the same dir via lockFor, so concurrent
	// sessions (e.g. a work session and a quick-add capture agent)
	// serialize their writes. It is NON-reentrant: public methods acquire it once
	// and delegate to lock-free *Locked helpers to avoid self-deadlock.
	mu *sync.Mutex
	// cfg is the workspace docs configuration: the spec entry-point
	// path and docs-set globs, loaded once at construction from
	// <workspace>/.ycc/config.toml (defaults when absent/malformed).
	cfg specConfig
	// idSource, when set by a daemon-owned session manager, reserves ids from a
	// per-project allocator rather than scanning this Store's current tree.
	idSource func() (string, error)
	// repairLease is a non-blocking daemon ownership hook used only after a scan
	// discovers duplicate IDs. Ordinary reads never call it. When acquisition is
	// refused, the read returns the raw tasks and leaves repair for a later read.
	repairLease func() (release func(), err error)
}

// NewStore returns a Store for the backlog under workspaceRoot.
func NewStore(workspaceRoot string) *Store {
	dir := filepath.Clean(filepath.Join(workspaceRoot, "backlog"))
	return &Store{dir: dir, mu: lockFor(dir), cfg: loadSpecConfig(workspaceRoot)}
}

// Dir returns the backlog directory path.
func (s *Store) Dir() string { return s.dir }

// SetIDSource configures the daemon-owned id reservation function used by
// Create. Stores without an id source retain the daemon-less scan fallback.
// Configure it before using the Store.
func (s *Store) SetIDSource(fn func() (string, error)) { s.idSource = fn }

// SetRepairLease configures the daemon ownership hook for exceptional duplicate
// repair. Stores without a hook retain daemon-less automatic self-healing.
// Configure it before using the Store.
func (s *Store) SetRepairLease(fn func() (release func(), err error)) { s.repairLease = fn }

// beginRepair returns a release function when duplicate repair may proceed.
// Acquisition failure is deliberately best-effort: apparent read methods remain
// successful and non-mutating while another execution owns the worktree.
func (s *Store) beginRepair() (func(), bool) {
	if s.repairLease == nil {
		return func() {}, true
	}
	release, err := s.repairLease()
	if err != nil {
		return nil, false
	}
	if release == nil {
		release = func() {}
	}
	return release, true
}

// List returns all tasks, including their bodies, sorted by id. Files without
// YAML frontmatter are skipped. Summary and dependency callers should prefer
// ListMetadata so completed-task history is not read unnecessarily.
func (s *Store) List() ([]*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

// ListMetadata returns all tasks without reading their Markdown bodies. It is
// the listing path for backlog summaries, readiness checks, and task selection.
// Get remains the full-fidelity lookup for callers that need one task's body.
func (s *Store) ListMetadata() ([]*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listMetadataLocked()
}

func (s *Store) listMetadataLocked() ([]*Task, error) {
	tasks, err := s.scanMetadataLocked()
	if err != nil {
		return nil, err
	}
	if !hasDuplicateIDs(tasks) {
		return tasks, nil
	}
	// Duplicate repair rewrites a claimant and records a breadcrumb, so acquire
	// daemon ownership and load bodies only on this exceptional path. Re-scan
	// after acquisition because an unrestricted writer may have changed files
	// between the detecting scan and the lease claim.
	release, ok := s.beginRepair()
	if !ok {
		return tasks, nil
	}
	defer release()
	full, err := s.scanLocked()
	if err != nil {
		return nil, err
	}
	if _, err := s.dedupeLocked(full); err != nil {
		return tasks, nil
	}
	return s.scanMetadataLocked()
}

// listLocked scans the backlog and self-heals duplicate ids before returning
// (see dedupe.go): two files claiming the same id make one of them unreachable
// by id (Get, update_task, the TUI browser all resolve to the first match), so
// the store renumbers the younger claimant onto a fresh id. Healing is
// best-effort — if the rewrite fails (read-only dir, races) the raw scan is
// still returned so reads keep working.
func (s *Store) listLocked() ([]*Task, error) {
	tasks, err := s.scanLocked()
	if err != nil {
		return nil, err
	}
	if !hasDuplicateIDs(tasks) {
		return tasks, nil
	}
	release, ok := s.beginRepair()
	if !ok {
		return tasks, nil
	}
	defer release()
	// Re-scan after ownership acquisition before choosing which claimant moves.
	tasks, err = s.scanLocked()
	if err != nil {
		return nil, err
	}
	if _, err := s.dedupeLocked(tasks); err != nil {
		return tasks, nil
	}
	return s.scanLocked()
}

// scanLocked parses every task file in the backlog dir, sorted by id. It does
// NOT heal duplicate ids (listLocked does) so it can be used from the dedupe
// pass itself without recursing.
func (s *Store) scanLocked() ([]*Task, error) {
	return s.scanFilesLocked(parseFile)
}

// scanMetadataLocked parses only YAML frontmatter. In particular, it does not
// read completed-task outcomes (or legacy work logs) merely to render a list or
// resolve dependencies.
func (s *Store) scanMetadataLocked() ([]*Task, error) {
	return s.scanFilesLocked(parseMetadataFile)
}

func (s *Store) scanFilesLocked(parse func(string) (*Task, error)) ([]*Task, error) {
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
		t, err := parse(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if t != nil {
			// Normalize on read so a hand-written `id: "195"` still matches the
			// canonical zero-padded form used by lookups and dedupe grouping.
			t.ID = normalizeID(t.ID)
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
	tasks, err := s.listMetadataLocked()
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.ID == id {
			return parseFile(t.Path)
		}
	}
	return nil, fmt.Errorf("no task with id %q", id)
}

// Create writes a new task file, assigning the next id and a slug from the title.
// The task starts in the default accepted state (todo).
func (s *Store) Create(title, body string, priority int, dependsOn, specRefs []string) (*Task, error) {
	return s.CreateWithStatus(title, body, priority, dependsOn, specRefs, StatusTodo)
}

// CreateWithStatus is Create with an explicit initial status — e.g.
// StatusProposed for ideas captured during ideation that the user has not yet
// accepted. An empty status defaults to todo.
func (s *Store) CreateWithStatus(title, body string, priority int, dependsOn, specRefs []string, status Status) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createLocked(title, body, priority, dependsOn, specRefs, status)
}

func (s *Store) createLocked(title, body string, priority int, dependsOn, specRefs []string, status Status) (*Task, error) {
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
	if status == "" {
		status = StatusTodo
	}
	t := &Task{
		ID: id, Title: title, Status: status, Priority: priority,
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
		t.Body = appendWorkLogLine(t.Body, time.Now().Format("2006-01-02"), strings.TrimSpace(line))
	})
}

// SetPlan upserts a "## Plan" section into the task body, persisting the FULL
// coordinator plan next to its task. The section is placed just above
// "## Work log" when present, else appended. Repeated calls REPLACE the section's
// content rather than appending duplicate sections.
func (s *Store) SetPlan(id, plan string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(id, func(t *Task) {
		t.Body = upsertSection(t.Body, "## Plan", plan)
	})
}

// Complete marks a task done and replaces its operational body with the compact
// durable record: original intent, acceptance criteria, concise outcome, and the
// accepted commit subject. Session events and git retain the detailed execution
// history. It is intended to run immediately before the accepting commit.
func (s *Store) Complete(id, outcome, commitSubject string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	outcome, err := completedText("outcome", outcome, maxOutcomeRunes)
	if err != nil {
		return nil, err
	}
	commitSubject, err = completedText("commit subject", commitSubject, maxCommitRunes)
	if err != nil {
		return nil, err
	}
	return s.updateLocked(id, func(t *Task) {
		t.Status = StatusDone
		t.Body = compactCompletedBody(t.Body, outcome, commitSubject)
	})
}

// CompactCompletedHistory conservatively migrates legacy done tasks. A task is
// eligible only when canonical non-empty intent and acceptance criteria plus a
// complete final implementer/revision outcome and concise commit subject can all
// be extracted. Dry-run mode reports ids without writing; repeated write runs are
// no-ops. Git retains
// every removed body as the rollback path.
func (s *Store) CompactCompletedHistory(write bool, exclude ...string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	excluded := make(map[string]bool, len(exclude))
	for _, id := range exclude {
		excluded[normalizeID(id)] = true
	}
	tasks, err := s.listLocked()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, task := range tasks {
		if excluded[task.ID] {
			continue
		}
		outcome, subject, ok := legacyCompletion(task)
		if !ok {
			continue
		}
		ids = append(ids, task.ID)
		if write {
			task.Body = compactCompletedBody(task.Body, outcome, subject)
			// Preserve the historical completion date; migration is a storage-shape
			// change, not new task activity.
			if err := s.write(task); err != nil {
				return ids[:len(ids)-1], err
			}
		}
	}
	return ids, nil
}

func legacyCompletion(task *Task) (outcome, subject string, ok bool) {
	if task.Status != StatusDone || hasHeaderLine(task.Body, "outcome") {
		return "", "", false
	}
	if collectSectionContent(task.Body, "description") == "" || collectSectionContent(task.Body, "acceptance criteria") == "" {
		return "", "", false
	}

	lines := strings.Split(task.Body, "\n")
	outcomeAt, commitAt := -1, -1
	outcomeOK, commitOK := false, false
	for i, line := range lines {
		// Keep replacing the candidate: a later revision supersedes the initial
		// implementer report even when that later revision is itself incomplete.
		for _, marker := range []string{"implementer report:", "revision:"} {
			if at := strings.Index(line, marker); at >= 0 {
				outcome = strings.TrimSpace(line[at+len(marker):])
				outcomeAt = i
				normalized, err := completedText("outcome", outcome, maxOutcomeRunes)
				outcomeOK = err == nil && legacyLineComplete(lines, i)
				if outcomeOK {
					outcome = normalized
				}
				break
			}
		}
		if !strings.Contains(line, "decision: accept") {
			continue
		}
		candidate := ""
		if at := strings.Index(line, "commit:"); at >= 0 {
			candidate = strings.TrimSpace(line[at+len("commit:"):])
		} else if at := strings.Index(line, "commit "); at >= 0 {
			rest := line[at+len("commit "):]
			if colon := strings.Index(rest, ":"); colon >= 0 {
				candidate = strings.TrimSpace(rest[colon+1:])
			}
		}
		subject = candidate
		commitAt = i
		normalized, err := completedText("commit subject", subject, maxCommitRunes)
		commitOK = err == nil && legacyLineComplete(lines, i)
		if commitOK {
			subject = normalized
		}
	}
	return outcome, subject, outcomeOK && commitOK && outcomeAt < commitAt
}

func legacyLineComplete(lines []string, i int) bool {
	if strings.Contains(lines[i], "…[truncated]") {
		return false
	}
	if i+1 >= len(lines) {
		return true
	}
	next := strings.TrimSpace(lines[i+1])
	return next == "" || strings.HasPrefix(next, "- ") || strings.HasPrefix(next, "## ")
}

const (
	maxOutcomeRunes = 1000
	maxCommitRunes  = 240
)

func compactCompletedBody(body, outcome, commitSubject string) string {
	description := collectSectionContent(body, "description")
	criteria := collectSectionContent(body, "acceptance criteria")
	if description == "" {
		// Hand-written legacy tasks are not always canonical. Preserve all content
		// before operational Plan/Outcome/Work log sections rather than dropping it.
		description = strings.TrimSpace(stripOperationalSections(body))
	}
	var compact strings.Builder
	compact.WriteString("## Description\n\n")
	compact.WriteString(description)
	if criteria != "" {
		compact.WriteString("\n\n## Acceptance criteria\n\n")
		compact.WriteString(criteria)
	}
	compact.WriteString("\n\n## Outcome\n\n")
	compact.WriteString(outcome)
	compact.WriteString("\n\nCommit: ")
	compact.WriteString(commitSubject)
	compact.WriteByte('\n')
	return compact.String()
}

func collectSectionContent(body, wanted string) string {
	lines := strings.Split(body, "\n")
	var sections []string
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "## ") {
			i++
			continue
		}
		title := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "##"))
		start := i + 1
		i = start
		for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "## ") {
			i++
		}
		if strings.EqualFold(title, wanted) {
			if content := strings.TrimSpace(strings.Join(lines[start:i], "\n")); content != "" {
				sections = append(sections, content)
			}
		}
	}
	return strings.Join(sections, "\n\n")
}

func stripOperationalSections(body string) string {
	lines := strings.Split(body, "\n")
	var kept []string
	skip := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			title := strings.TrimSpace(strings.TrimPrefix(trimmed, "##"))
			skip = strings.EqualFold(title, "plan") || strings.EqualFold(title, "work log") || strings.EqualFold(title, "outcome")
		}
		if !skip {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func completedText(name, s string, limit int) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if strings.Contains(s, "…[truncated]") {
		return "", fmt.Errorf("%s is truncated", name)
	}
	if len([]rune(s)) > limit {
		return "", fmt.Errorf("%s exceeds %d characters", name, limit)
	}
	return s, nil
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

func (s *Store) nextID() (string, error) {
	if s.idSource != nil {
		return s.idSource()
	}
	// A Store outside a daemon-owned project has no parallel worktrees. Reserve
	// from frontmatter only; duplicate repair still loads full bodies exceptionally.
	tasks, err := s.listMetadataLocked()
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
	t, err := parseFrontmatter(path, m[1])
	if err != nil {
		return nil, err
	}
	t.Body = strings.TrimLeft(string(m[2]), "\n")
	return t, nil
}

func parseMetadataFile(path string) (*Task, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 1024*1024)
	if !scan.Scan() || scan.Text() != "---" {
		return nil, scan.Err()
	}
	var front strings.Builder
	for scan.Scan() {
		if scan.Text() == "---" {
			return parseFrontmatter(path, []byte(front.String()))
		}
		front.WriteString(scan.Text())
		front.WriteByte('\n')
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

func parseFrontmatter(path string, front []byte) (*Task, error) {
	var t Task
	if err := yaml.Unmarshal(front, &t); err != nil {
		return nil, fmt.Errorf("invalid frontmatter: %w", err)
	}
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
