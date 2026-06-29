package session

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// scriptTurner returns one assistant text turn per Turn call (no tool calls), so
// the loop yields immediately. Used to exercise resume without a network call.
type scriptTurner struct {
	text  string
	calls int
}

func (s *scriptTurner) Turn(gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	s.calls++
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{
		Message: gollama.Message{Role: "assistant", Content: s.text},
	}}}, nil
}

// (a) Reopen on an already-live id is a no-op returning the same *Session.
func TestReopenAlreadyLive(t *testing.T) {
	m := NewManager(config.NewRegistry(nil), t.TempDir())
	s := newStopSession(t)
	s.ID = "s_live"
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	got, err := m.Reopen("", "s_live")
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if got != s {
		t.Fatalf("Reopen returned a different *Session for a live id")
	}
}

// Reopen of a session that has no persisted log is ErrUnknownSession.
func TestReopenUnknown(t *testing.T) {
	m := NewManager(testRegistry(), t.TempDir())
	if _, err := m.Reopen("", "s_missing"); err == nil {
		t.Fatal("want error for unknown session")
	}
}

// A hard-stopped session (log ends in session_stopped) cannot be reopened:
// Reopen returns ErrSessionStopped and registers no live session.
func TestReopenStopped(t *testing.T) {
	ws := t.TempDir()
	id := "s_stopped"
	writeSession(t, ws, id, []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "work"}},
		{Seq: 2, TS: ts(2), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "go"}},
		{Seq: 3, TS: ts(3), Type: event.SessionStopped},
	})

	m := NewManager(testRegistry(), ws)
	_, err := m.Reopen("", id)
	if !errors.Is(err, ErrSessionStopped) {
		t.Fatalf("Reopen err = %v, want ErrSessionStopped", err)
	}
	if _, ok := m.Get(id); ok {
		t.Fatal("stopped session should not be registered as live")
	}
}

// (b) Reopen from an on-disk log registers a live session, restores its mode,
// and reconstructs the loop history losslessly. No input is sent (no turn runs).
func TestReopenFromDisk(t *testing.T) {
	ws := t.TempDir()
	absWS, _ := filepath.Abs(ws)
	id := "s_disk"
	events := []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "work", "workspace": absWS}},
		{Seq: 2, TS: ts(2), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "do the thing"}},
		{Seq: 3, TS: ts(3), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "on it"}},
		{Seq: 4, TS: ts(4), Type: event.SessionIdle, Data: map[string]any{"report": "on it"}},
	}
	writeSession(t, ws, id, events)

	m := NewManager(testRegistry(), ws)
	sess, err := m.Reopen("", id)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer sess.Stop()

	if got, ok := m.Get(id); !ok || got != sess {
		t.Fatalf("reopened session not registered as live")
	}
	if sess.Mode != "work" {
		t.Fatalf("Mode = %q, want work", sess.Mode)
	}

	want := engine.ReplayHistory(events)
	got := sess.currentLoop().History()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("history mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

// (c) The resume run-loop continues on the existing log: SendInput triggers a new
// model_turn appended to the SAME events.jsonl with a monotonic seq, and a
// session_reopened marker is recorded.
func TestResumeRunLoopAppends(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	// Pre-populate so seq starts > 1.
	log.Record("coordinator", event.SessionStarted, map[string]any{"mode": "chat"})
	log.Record("coordinator", event.ModelTurn, map[string]any{"text": "earlier"})
	tailSeq := log.LastSeq()

	em := event.NewEmitter(log, "coordinator")
	s := newStopSession(t)
	s.log = log
	s.emitter = em
	s.inter = newInteraction("autonomous", em)
	s.Mode = "chat"
	s.resumed = true

	reg := tools.New()
	loop := &engine.Loop{
		Client:  &scriptTurner{text: "continued"},
		Model:   "test",
		Tools:   reg,
		Emitter: em,
		Steer:   s,
	}
	s.loop = loop

	go s.run()

	// Wait until the reopen marker is recorded and the session is idle.
	deadline := time.Now().Add(2 * time.Second)
	for !hasType(s.log.Snapshot(), event.SessionReopened) {
		if time.Now().After(deadline) {
			t.Fatal("session_reopened never recorded")
		}
		time.Sleep(time.Millisecond)
	}

	if err := s.SendInput("continue"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	// A new model_turn should be appended after the pre-existing tail.
	deadline = time.Now().Add(2 * time.Second)
	for {
		snap := s.log.Snapshot()
		var newTurn *event.Event
		for i := range snap {
			ev := snap[i]
			if ev.Type == event.ModelTurn && ev.Seq > tailSeq {
				newTurn = &snap[i]
			}
		}
		if newTurn != nil {
			if newTurn.Seq <= tailSeq {
				t.Fatalf("new model_turn seq %d not > tail %d", newTurn.Seq, tailSeq)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new model_turn never appended after resume")
		}
		time.Sleep(time.Millisecond)
	}

	s.Stop()
}
