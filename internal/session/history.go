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
// disk — derived by reducing its event log. It is the row type
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
	ModelUsage   []ModelUsage
	TotalTokens  int64
	// ContextTokens is the coarse prompt-size estimate (context_tokens_est)
	// from the newest coordinator model_turn — how full the session's active
	// context is, as opposed to cumulative spend. Subagent turns are ignored
	// because they run isolated histories. Zero means the log predates the
	// telemetry (or no coordinator turn completed yet).
	ContextTokens int64
	Turns         int
	ToolCalls     int
	Live          bool
	// Waiting is true when a live session is blocked on an unanswered ask_user
	// question. Only ever set on live rows — a persisted-only session holds no
	// in-memory pending question.
	Waiting bool
}

// ModelUsage is the total recorded token usage for one logical model name in a
// session. Rows are ordered by token count descending, then model name ascending,
// so every client can present the same compact summary without re-sorting.
type ModelUsage struct {
	Model  string
	Tokens int64
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
		if len(evs) != 0 {
			out = append(out, reduceSessionSummary(workspace, path, evs))
		}
	}
	return out, nil
}

func reduceSessionSummary(workspace, path string, evs []event.Event) SessionSummary {
	id := filepath.Base(filepath.Dir(path))
	proj := event.Reduce(evs)
	models, totalTokens := sessionModelUsage(evs)
	ws := proj.Workspace
	if ws == "" {
		ws = workspace
	}
	return SessionSummary{
		ID:            id,
		Mode:          proj.Mode,
		Status:        proj.Status,
		Workspace:     ws,
		Title:         deriveTitle(evs),
		StartedAt:     evs[0].TS,
		LastActivity:  evs[len(evs)-1].TS,
		FocusTasks:    focusTasks(evs),
		ModelUsage:    models,
		TotalTokens:   totalTokens,
		ContextTokens: sessionContextTokens(evs),
		Turns:         proj.Turns,
		ToolCalls:     proj.ToolCalls,
	}
}

// readEventsTolerant reads a session log line by line, skipping (with a logged
// warning) a file it can't open or any line that fails to parse, so a partial
// or corrupt log still yields its good events instead of failing the whole scan.
func readEventsTolerant(path string) []event.Event {
	evs, _ := readEventsTolerantResult(path)
	return evs
}

// readEventsTolerantResult additionally reports whether the reduction is safe
// to cache. Open, scanner, and malformed-line failures still return any usable
// events for tolerant display, but their partial result is not cached.
func readEventsTolerantResult(path string) ([]event.Event, bool) {
	f, err := os.Open(path)
	if err != nil {
		log.Printf("ycc: session history: skipping %s: %v", path, err)
		return nil, false
	}
	defer f.Close()

	var out []event.Event
	cacheable := true
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
			cacheable = false
			continue
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		log.Printf("ycc: session history: read error in %s: %v", path, err)
		return out, false
	}
	return out, cacheable
}

// deriveTitle uses the first user_input as the session title unless it is one
// of the canned mode kickoffs (or is absent). In that case, the first task focus
// is more useful: its id and optional title identify the work the session chose.
// Custom user prompts remain authoritative even when the session later focuses
// a task.
func deriveTitle(evs []event.Event) string {
	var opening string
	for _, ev := range evs {
		if ev.Type != event.UserInput {
			continue
		}
		text, _ := ev.Data["text"].(string)
		if text = strings.TrimSpace(text); text != "" {
			opening = text
			break
		}
	}

	if opening == "" || isDefaultPrompt(opening) {
		for _, ev := range evs {
			if ev.Type != event.TaskFocus {
				continue
			}
			task, _ := ev.Data["task"].(string)
			if task = strings.TrimSpace(task); task == "" {
				continue
			}
			title, _ := ev.Data["title"].(string)
			if title = strings.TrimSpace(title); title != "" {
				return truncateTitle(task + " — " + title)
			}
			return truncateTitle(task)
		}
	}

	return truncateTitle(opening)
}

func isDefaultPrompt(prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	for _, mode := range []string{"work", "chat", "pm"} {
		if prompt == strings.TrimSpace(defaultPrompt(mode)) {
			return true
		}
	}
	return false
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

// sessionModelUsage totals every recorded model_turn, regardless of actor, while
// grouping named turns by logical model_name. Unnamed usage contributes to the
// session total but cannot produce a useful model row. Missing, zero, negative,
// or malformed usage is omitted so old and partial logs do not fabricate data.
func sessionModelUsage(evs []event.Event) ([]ModelUsage, int64) {
	byModel := make(map[string]int64)
	var total int64
	for _, ev := range evs {
		if ev.Type != event.ModelTurn {
			continue
		}
		tokens := modelTurnTokens(ev.Data["usage"])
		if tokens <= 0 {
			continue
		}
		total += tokens
		model, _ := ev.Data["model_name"].(string)
		if model = strings.TrimSpace(model); model != "" {
			byModel[model] += tokens
		}
	}
	models := make([]ModelUsage, 0, len(byModel))
	for model, tokens := range byModel {
		models = append(models, ModelUsage{Model: model, Tokens: tokens})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Tokens != models[j].Tokens {
			return models[i].Tokens > models[j].Tokens
		}
		return models[i].Model < models[j].Model
	})
	return models, total
}

// sessionContextTokens returns the context_tokens_est recorded on the newest
// coordinator model_turn (actor empty or "coordinator"), or 0 when no such
// telemetry exists. Subagent (implementer/reviewer/agent) turns are skipped:
// they run isolated histories, so their context size says nothing about the
// session's own conversation. Turns without the field (older logs) are also
// skipped rather than clearing a previously known value.
func sessionContextTokens(evs []event.Event) int64 {
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		if ev.Type != event.ModelTurn {
			continue
		}
		if ev.Actor != "" && ev.Actor != "coordinator" {
			continue
		}
		if tokens, ok := integerField(ev.Data["context_tokens_est"]); ok && tokens >= 0 {
			return tokens
		}
	}
	return 0
}

// integerField coerces the numeric encodings a reduced event field can arrive
// in (in-memory int, JSON float64/Number) into an int64.
func integerField(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case json.Number:
		value, err := n.Int64()
		return value, err == nil
	}
	return 0, false
}

func modelTurnTokens(v any) int64 {
	switch usage := v.(type) {
	case event.Usage:
		return int64(usage.Total)
	case *event.Usage:
		if usage != nil {
			return int64(usage.Total)
		}
	case map[string]any:
		switch total := usage["total"].(type) {
		case float64:
			return int64(total)
		case int:
			return int64(total)
		case int64:
			return total
		case json.Number:
			value, _ := total.Int64()
			return value
		}
	}
	return 0
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

type sessionSummaryCacheEntry struct {
	info    os.FileInfo
	summary SessionSummary
}

func cloneSessionSummary(s SessionSummary) SessionSummary {
	s.FocusTasks = append([]string(nil), s.FocusTasks...)
	s.ModelUsage = append([]ModelUsage(nil), s.ModelUsage...)
	return s
}

func sameSessionLogVersion(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Size() == b.Size() &&
		a.ModTime().Equal(b.ModTime()) && os.SameFile(a, b)
}

// scanSessionHistoryCached reduces only logs whose file identity, size, or
// modification time changed. A log that changes while it is being read still
// contributes its best-effort summary to this call but is not cached.
func (m *Manager) scanSessionHistoryCached(workspace string) ([]SessionSummary, error) {
	glob := filepath.Join(workspace, ".ycc", "sessions", "*", "events.jsonl")
	paths, err := filepath.Glob(glob)
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	seen := make(map[string]bool, len(paths))

	var out []SessionSummary
	for _, path := range paths {
		seen[path] = true
		before, err := os.Stat(path)
		if err != nil {
			log.Printf("ycc: session history: skipping %s: %v", path, err)
			m.historyCacheMu.Lock()
			delete(m.historyCache, path)
			m.historyCacheMu.Unlock()
			continue
		}

		m.historyCacheMu.Lock()
		cached, ok := m.historyCache[path]
		m.historyCacheMu.Unlock()
		if ok && sameSessionLogVersion(cached.info, before) {
			out = append(out, cloneSessionSummary(cached.summary))
			continue
		}

		evs, complete := readEventsTolerantResult(path)
		after, statErr := os.Stat(path)
		stable := complete && statErr == nil && sameSessionLogVersion(before, after)
		if len(evs) == 0 {
			m.historyCacheMu.Lock()
			delete(m.historyCache, path)
			m.historyCacheMu.Unlock()
			continue
		}

		summary := reduceSessionSummary(workspace, path, evs)
		out = append(out, summary)
		m.historyCacheMu.Lock()
		if stable {
			if m.historyCache == nil {
				m.historyCache = make(map[string]sessionSummaryCacheEntry)
			}
			m.historyCache[path] = sessionSummaryCacheEntry{
				info:    after,
				summary: cloneSessionSummary(summary),
			}
		} else {
			delete(m.historyCache, path)
		}
		m.historyCacheMu.Unlock()
	}

	// A glob is the source of truth for removals. Keep entries for other
	// workspaces while dropping logs that disappeared from this workspace.
	sessionsDir := filepath.Join(workspace, ".ycc", "sessions") + string(os.PathSeparator)
	m.historyCacheMu.Lock()
	for path := range m.historyCache {
		if strings.HasPrefix(path, sessionsDir) && !seen[path] {
			delete(m.historyCache, path)
		}
	}
	m.historyCacheMu.Unlock()
	return out, nil
}

// ListSessionHistory enumerates all sessions for a project — both live (from the
// manager map) and persisted on-disk logs — and returns their summaries sorted
// most-recent first. The project may be omitted only when the
// daemon has one project; unknown or ambiguous selection returns
// ErrUnknownProject. Live sessions override their on-disk snapshot (live
// status/mode win, Live=true), and a live session with no disk snapshot yet is
// still included.
func (m *Manager) ListSessionHistory(project string) ([]SessionSummary, error) {
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, err
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	summaries, err := m.scanSessionHistoryCached(absWS)
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
