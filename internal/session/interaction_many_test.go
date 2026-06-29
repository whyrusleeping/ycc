package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

func waitBatchPending(t *testing.T, in *interaction) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		in.mu.Lock()
		pending := in.batchWaiting != nil
		in.mu.Unlock()
		if pending {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("batch question never became pending")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// In autonomous mode AskMany must not block: it auto-answers every question and
// records each as an assumption.
func TestAskManyAutonomous(t *testing.T) {
	in := newInteraction("autonomous", discardEmitter())
	qs := []orchestrator.Question{
		{Prompt: "which database?"},
		{Prompt: "which language?", Options: []string{"go", "rust"}},
	}
	ans, err := in.AskMany(context.Background(), qs)
	if err != nil {
		t.Fatal(err)
	}
	if len(ans) != 2 {
		t.Fatalf("answers = %v", ans)
	}
	for _, a := range ans {
		if !strings.Contains(strings.ToLower(a), "autonomous") {
			t.Fatalf("answer = %q", a)
		}
	}
	as := in.Assumptions()
	if len(as) != 2 || as[0] != "which database?" || as[1] != "which language?" {
		t.Fatalf("assumptions = %v", as)
	}
}

// Interactive AskMany blocks until AnswerAll delivers; option indices resolve to
// option text and free-text answers pass through.
func TestAskManyInteractive(t *testing.T) {
	in := newInteraction("interactive", discardEmitter())

	// No batch pending yet.
	if in.AnswerAll([]answer{{idx: 0}}) {
		t.Fatal("AnswerAll with no pending batch should return false")
	}

	qs := []orchestrator.Question{
		{Prompt: "db?", Options: []string{"postgres", "sqlite"}},
		{Prompt: "name?"},
	}
	got := make(chan []string, 1)
	go func() {
		a, _ := in.AskMany(context.Background(), qs)
		got <- a
	}()
	waitBatchPending(t, in)

	if !in.AnswerAll([]answer{{idx: 1}, {idx: -1, text: "myproj"}}) {
		t.Fatal("AnswerAll on pending batch should return true")
	}
	select {
	case a := <-got:
		if len(a) != 2 || a[0] != "sqlite" || a[1] != "myproj" {
			t.Fatalf("answers = %v, want [sqlite myproj]", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AskMany did not return after AnswerAll")
	}
}

// A cancelled context unblocks a pending AskMany with an error.
func TestAskManyContextCancel(t *testing.T) {
	in := newInteraction("judgement", discardEmitter())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := in.AskMany(ctx, []orchestrator.Question{{Prompt: "q1"}, {Prompt: "q2"}})
		done <- err
	}()
	waitBatchPending(t, in)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error on cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AskMany did not return on cancel")
	}
}
