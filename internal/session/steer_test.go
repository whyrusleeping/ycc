package session

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
)

// newSteerSession builds a minimal Session exercising only the steer state
// machine (Checkpoint/Interrupt/Resume/SendInput) plus an emitter and status.
func newSteerSession() (*Session, *captureRecorder) {
	rec := &captureRecorder{}
	em := event.NewEmitter(rec, "coordinator")
	s := &Session{
		ID:        "test",
		emitter:   em,
		inter:     newInteraction(false, em),
		messageCh: make(chan engine.UserMessage, 4),
		status:    event.StatusRunning,
	}
	return s, rec
}

// waitStatus polls until the session reaches st or the deadline elapses.
func waitStatus(t *testing.T, s *Session, st event.Status) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.Status() == st {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session did not reach status %q (got %q)", st, s.Status())
}

// (a) pause → resume returns no corrections and unblocks, status running.
func TestSteerPauseResume(t *testing.T) {
	s, rec := newSteerSession()

	// Fast path: no pause pending ⇒ immediate no-op.
	if msgs, err := s.Checkpoint(context.Background()); err != nil || msgs != nil {
		t.Fatalf("no-op Checkpoint = (%v, %v), want (nil, nil)", msgs, err)
	}

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	done := make(chan struct {
		msgs []string
		err  error
	}, 1)
	go func() {
		msgs, err := s.Checkpoint(context.Background())
		done <- struct {
			msgs []string
			err  error
		}{msgs, err}
	}()

	waitStatus(t, s, event.StatusPaused)
	if err := s.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Checkpoint err: %v", r.err)
		}
		if len(r.msgs) != 0 {
			t.Fatalf("want no corrections, got %v", r.msgs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Checkpoint did not unblock on Resume")
	}
	waitStatus(t, s, event.StatusRunning)
	if rec.count(event.Interrupted) != 1 {
		t.Fatalf("interrupted emitted %d times, want 1", rec.count(event.Interrupted))
	}
	if rec.count(event.Resumed) != 1 {
		t.Fatalf("resumed emitted %d times, want 1", rec.count(event.Resumed))
	}
}

// A blocked turn/tool does not observe a pause until it reaches a checkpoint.
// Duplicate requests and a request while already paused must not leave a stale
// pause that re-interrupts the next checkpoint after Resume.
func TestPauseRequestCheckpointLifecycle(t *testing.T) {
	s, rec := newSteerSession()
	toolDone := make(chan struct{})
	checkpointDone := make(chan struct{})
	go func() {
		<-toolDone
		_, _ = s.Checkpoint(context.Background())
		close(checkpointDone)
	}()
	for i := 0; i < 2; i++ {
		if err := s.Interrupt(); err != nil {
			t.Fatal(err)
		}
	}
	if rec.count(event.PauseRequested) != 1 || rec.count(event.Interrupted) != 0 {
		t.Fatalf("while tool blocked: requests=%d interruptions=%d", rec.count(event.PauseRequested), rec.count(event.Interrupted))
	}
	close(toolDone)
	waitStatus(t, s, event.StatusPaused)
	if err := s.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if rec.count(event.PauseRequested) != 1 {
		t.Fatal("pause while paused emitted another request")
	}
	if err := s.Resume(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-checkpointDone:
	case <-time.After(2 * time.Second):
		t.Fatal("resume did not release checkpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if rec.count(event.Interrupted) != 1 || rec.count(event.Resumed) != 1 {
		t.Fatalf("interruptions=%d resumptions=%d", rec.count(event.Interrupted), rec.count(event.Resumed))
	}
}

func TestPauseRequestThenStop(t *testing.T) {
	s, rec := newSteerSession()
	log, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s.log = log
	_, s.cancel = context.WithCancel(context.Background())
	if err := s.Interrupt(); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	if rec.count(event.PauseRequested) != 1 || rec.count(event.SessionStopped) != 1 || rec.count(event.Interrupted) != 0 {
		t.Fatalf("request=%d stopped=%d interrupted=%d", rec.count(event.PauseRequested), rec.count(event.SessionStopped), rec.count(event.Interrupted))
	}
}

func TestCancelPauseBeforeCheckpoint(t *testing.T) {
	s, rec := newSteerSession()
	if err := s.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec.count(event.PauseRequested) != 1 || rec.count(event.PauseCancelled) != 1 || rec.count(event.Interrupted) != 0 {
		t.Fatalf("request=%d cancelled=%d interrupted=%d", rec.count(event.PauseRequested), rec.count(event.PauseCancelled), rec.count(event.Interrupted))
	}
}

// (b) pause → SendInput(correction) buffers; only Resume drains them in order.
func TestSteerPauseCorrect(t *testing.T) {
	s, _ := newSteerSession()

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	done := make(chan []string, 1)
	go func() {
		msgs, _ := s.Checkpoint(context.Background())
		done <- msgs
	}()
	waitStatus(t, s, event.StatusPaused)

	// Both sends only buffer; the goroutine stays blocked until Resume.
	if err := s.SendInput("first correction"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if err := s.SendInput("second correction"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	// Still blocked: no resume signaled yet.
	select {
	case <-done:
		t.Fatal("Checkpoint resumed before explicit Resume")
	case <-time.After(20 * time.Millisecond):
	}

	if err := s.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	select {
	case msgs := <-done:
		if len(msgs) != 2 || msgs[0] != "first correction" || msgs[1] != "second correction" {
			t.Fatalf("corrections = %v, want [first correction second correction]", msgs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Checkpoint did not unblock on Resume")
	}
}

// (c) pause → ctx cancel returns ctx.Err() and unblocks cleanly.
func TestSteerPauseCancel(t *testing.T) {
	s, _ := newSteerSession()
	ctx, cancel := context.WithCancel(context.Background())

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Checkpoint(ctx)
		done <- err
	}()
	waitStatus(t, s, event.StatusPaused)

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want ctx.Err() on cancel, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Checkpoint did not unblock on cancel")
	}
}
