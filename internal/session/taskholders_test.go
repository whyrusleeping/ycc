package session

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
)

// Backlog views learn who holds each task from TaskHolders: the work loop's
// heldTasks rule (this workspace, actively working), named, and flagged when
// the work loop started the holder. Session coordination is off here.
func TestTaskHoldersFollowHeldTasksRule(t *testing.T) {
	ws := t.TempDir()
	other := t.TempDir()
	m := NewManager(config.NewRegistry(nil), ws)
	t.Cleanup(func() {
		m.sessions = make(map[string]*Session) // minimally constructed rows have no run owner
		m.ReclaimAll()
	})
	add := func(id, workspace, origin, prompt, task string, status event.Status) {
		t.Helper()
		lg, err := event.OpenLog(filepath.Join(workspace, ".ycc", "sessions", id, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { lg.Close() })
		lg.Record("coordinator", event.SessionStarted, map[string]any{"origin": origin, "mode": "work"})
		lg.Record("user", event.UserInput, map[string]any{"text": prompt, "opening": true})
		lg.Record("coordinator", event.TaskFocus, map[string]any{"task": task})
		m.sessions[id] = &Session{ID: id, Workspace: workspace, Mode: "work", origin: origin, log: lg,
			status: status, inter: &interaction{}}
	}
	add("s_loop", ws, OriginWorkLoop, "Work on the backlog", "1", event.StatusRunning)
	add("s_user", ws, OriginUser, "Fix the login flow", "0002", event.StatusRunning)
	add("s_idle", ws, OriginUser, "Earlier work", "0003", event.StatusIdle)
	add("s_other", other, OriginUser, "Another worktree", "0004", event.StatusRunning)

	got, err := m.TaskHolders("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("holders = %+v, want 0001 and 0002 only", got)
	}
	if h := got["0001"]; h.SessionID != "s_loop" || !h.Loop {
		t.Fatalf("0001 holder = %+v, want the loop session", h)
	}
	if h := got["0002"]; h.SessionID != "s_user" || h.Loop || !strings.Contains(h.Title, "Fix the login flow") {
		t.Fatalf("0002 holder = %+v, want the titled user session", h)
	}
}
