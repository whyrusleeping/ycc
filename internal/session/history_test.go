package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
)

// writeSession writes a session dir with the given events as JSONL lines under
// ws/.ycc/sessions/<id>/events.jsonl.
func writeSession(t *testing.T, ws, id string, evs []event.Event) {
	t.Helper()
	dir := filepath.Join(ws, ".ycc", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	for _, ev := range evs {
		b, err := marshalEvent(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func marshalEvent(ev event.Event) ([]byte, error) {
	return json.Marshal(ev)
}

func ts(sec int) time.Time {
	return time.Date(2026, 1, 1, 0, 0, sec, 0, time.UTC)
}

func TestScanSessionHistoryOrdering(t *testing.T) {
	ws := t.TempDir()
	writeSession(t, ws, "s_old", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "work"}},
		{Seq: 2, TS: ts(2), Type: event.ModelTurn},
	})
	writeSession(t, ws, "s_new", []event.Event{
		{Seq: 1, TS: ts(10), Type: event.SessionStarted, Data: map[string]any{"mode": "chat"}},
		{Seq: 2, TS: ts(20), Type: event.ModelTurn},
	})

	m := NewManager(config.NewRegistry(nil), ws)
	got, err := m.ListSessionHistory("")
	if err != nil {
		t.Fatalf("ListSessionHistory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 summaries, got %d", len(got))
	}
	if got[0].ID != "s_new" || got[1].ID != "s_old" {
		t.Fatalf("want most-recent first [s_new s_old], got [%s %s]", got[0].ID, got[1].ID)
	}
}

func TestScanSessionHistoryTitleAndFocus(t *testing.T) {
	ws := t.TempDir()
	writeSession(t, ws, "s_a", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "work", "workspace": ws}},
		{Seq: 2, TS: ts(2), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "Implement the widget"}},
		{Seq: 3, TS: ts(3), Type: event.TaskFocus, Data: map[string]any{"task": "0007"}},
		{Seq: 4, TS: ts(4), Type: event.ModelTurn},
		{Seq: 5, TS: ts(5), Type: event.TaskFocus, Data: map[string]any{"task": "0008"}},
		{Seq: 6, TS: ts(6), Type: event.ToolCall},
		// duplicate focus should not repeat
		{Seq: 7, TS: ts(7), Type: event.TaskFocus, Data: map[string]any{"task": "0007"}},
	})

	sums, err := scanSessionHistory(ws)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(sums) != 1 {
		t.Fatalf("want 1 summary, got %d", len(sums))
	}
	s := sums[0]
	if s.Title != "Implement the widget" {
		t.Fatalf("title = %q", s.Title)
	}
	if len(s.FocusTasks) != 2 || s.FocusTasks[0] != "0007" || s.FocusTasks[1] != "0008" {
		t.Fatalf("focus tasks = %v", s.FocusTasks)
	}
	if s.Turns != 1 || s.ToolCalls != 1 {
		t.Fatalf("turns/toolcalls = %d/%d", s.Turns, s.ToolCalls)
	}
	if s.Mode != "work" {
		t.Fatalf("mode = %q", s.Mode)
	}
	if s.Workspace != ws {
		t.Fatalf("workspace = %q want %q", s.Workspace, ws)
	}
}

func TestTruncateTitle(t *testing.T) {
	if got := truncateTitle("  hello\nworld  "); got != "hello world" {
		t.Fatalf("collapse: %q", got)
	}
	if got := truncateTitle("   "); got != "" {
		t.Fatalf("empty: %q", got)
	}
	long := ""
	for i := 0; i < 100; i++ {
		long += "x"
	}
	got := truncateTitle(long)
	if r := []rune(got); len(r) != 81 || r[80] != '…' {
		t.Fatalf("truncate len = %d", len([]rune(got)))
	}
}

func TestScanSessionHistoryMalformedTolerated(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, ".ycc", "sessions", "s_bad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	good1, _ := marshalEvent(event.Event{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "work"}})
	good2, _ := marshalEvent(event.Event{Seq: 2, TS: ts(2), Type: event.ModelTurn})
	content := string(good1) + "\n" + "{not valid json" + "\n" + string(good2) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	sums, err := scanSessionHistory(ws)
	if err != nil {
		t.Fatalf("scan returned error: %v", err)
	}
	if len(sums) != 1 {
		t.Fatalf("want 1 summary, got %d", len(sums))
	}
	if sums[0].Turns != 1 {
		t.Fatalf("want 1 valid model_turn, got %d", sums[0].Turns)
	}
}

func TestScanSessionHistoryEmptySkipped(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, ".ycc", "sessions", "s_empty")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sums, err := scanSessionHistory(ws)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(sums) != 0 {
		t.Fatalf("want empty session skipped, got %d", len(sums))
	}
}

func TestListSessionHistoryOrphanedRunningIsStopped(t *testing.T) {
	ws := t.TempDir()
	writeSession(t, ws, "s_orphan", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "work"}},
		{Seq: 2, TS: ts(2), Type: event.ModelTurn},
		// No session_idle/error/stopped: simulate an abruptly terminated daemon.
	})

	m := NewManager(config.NewRegistry(nil), ws)
	got, err := m.ListSessionHistory("")
	if err != nil {
		t.Fatalf("ListSessionHistory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 summary, got %d", len(got))
	}
	if got[0].Live {
		t.Fatal("orphaned persisted session must not be live")
	}
	if got[0].Status != event.StatusStopped {
		t.Fatalf("orphaned persisted status = %q, want %q", got[0].Status, event.StatusStopped)
	}
}

func TestListSessionHistoryLiveOverridesDisk(t *testing.T) {
	ws := t.TempDir()
	absWS, _ := filepath.Abs(ws)
	writeSession(t, ws, "s_live", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "work"}},
		// The disk projection is running too; the live overlay must prevent orphan
		// normalization and preserve the manager's current running status.
		{Seq: 2, TS: ts(2), Type: event.ModelTurn},
	})

	m := NewManager(config.NewRegistry(nil), ws)
	// Inject a live session sharing the workspace with a different status/mode.
	m.sessions["s_live"] = &Session{
		ID:        "s_live",
		Workspace: absWS,
		Mode:      "chat",
		status:    event.StatusRunning,
	}

	got, err := m.ListSessionHistory("")
	if err != nil {
		t.Fatalf("ListSessionHistory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 summary, got %d", len(got))
	}
	s := got[0]
	if !s.Live {
		t.Fatalf("expected Live=true")
	}
	if s.Status != event.StatusRunning {
		t.Fatalf("live status should win, got %q", s.Status)
	}
	if s.Mode != "chat" {
		t.Fatalf("live mode should win, got %q", s.Mode)
	}
}

func TestListSessionHistoryLiveWithoutDisk(t *testing.T) {
	ws := t.TempDir()
	absWS, _ := filepath.Abs(ws)
	m := NewManager(config.NewRegistry(nil), ws)
	m.sessions["s_fresh"] = &Session{
		ID:        "s_fresh",
		Workspace: absWS,
		Mode:      "work",
		status:    event.StatusRunning,
	}
	got, err := m.ListSessionHistory("")
	if err != nil {
		t.Fatalf("ListSessionHistory: %v", err)
	}
	if len(got) != 1 || got[0].ID != "s_fresh" || !got[0].Live {
		t.Fatalf("want fresh live session included, got %+v", got)
	}
}

func TestListSessionHistoryUnknownProject(t *testing.T) {
	m := NewManager(config.NewRegistry(nil), t.TempDir())
	if _, err := m.ListSessionHistory("nope"); err == nil {
		t.Fatal("want error for unknown project")
	}
}

// A live session blocked on a pending ask_user question has Waiting=true in its
// history row; without a pending question it stays false (task 0107). Only live
// sessions carry the flag.
func TestListSessionHistoryWaiting(t *testing.T) {
	ws := t.TempDir()
	m := NewManager(config.NewRegistry(nil), ws)
	s := newStopSession(t)
	s.ID = "s_wait"
	absWS, _ := filepath.Abs(ws)
	s.Workspace = absWS
	m.mu.Lock()
	m.sessions["s_wait"] = s
	m.mu.Unlock()

	// No pending question: Waiting=false.
	got, err := m.ListSessionHistory("")
	if err != nil {
		t.Fatalf("ListSessionHistory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 summary, got %d", len(got))
	}
	if got[0].Waiting {
		t.Fatalf("want Waiting=false with no pending question")
	}

	// Simulate a blocked ask_user (as reaper_test does): a pending waiting channel.
	s.inter.mu.Lock()
	s.inter.waiting = make(chan string, 1)
	s.inter.mu.Unlock()

	got, err = m.ListSessionHistory("")
	if err != nil {
		t.Fatalf("ListSessionHistory: %v", err)
	}
	if len(got) != 1 || !got[0].Waiting {
		t.Fatalf("want Waiting=true with a pending question, got %+v", got)
	}
}
