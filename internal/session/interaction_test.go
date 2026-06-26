package session

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
)

func discardEmitter() *event.Emitter {
	return event.NewEmitter(event.NewStdoutRecorder(io.Discard), "coordinator")
}

// In autonomous mode ask_user must not block; it records the question as an
// assumption and tells the agent to proceed.
func TestAutonomousDoesNotBlock(t *testing.T) {
	in := newInteraction("autonomous", discardEmitter())
	ans, err := in.Ask(context.Background(), "which database?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(ans), "autonomous") {
		t.Fatalf("answer = %q", ans)
	}
	as := in.Assumptions()
	if len(as) != 1 || as[0] != "which database?" {
		t.Fatalf("assumptions = %v", as)
	}
}

// In interactive mode ask_user blocks until an answer arrives.
func TestInteractiveBlocksUntilAnswer(t *testing.T) {
	in := newInteraction("interactive", discardEmitter())
	if in.Answer("nope") {
		t.Fatal("Answer with no pending question should return false")
	}

	done := make(chan string, 1)
	go func() {
		ans, _ := in.Ask(context.Background(), "proceed?", nil)
		done <- ans
	}()

	// Wait until the question is pending, then answer it.
	deadline := time.Now().Add(2 * time.Second)
	for !in.Answer("yes") {
		if time.Now().After(deadline) {
			t.Fatal("question never became pending")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case ans := <-done:
		if ans != "yes" {
			t.Fatalf("answer = %q", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not return after answer")
	}
	if len(in.Assumptions()) != 0 {
		t.Fatal("interactive mode should not record assumptions")
	}
}

// A cancelled context unblocks a pending ask_user with an error.
func TestInteractiveContextCancel(t *testing.T) {
	in := newInteraction("judgement", discardEmitter())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := in.Ask(ctx, "q", nil)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error on cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not return on cancel")
	}
}
