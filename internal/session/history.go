package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
)

// SessionSummary is a read-only digest of one session — live or persisted on
// disk — derived by reducing its event log (spec §18.6). It is the row type
// returned by ListSessionHistory so the session browser and cost views can
// enumerate every session for a project, not just the live ones.
type SessionSummary struct {
	ID           string
	Mode         string
	Status       event.Status
	Workspace    string
	Title        string
	StartedAt    time.Time
	LastActivity time.Time
	FocusTasks   []string
	Turns        int
	ToolCalls    int
	Live         bool
	// Waiting is true when a live session is blocked on an unanswered ask_user
	// question. Only ever set on live rows — a persisted-only session holds no
	// in-memory pending question.
	Waiting bool
}

// scanSessionHistory scans a workspace's persisted session logs at
// <workspace>/.ycc/sessions/*/events.jsonl, reduces each into a SessionSummary,
// and returns the rows (unsorted). It is deliberately tolerant of partial or
// corrupt logs: an unreadable directory is skipped with a logged warning, a
// malformed JSONL line is skipped (keeping the surrounding good events), and a
// directory yielding no usable events is dropped — none of these crash the scan.
// Only a real glob error surfaces as a returned error.
func scanSessionHistory(workspace string) ([]SessionSummary, error) {
	glob := filepath.Join(workspace, ".ycc", "sessions", "*", "events.jsonl")
	paths, err := filepath.Glob(glob)
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	var out []SessionSummary
	for _, path := range paths {
		evs := readEventsTolerant(path)
		if len(evs) == 0 {
			continue
		}
		id := filepath.Base(filepath.Dir(path))
		proj := event.Reduce(evs)
		ws := proj.Workspace
		if ws == "" {
			ws = workspace
		}
		out = append(out, SessionSummary{
			ID:           id,
			Mode:         proj.Mode,
			Status:       proj.Status,
			Workspace:    ws,
			Title:        firstUserPrompt(evs),
			StartedAt:    evs[0].TS,
			LastActivity: evs[len(evs)-1].TS,
			FocusTasks:   focusTasks(evs),
			Turns:        proj.Turns,
			ToolCalls:    proj.ToolCalls,
		})
	}
	return out, nil
}

// readEventsTolerant reads a session log line by line, skipping (with a logged
// warning) a file it can't open or any line that fails to parse, so a partial
// or corrupt log still yields its good events instead of failing the whole scan.
func readEventsTolerant(path string) []event.Event {
	f, err := os.Open(path)
	if err != nil {
		log.Printf("ycc: session history: skipping %s: %v", path, err)
		return nil
	}
	defer f.Close()

	var out []event.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			log.Printf("ycc: session history: skipping corrupt line in %s: %v", path, err)
			continue
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		log.Printf("ycc: session history: read error in %s: %v", path, err)
	}
	return out
}

// firstUserPrompt derives a short, single-line title from the first non-empty
// user_input text (the opening prompt / kickoff), truncated to ~80 runes. Empty
// if the session has no user input yet.
func firstUserPrompt(evs []event.Event) string {
	for _, ev := range evs {
		if ev.Type != event.UserInput {
			continue
		}
		text, _ := ev.Data["text"].(string)
		if title := truncateTitle(text); title != "" {
			return title
		}
	}
	return ""
}

// focusTasks collects the distinct, non-empty task ids from task_focus events,
// preserving first-seen order, so a summary lists every task the session worked.
func focusTasks(evs []event.Event) []string {
	var tasks []string
	seen := map[string]bool{}
	for _, ev := range evs {
		if ev.Type != event.TaskFocus {
			continue
		}
		task, _ := ev.Data["task"].(string)
		if task == "" || seen[task] {
			continue
		}
		seen[task] = true
		tasks = append(tasks, task)
	}
	return tasks
}

// truncateTitle collapses whitespace/newlines to a single line and truncates to
// ~80 runes with an ellipsis, returning "" for empty/whitespace-only input.
func truncateTitle(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if s == "" {
		return ""
	}
	const max = 80
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// ListSessionHistory enumerates all sessions for a project — both live (from the
// manager map) and persisted on-disk logs — and returns their summaries sorted
// most-recent first (spec §18.6). An empty project means the daemon default
// workspace; an unknown project name returns ErrUnknownProject. Live sessions
// override their on-disk snapshot (live status/mode win, Live=true), and a live
// session with no disk snapshot yet is still included.
func (m *Manager) ListSessionHistory(project string) ([]SessionSummary, error) {
	ws := m.defaultWorkspace
	if project != "" {
		p, ok := m.projects.Resolve(project)
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrUnknownProject, project)
		}
		ws = p
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	summaries, err := scanSessionHistory(absWS)
	if err != nil {
		return nil, err
	}

	// Index by id so live sessions can override their on-disk snapshot.
	byID := make(map[string]int, len(summaries))
	for i, s := range summaries {
		byID[s.ID] = i
	}

	// Snapshot the live sessions for this workspace under lock.
	m.mu.Lock()
	type liveInfo struct {
		id      string
		mode    string
		status  event.Status
		waiting bool
	}
	var live []liveInfo
	for _, s := range m.sessions {
		if s.Workspace == absWS {
			live = append(live, liveInfo{id: s.ID, mode: s.Mode, status: s.Status(), waiting: s.PendingQuestion()})
		}
	}
	m.mu.Unlock()

	now := time.Now()
	for _, li := range live {
		if idx, ok := byID[li.id]; ok {
			summaries[idx].Status = li.status
			summaries[idx].Mode = li.mode
			summaries[idx].Live = true
			summaries[idx].Waiting = li.waiting
			continue
		}
		// Live session with no on-disk snapshot yet (log just opened): include it
		// with best-effort timestamps so it is still enumerable.
		summaries = append(summaries, SessionSummary{
			ID:           li.id,
			Mode:         li.mode,
			Status:       li.status,
			Workspace:    absWS,
			StartedAt:    now,
			LastActivity: now,
			Live:         true,
			Waiting:      li.waiting,
		})
	}

	// A persisted log can end without a terminal event when its owning daemon is
	// killed or crashes. Reduction correctly leaves such a log at running, but if
	// it has no matching in-memory session it cannot actually be running now.
	// Normalize that orphaned display state while preserving running for rows the
	// live overlay above confirmed are active.
	for i := range summaries {
		if !summaries[i].Live && summaries[i].Status == event.StatusRunning {
			summaries[i].Status = event.StatusStopped
		}
	}

	sort.SliceStable(summaries, func(i, j int) bool {
		a, b := summaries[i], summaries[j]
		if !a.LastActivity.Equal(b.LastActivity) {
			return a.LastActivity.After(b.LastActivity)
		}
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.After(b.StartedAt)
		}
		return a.ID < b.ID
	})
	return summaries, nil
}
