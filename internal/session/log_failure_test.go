package session

import (
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
)

func TestSessionStopsWhenEventLogFails(t *testing.T) {
	ws := t.TempDir()
	m := NewManager(testRegistry(), ws)
	log, err := event.OpenLog(t.TempDir() + "/events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.newSession(ws, "s_log_failure", "chat", false, "", log, true, "")
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	// A resumed session with no pending response emits session_reopened and then
	// waits idle without touching a backend. This keeps the real run goroutine live
	// while the injected encoding failure fires.
	s.loop = &engine.Loop{Emitter: event.NewEmitter(log, "coordinator")}
	done := make(chan struct{})
	go func() {
		s.run()
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for s.Status() != event.StatusIdle || log.LastSeq() != 1 {
		select {
		case <-deadline:
			t.Fatalf("session did not reach idle wait: status=%q seq=%d", s.Status(), log.LastSeq())
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// An unsupported JSON value drives the real Log failure path without needing
	// access to event's injected file seam. OnFailure runs synchronously and must
	// wake the blocked run goroutine.
	failed := log.Record("coordinator", event.ModelTurn, map[string]any{"unsupported": func() {}})
	if failed.Seq != 0 {
		t.Fatalf("failed Record seq = %d, want 0", failed.Seq)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session run goroutine did not exit after event-log failure")
	}
	if s.Status() != event.StatusError {
		t.Fatalf("session status = %q, want error", s.Status())
	}
	if s.ctx.Err() == nil {
		t.Fatal("session context was not cancelled after event-log failure")
	}

	// Input is rejected before either delivery channel or model history can be
	// mutated. Other public mutators are terminally gated by the same log error.
	if err := s.SendInput("must not be accepted"); err == nil || !strings.Contains(err.Error(), "event log failed") {
		t.Fatalf("SendInput after log failure error = %v, want terminal log error", err)
	}
	if len(s.inputCh) != 0 || len(s.messageCh) != 0 || len(s.loop.History()) != 0 {
		t.Fatalf("input reached failed session: input=%d messages=%d history=%+v", len(s.inputCh), len(s.messageCh), s.loop.History())
	}
	if err := s.Answer("no"); err == nil || !strings.Contains(err.Error(), "event log failed") {
		t.Fatalf("Answer after log failure error = %v, want terminal log error", err)
	}
	if err := s.Resume(); err == nil || !strings.Contains(err.Error(), "event log failed") {
		t.Fatalf("Resume after log failure error = %v, want terminal log error", err)
	}
	if snap := log.Snapshot(); len(snap) != 1 || snap[0].Type != event.SessionReopened {
		t.Fatalf("failed event or later mutation appeared in durable history: %+v", snap)
	}
}
