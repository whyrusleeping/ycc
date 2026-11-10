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
