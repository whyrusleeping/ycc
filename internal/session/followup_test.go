package session

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

func TestSessionFollowUpPersistenceAndHistory(t *testing.T) {
	ws := t.TempDir()
	writeSession(t, ws, "older", []event.Event{{Seq: 1, TS: ts(1), Type: event.SessionStarted}})
	writeSession(t, ws, "newer", []event.Event{{Seq: 1, TS: ts(2), Type: event.SessionStarted}})
	m := NewManager(config.NewRegistry(nil), ws)
	defer m.ReclaimAll()
	// Warm the summary cache before the metadata changes.
	before, err := m.ListSessionHistory("")
	if err != nil || len(before) != 2 || before[1].FollowUp {
		t.Fatalf("initial history: %+v %v", before, err)
	}
	at, err := m.SetSessionFollowUp("", "older", true)
	if err != nil || at.IsZero() {
		t.Fatalf("set: %v %v", at, err)
	}
	again, err := m.SetSessionFollowUp("", "older", true)
	if err != nil || !at.Equal(again) {
		t.Fatalf("idempotent set: %v %v", again, err)
	}
	// Simulate new input; it must not clear the user's bookmark.
	writeSession(t, ws, "older", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted},
		{Seq: 2, TS: ts(1), Type: event.UserInput, Data: map[string]any{"text": "another message"}},
	})
	rows, err := m.ListSessionHistory("")
	if err != nil || !rows[1].FollowUp || !rows[1].FollowUpAt.Equal(at) || before[1].FollowUp {
		t.Fatalf("history overlay: %+v %v", rows, err)
	}
	// A live row outside the first page appears in pinned with the same flag.
	m.mu.Lock()
	m.sessions["older"] = &Session{ID: "older", Workspace: ws, Mode: "chat", status: event.StatusIdle}
	m.mu.Unlock()
	page, pinned, next, err := m.ListSessionHistoryPage("", 1, "")
	m.mu.Lock()
	delete(m.sessions, "older")
	m.mu.Unlock()
	if err != nil || len(page) != 1 || len(pinned) != 1 || !pinned[0].FollowUp || !pinned[0].FollowUpAt.Equal(at) {
		t.Fatalf("pinned overlay: %+v %+v %v", page, pinned, err)
	}
	page, _, _, err = m.ListSessionHistoryPage("", 1, next)
	if err != nil || len(page) != 1 || !page[0].FollowUp || !page[0].FollowUpAt.Equal(at) {
		t.Fatalf("page overlay: %+v %v", page, err)
	}
	m2 := NewManager(config.NewRegistry(nil), ws)
	defer m2.ReclaimAll()
	rows, err = m2.ListSessionHistory("")
	if err != nil || !rows[1].FollowUp || !rows[1].FollowUpAt.Equal(at) {
		t.Fatalf("restart: %+v %v", rows, err)
	}
	for range 2 {
		cleared, err := m2.SetSessionFollowUp("", "older", false)
		if err != nil || !cleared.IsZero() {
			t.Fatalf("clear: %v %v", cleared, err)
		}
	}
	// Even an already-running Manager reloads metadata, not a stale cache.
	rows, err = m.ListSessionHistory("")
	if err != nil || rows[1].FollowUp || !rows[1].FollowUpAt.IsZero() {
		t.Fatalf("cleared history: %+v %v", rows, err)
	}
	m3 := NewManager(config.NewRegistry(nil), ws)
	defer m3.ReclaimAll()
	rows, err = m3.ListSessionHistory("")
	if err != nil || rows[1].FollowUp || !rows[1].FollowUpAt.IsZero() {
		t.Fatalf("clear persisted: %+v %v", rows, err)
	}
}

func TestSessionFollowUpValidationAndCorruption(t *testing.T) {
	ws := t.TempDir()
	writeSession(t, ws, "session", []event.Event{{Seq: 1, TS: ts(1), Type: event.SessionStarted}})
	m := NewManager(config.NewRegistry(nil), ws)
	defer m.ReclaimAll()
	for _, id := range []string{"", ".", "..", "../session", "session/child", "/session", "unknown"} {
		for _, flagged := range []bool{false, true} {
			if _, err := m.SetSessionFollowUp("", id, flagged); !errors.Is(err, ErrUnknownSession) {
				t.Fatalf("id %q flag %v: %v", id, flagged, err)
			}
		}
	}
	if _, err := m.SetSessionFollowUp("unknown", "session", true); !errors.Is(err, ErrUnknownProject) {
		t.Fatalf("unknown project: %v", err)
	}
	other, err := m.AddProject(t.TempDir(), "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetSessionFollowUp(other.Name, "session", true); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("wrong project: %v", err)
	}
	project := m.Projects()[0].Name
	for _, p := range m.Projects() {
		if p.Path == ws {
			project = p.Name
		}
	}
	path := filepath.Join(ws, ".ycc", "session-follow-ups.json")
	for _, corrupt := range []string{"{", "null", "{}", `{"sessions":{"session":{}}}`} {
		if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := m.SetSessionFollowUp(project, "session", true); err == nil {
			t.Fatalf("set accepted corrupt file %q", corrupt)
		}
		if rows, err := m.ListSessionHistory(project); err != nil || len(rows) != 1 || rows[0].FollowUp {
			t.Fatalf("history with corrupt file %q: rows=%v err=%v", corrupt, rows, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != corrupt {
			t.Fatalf("corrupt file overwritten: %q %v", data, err)
		}
	}
}

func TestSessionFollowUpConcurrentSets(t *testing.T) {
	ws := t.TempDir()
	m := NewManager(config.NewRegistry(nil), ws)
	defer m.ReclaimAll()
	ids := []string{"one", "two", "three", "four", "five", "six", "seven", "eight"}
	for _, id := range ids {
		writeSession(t, ws, id, []event.Event{{Seq: 1, TS: ts(1), Type: event.SessionStarted}})
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			if _, err := m.SetSessionFollowUp("", id, true); err != nil {
				t.Errorf("set %s: %v", id, err)
			}
		})
	}
	wg.Wait()
	rows, err := m.ListSessionHistory("")
	if err != nil || len(rows) != len(ids) {
		t.Fatalf("history: %+v %v", rows, err)
	}
	for _, row := range rows {
		if !row.FollowUp {
			t.Errorf("concurrent set lost bookmark for %s", row.ID)
		}
	}
}

func TestSessionFollowUpWorktreeStorage(t *testing.T) {
	root := t.TempDir()
	m := NewManager(config.NewRegistry(nil), root)
	defer m.ReclaimAll()
	m.worktreesRoot = t.TempDir()
	wt := filepath.Join(m.worktreesRoot, "child")
	writeSession(t, wt, "child-session", []event.Event{{Seq: 1, TS: ts(1), Type: event.SessionStarted}})
	name := m.Projects()[0].Name
	if err := m.workstreams.Add(workstream.Workstream{ID: "ws_child", Project: name, WorktreePath: wt, SessionID: "child-session", Status: workstream.StatusReady}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.sessions["child-session"] = &Session{ID: "child-session", Workspace: wt}
	m.mu.Unlock()
	_, err := m.SetSessionFollowUp(name, "child-session", true)
	m.mu.Lock()
	delete(m.sessions, "child-session")
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// The same id remains valid once no longer live, while its log is in the worktree.
	if _, err := m.SetSessionFollowUp(name, "child-session", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".ycc", "session-follow-ups.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bookmark written to worktree: %v", err)
	}
	// Merge/discard preserves the log in the root and deletes the worktree.
	writeSession(t, root, "child-session", []event.Event{{Seq: 1, TS: ts(1), Type: event.SessionStarted}})
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	m2 := NewManager(config.NewRegistry(nil), root)
	defer m2.ReclaimAll()
	rows, err := m2.ListSessionHistory("")
	if err != nil || len(rows) != 1 || !rows[0].FollowUp {
		t.Fatalf("bookmark lost with worktree: %+v %v", rows, err)
	}
}
