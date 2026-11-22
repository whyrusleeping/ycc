package session

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// Loop sessions leave uncommitted work of in_review tasks alone (its review and
// commit gates belong to the owner) and route around those files, so a loop
// started on such a tree fragments tasks without committing anything. The
// preflight names that work so clients can warn before StartWorkLoop and the
// loop digest keeps it visible.

// preflightPathsShown bounds the paths named in each task's Reason.
const preflightPathsShown = 5

// WorkLoopPreflight lists in_review tasks whose work is uncommitted in the
// workspace. Each task's Reason names its uncommitted paths.
type WorkLoopPreflight struct {
	InReview []WorkLoopDigestTask
	Paths    int // distinct uncommitted paths attributed to InReview
}

// WorkLoopPreflight reports what a loop started now in project would have to
// route around.
func (m *Manager) WorkLoopPreflight(project string) (WorkLoopPreflight, error) {
	absWS, _, err := m.resolveWorkspace(project)
	if err != nil {
		return WorkLoopPreflight{}, err
	}
	tasks, err := m.backlogStore(absWS).ListMetadata()
	if err != nil {
		return WorkLoopPreflight{}, err
	}
	return m.workLoopPreflight(absWS, tasks), nil
}

// workLoopPreflight attributes the workspace's uncommitted paths to in_review
// tasks: a task's own backlog file, and each claimed path to the task its
// claiming session focused most recently before first writing it. A claim
// without that evidence (no recorded claim time, from a session that focused
// several tasks) is left unattributed rather than charged to every task. It is
// best effort and reports nothing when git is unavailable.
func (m *Manager) workLoopPreflight(absWS string, tasks []*docs.Task) WorkLoopPreflight {
	var review []*docs.Task
	for _, t := range tasks {
		if t.Status == docs.StatusInReview {
			review = append(review, t)
		}
	}
	if len(review) == 0 {
		return WorkLoopPreflight{}
	}
	repo, err := git.OpenExisting(absWS)
	if err != nil {
		return WorkLoopPreflight{}
	}
	dirtyList, err := repo.DirtyPaths()
	if err != nil || len(dirtyList) == 0 {
		return WorkLoopPreflight{}
	}
	top, err := workspacelease.Canonical(absWS)
	if err != nil {
		return WorkLoopPreflight{}
	}
	dirty := make(map[string]bool, len(dirtyList))
	for _, p := range dirtyList {
		dirty[p] = true
	}

	byTask := map[string]map[string]bool{}
	add := func(id, path string) {
		if byTask[id] == nil {
			byTask[id] = map[string]bool{}
		}
		byTask[id][path] = true
	}
	inReview := make(map[string]bool, len(review))
	for _, t := range review {
		inReview[t.ID] = true
		if rel, ok := repoRelative(top, t.Path); ok && dirty[rel] {
			add(t.ID, rel)
		}
	}
	primary := m.primaryTreeFor(absWS)
	timelines := map[string][]focusMark{} // claiming session -> its task focus changes
	for path, claims := range m.ownership.ClaimDetails(absWS) {
		if !dirty[path] {
			continue
		}
		for _, c := range claims {
			marks, ok := timelines[c.Scope]
			if !ok {
				marks = m.focusTimeline(primary, c.Scope)
				timelines[c.Scope] = marks
			}
			if id := taskFocusedAt(marks, c.Since); inReview[id] {
				add(id, path)
			}
		}
	}

	var out WorkLoopPreflight
	all := map[string]bool{}
	for _, t := range review {
		paths := byTask[t.ID]
		if len(paths) == 0 {
			continue
		}
		list := make([]string, 0, len(paths))
		for p := range paths {
			list = append(list, p)
			all[p] = true
		}
		sort.Strings(list)
		out.InReview = append(out.InReview, WorkLoopDigestTask{
			ID: t.ID, Title: t.Title, Status: string(t.Status),
			Reason: uncommittedReason(list),
		})
	}
	sort.Slice(out.InReview, func(i, j int) bool { return out.InReview[i].ID < out.InReview[j].ID })
	out.Paths = len(all)
	return out
}

// focusMark is a session's task focus change.
type focusMark struct {
	task string
	at   time.Time
}

// focusTimeline returns the task focus changes of session id of the project at
// primary, live or ended, oldest first.
func (m *Manager) focusTimeline(primary, id string) []focusMark {
	var evs []event.Event
	if s, ok := m.Get(id); ok {
		if canonicalPath(m.primaryTreeFor(s.Workspace)) != canonicalPath(primary) {
			return nil
		}
		evs = s.Log().Snapshot()
	} else {
		if id == "" || filepath.Base(id) != id {
			return nil
		}
		absPrimary, err := filepath.Abs(primary)
		if err != nil {
			return nil
		}
		_, logPath, err := m.locateSessionLog(absPrimary, id)
		if err != nil {
			return nil
		}
		evs, _ = readSummaryEventsTolerantResult(logPath)
	}
	var marks []focusMark
	for _, ev := range evs {
		if ev.Type != event.TaskFocus {
			continue
		}
		if task, _ := ev.Data["task"].(string); task != "" {
			marks = append(marks, focusMark{task: task, at: ev.TS})
		}
	}
	return marks
}

// taskFocusedAt returns the task a session was focused on when it first
// claimed a path at since, or "" without that evidence. A zero since (claims
// persisted before claim times were recorded) is attributable only when the
// session focused one task. Claims reloaded from disk keep whole seconds, so a
// focus change to another task within that second leaves the claim
// unattributed.
func taskFocusedAt(marks []focusMark, since time.Time) string {
	if since.IsZero() {
		task := ""
		for _, mk := range marks {
			if task != "" && mk.task != task {
				return ""
			}
			task = mk.task
		}
		return task
	}
	task := focusAt(marks, since)
	if since.Nanosecond() == 0 {
		end := since.Add(time.Second)
		for _, mk := range marks {
			if mk.at.After(since) && mk.at.Before(end) && mk.task != task {
				return ""
			}
		}
	}
	return task
}

// focusAt returns the task focused at t.
func focusAt(marks []focusMark, t time.Time) string {
	task := ""
	for _, mk := range marks {
		if mk.at.After(t) {
			break
		}
		task = mk.task
	}
	return task
}

// uncommittedReason names a task's uncommitted paths, bounded.
func uncommittedReason(paths []string) string {
	shown := paths
	if len(shown) > preflightPathsShown {
		shown = shown[:preflightPathsShown]
	}
	reason := "uncommitted: " + strings.Join(shown, ", ")
	if more := len(paths) - len(shown); more > 0 {
		reason += fmt.Sprintf(" (+%d more)", more)
	}
	return reason
}

// repoRelative maps path to its slash-separated form relative to the canonical
// worktree top.
func repoRelative(top, path string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(top, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
