package session

import (
	"context"
	"testing"

	"github.com/whyrusleeping/ycc/internal/event"
)

// setRunning flips the mid-run flag the way the run loop does.
func (s *Session) setRunning(v bool) {
	s.steerMu.Lock()
	s.running = v
	s.steerMu.Unlock()
}

// Mid-run SendInput (steer-by-default): the text is queued as a correction with a
// queued:true echo rather than pushed to inputCh, and an unpaused Checkpoint
// delivers it — returning the text and emitting user_input_delivered referencing
// the queued echo's seq (spec §18.7).
func TestSteerByDefaultDeliversAtCheckpoint(t *testing.T) {
	s, rec := newSteerSession()
	s.setRunning(true)

	if err := s.SendInput("no, wrong file"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	// Not pushed to inputCh — it is a mid-run correction, not an idle prod.
	select {
	case text := <-s.inputCh:
		t.Fatalf("mid-run input reached inputCh (%q); want queued as correction", text)
	default:
	}

	// The echo is present and flagged queued:true.
	echo := lastEvent(rec, event.UserInput)
	if echo == nil {
		t.Fatal("no user_input echo recorded")
	}
	if echo.Data["queued"] != true {
		t.Fatalf("echo queued flag = %v, want true", echo.Data["queued"])
	}
	if got, _ := echo.Data["text"].(string); got != "no, wrong file" {
		t.Fatalf("echo text = %q", got)
	}

	// An unpaused checkpoint delivers it (no pause ceremony).
	msgs, err := s.Checkpoint(context.Background())
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if len(msgs) != 1 || msgs[0] != "no, wrong file" {
		t.Fatalf("Checkpoint returned %v, want [no, wrong file]", msgs)
	}
	// No pause: interrupted/resumed must NOT be emitted.
	if n := rec.count(event.Interrupted); n != 0 {
		t.Fatalf("interrupted emitted %d times, want 0", n)
	}
	if n := rec.count(event.Resumed); n != 0 {
		t.Fatalf("resumed emitted %d times, want 0", n)
	}
	// A delivered event referencing the queued echo's seq is emitted.
	del := lastEvent(rec, event.UserInputDelivered)
	if del == nil {
		t.Fatal("no user_input_delivered recorded")
	}
	if del.Data["seq"] != echo.Seq {
		t.Fatalf("delivered seq = %v, want %v", del.Data["seq"], echo.Seq)
	}
}

// Multiple mid-run sends preserve FIFO order at delivery, and the fast-path
// Checkpoint is a no-op once drained.
func TestSteerByDefaultOrderAndDrain(t *testing.T) {
	s, rec := newSteerSession()
	s.setRunning(true)

	for _, txt := range []string{"first", "second", "third"} {
		if err := s.SendInput(txt); err != nil {
			t.Fatalf("SendInput(%q): %v", txt, err)
		}
	}

	msgs, err := s.Checkpoint(context.Background())
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if len(msgs) != 3 || msgs[0] != "first" || msgs[1] != "second" || msgs[2] != "third" {
		t.Fatalf("corrections = %v, want [first second third]", msgs)
	}
	if n := rec.count(event.UserInputDelivered); n != 3 {
		t.Fatalf("delivered events = %d, want 3", n)
	}

	// Drained: the next checkpoint is a cheap no-op.
	if got, err := s.Checkpoint(context.Background()); err != nil || got != nil {
		t.Fatalf("drained Checkpoint = (%v, %v), want (nil, nil)", got, err)
	}
}

// While paused, a mid-run/steer SendInput still buffers (queued echo) and drains
// only on explicit Resume — now also emitting delivered events (§18.7 unchanged).
func TestSteerPausedStillDrainsOnResume(t *testing.T) {
	s, rec := newSteerSession()

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	done := make(chan []string, 1)
	go func() {
		msgs, _ := s.Checkpoint(context.Background())
		done <- msgs
	}()
	waitStatus(t, s, event.StatusPaused)

	if err := s.SendInput("hold"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	echo := lastEvent(rec, event.UserInput)
	if echo == nil || echo.Data["queued"] != true {
		t.Fatalf("paused echo not flagged queued: %+v", echo)
	}

	if err := s.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	msgs := <-done
	if len(msgs) != 1 || msgs[0] != "hold" {
		t.Fatalf("corrections = %v, want [hold]", msgs)
	}
	if n := rec.count(event.UserInputDelivered); n != 1 {
		t.Fatalf("delivered events = %d, want 1", n)
	}
	if rec.count(event.Interrupted) != 1 || rec.count(event.Resumed) != 1 {
		t.Fatalf("interrupted/resumed counts = %d/%d, want 1/1", rec.count(event.Interrupted), rec.count(event.Resumed))
	}
}

// Idle SendInput is unchanged: it goes via inputCh with a plain echo (no queued
// flag, no delivered event).
func TestIdleSendInputUnchanged(t *testing.T) {
	s, rec := newSteerSession()
	// running=false, not paused — the idle path.
	if err := s.SendInput("go"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	select {
	case text := <-s.inputCh:
		if text != "go" {
			t.Fatalf("inputCh text = %q", text)
		}
	default:
		t.Fatal("idle input did not reach inputCh")
	}
	echo := lastEvent(rec, event.UserInput)
	if echo == nil {
		t.Fatal("no echo recorded")
	}
	if _, ok := echo.Data["queued"]; ok {
		t.Fatalf("idle echo carries queued flag: %+v", echo.Data)
	}
	if n := rec.count(event.UserInputDelivered); n != 0 {
		t.Fatalf("delivered events = %d, want 0", n)
	}
}

// lastEvent returns the last recorded event of type t (or nil).
func lastEvent(c *captureRecorder, t event.Type) *event.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.events) - 1; i >= 0; i-- {
		if c.events[i].Type == t {
			ev := c.events[i]
			return &ev
		}
	}
	return nil
}
