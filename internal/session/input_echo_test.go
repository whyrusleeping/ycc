package session

import (
	"sync"
	"testing"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
)

// captureRecorder records every event it receives for later inspection.
type captureRecorder struct {
	mu     sync.Mutex
	seq    int
	events []event.Event
}

func (c *captureRecorder) Record(actor string, t event.Type, data map[string]any) event.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	ev := event.Event{Seq: c.seq, Actor: actor, Type: t, Data: data}
	c.events = append(c.events, ev)
	return ev
}

func (c *captureRecorder) count(t event.Type) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, ev := range c.events {
		if ev.Type == t {
			n++
		}
	}
	return n
}

func (c *captureRecorder) lastText(t event.Type) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.events) - 1; i >= 0; i-- {
		if c.events[i].Type == t {
			s, _ := c.events[i].Data["text"].(string)
			return s, true
		}
	}
	return "", false
}

// SendInput must emit the user_input echo at the moment the prod is accepted,
// not when the (possibly busy) run loop later dequeues it. Here we simulate a
// busy loop by never draining the idle queue; the echo must still be recorded promptly
// and exactly once.
func TestSendInputEmitsEchoOnEnqueue(t *testing.T) {
	rec := &captureRecorder{}
	s := &Session{
		ID:        "test",
		emitter:   event.NewEmitter(rec, "coordinator"),
		inter:     newInteraction(false, event.NewEmitter(rec, "coordinator")),
		messageCh: make(chan engine.UserMessage, 4),
	}

	if err := s.SendInput("hello there"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	// The echo is recorded immediately, before any loop dequeues it.
	if got, ok := rec.lastText(event.UserInput); !ok || got != "hello there" {
		t.Fatalf("user_input echo = %q (ok=%v), want %q", got, ok, "hello there")
	}
	if n := rec.count(event.UserInput); n != 1 {
		t.Fatalf("user_input recorded %d times, want exactly 1", n)
	}

	// The text is still queued for the loop to Post when it next goes idle.
	select {
	case input := <-s.messageCh:
		if input.Text != "hello there" {
			t.Fatalf("queued text = %q, want %q", input.Text, "hello there")
		}
	default:
		t.Fatal("text was not enqueued onto the idle queue")
	}

	// Draining (the run loop's job) must not record a second echo.
	if n := rec.count(event.UserInput); n != 1 {
		t.Fatalf("user_input recorded %d times after dequeue, want 1", n)
	}
}

// A buffer-full SendInput returns an error and records no echo.
func TestSendInputBufferFullNoEcho(t *testing.T) {
	rec := &captureRecorder{}
	s := &Session{
		ID:        "test",
		emitter:   event.NewEmitter(rec, "coordinator"),
		inter:     newInteraction(false, event.NewEmitter(rec, "coordinator")),
		messageCh: make(chan engine.UserMessage, 1),
	}
	if err := s.SendInput("first"); err != nil {
		t.Fatalf("SendInput first: %v", err)
	}
	// Buffer is now full; second send fails and emits nothing extra.
	if err := s.SendInput("second"); err == nil {
		t.Fatal("expected buffer-full error on second SendInput")
	}
	if n := rec.count(event.UserInput); n != 1 {
		t.Fatalf("user_input recorded %d times, want 1 (no echo on full buffer)", n)
	}
}
