package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
)

// newStopSession builds a minimal Session backed by a real on-disk log so Stop's
// terminal event is durably recorded and an ask_user can block on the ctx.
func newStopSession(t *testing.T) *Session {
	t.Helper()
	log, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	em := event.NewEmitter(log, "coordinator")
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		ID:      "test",
		log:     log,
		emitter: em,
		inter:   newInteraction("interactive", em),
		inputCh: make(chan string, 4),
		ctx:     ctx,
		cancel:  cancel,
		status:  event.StatusRunning,
	}
}

// countType returns how many events of type t are in the snapshot.
func countType(events []event.Event, t event.Type) int {
	n := 0
	for _, ev := range events {
		if ev.Type == t {
			n++
		}
	}
	return n
}

// hasType reports whether the snapshot contains an event of type t.
func hasType(events []event.Event, t event.Type) bool {
	return countType(events, t) > 0
}

// A session blocked in ask_user unblocks cleanly when Stop cancels the ctx, the
// status becomes stopped, and exactly one session_stopped event is recorded
// (idempotent across repeated Stop calls).
func TestSessionStopUnblocksAsk(t *testing.T) {
	s := newStopSession(t)

	type result struct {
		ans string
		err error
	}
	done := make(chan result, 1)
	go func() {
		ans, err := s.inter.Ask(s.ctx, "q?", nil)
		done <- result{ans, err}
	}()

	// Wait for the question to be asked (it is durably logged by the emitter).
	deadline := time.Now().Add(2 * time.Second)
	for !hasType(s.log.Snapshot(), event.QuestionAsked) {
		if time.Now().After(deadline) {
			t.Fatal("question_asked never recorded")
		}
		time.Sleep(time.Millisecond)
	}

	s.Stop()

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatalf("Ask returned nil error; want context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not unblock on Stop")
	}

	if s.Status() != event.StatusStopped {
		t.Fatalf("Status = %q, want %q", s.Status(), event.StatusStopped)
	}
	if n := countType(s.log.Snapshot(), event.SessionStopped); n != 1 {
		t.Fatalf("session_stopped count = %d, want 1", n)
	}

	// Idempotent: a second Stop records no additional session_stopped event.
	s.Stop()
	if n := countType(s.log.Snapshot(), event.SessionStopped); n != 1 {
		t.Fatalf("session_stopped count after 2nd Stop = %d, want 1", n)
	}
}

// Manager.Stop removes the session from the map (no leak) and reports
// ErrUnknownSession for an unknown / already-stopped id.
func TestManagerStop(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	s := newStopSession(t)
	s.ID = "s_test"

	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	if err := m.Stop(s.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, ok := m.Get(s.ID); ok {
		t.Fatal("session still present after Stop")
	}

	err := m.Stop(s.ID)
	if !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("second Stop err = %v, want ErrUnknownSession", err)
	}
}
