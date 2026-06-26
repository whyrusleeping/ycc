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

// Regression: an answer arriving after a question is cancelled must not be
// buffered and silently consumed by the NEXT question.
func TestNoStaleAnswerAcrossQuestions(t *testing.T) {
	in := newInteraction("interactive", discardEmitter())

	// Q1: ask, wait until pending, then cancel it.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { in.Ask(ctx, "q1", nil); close(done) }()
	waitPending(t, in)
	cancel()
	<-done

	// A late answer to the cancelled question is rejected, not stashed.
	if in.Answer("late") {
		t.Fatal("answer to a cancelled question should be rejected")
	}

	// Q2 must block for its own answer, not pick up a stale one.
	got := make(chan string, 1)
	go func() { a, _ := in.Ask(context.Background(), "q2", nil); got <- a }()
	select {
	case a := <-got:
		t.Fatalf("q2 returned without being answered (stale): %q", a)
	case <-time.After(100 * time.Millisecond):
	}
	waitPending(t, in)
	if !in.Answer("real") {
		t.Fatal("answering q2 failed")
	}
	if a := <-got; a != "real" {
		t.Fatalf("q2 got %q, want real", a)
	}
}

func waitPending(t *testing.T, in *interaction) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		in.mu.Lock()
		pending := in.waiting != nil
		in.mu.Unlock()
		if pending {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("question never became pending")
		}
		time.Sleep(2 * time.Millisecond)
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

// AnswerOption resolves a valid index to the offered option's text, and passes
// other indices/free text through unchanged.
func TestAnswerOptionResolvesIndex(t *testing.T) {
	opts := []string{"postgres", "sqlite", "mysql"}

	// Index selection resolves to the option text.
	in := newInteraction("interactive", discardEmitter())
	got := make(chan string, 1)
	go func() { a, _ := in.Ask(context.Background(), "db?", opts); got <- a }()
	deadline := time.Now().Add(2 * time.Second)
	for !in.AnswerOption(1, "ignored") {
		if time.Now().After(deadline) {
			t.Fatal("question never went pending")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if a := <-got; a != "sqlite" {
		t.Fatalf("index answer = %q, want sqlite", a)
	}

	// Negative index is treated as free text.
	in2 := newInteraction("interactive", discardEmitter())
	got2 := make(chan string, 1)
	go func() { a, _ := in2.Ask(context.Background(), "db?", opts); got2 <- a }()
	deadline = time.Now().Add(2 * time.Second)
	for !in2.AnswerOption(-1, "cockroachdb") {
		if time.Now().After(deadline) {
			t.Fatal("question never went pending")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if a := <-got2; a != "cockroachdb" {
		t.Fatalf("free-text answer = %q, want cockroachdb", a)
	}
}
