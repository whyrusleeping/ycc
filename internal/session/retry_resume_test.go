package session

import (
	"errors"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// flakyTurner fails its first `failures` calls, then returns a text turn. Used
// to exercise retry-after-error without a network call.
type flakyTurner struct {
	failures int
	calls    int
	text     string
}

func (f *flakyTurner) Turn(gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, errors.New("boom: connection reset by peer")
	}
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{
		Message: gollama.Message{Role: "assistant", Content: f.text},
	}}}, nil
}

// After an LLM API error exhausts automatic retries the session parks in
// StatusError; Resume() then re-runs the failed turn on the existing history —
// no new user message — and the session recovers to idle. This is the daemon
// side of the iOS "Retry" button.
func TestResumeRetriesAfterError(t *testing.T) {
	s := newStopSession(t)
	s.inter = newInteraction("autonomous", s.emitter)
	s.Mode = "chat"
	s.prompt = "do the thing"
	s.retryCh = make(chan struct{})
	s.messageCh = make(chan engine.UserMessage, 4)

	turner := &flakyTurner{failures: 1, text: "recovered"}
	// MaxAttempts: 1 disables the in-loop backoff retry so the single failure
	// surfaces immediately as a session_error.
	loop := &engine.Loop{
		Client:  turner,
		Model:   "test",
		Tools:   tools.New(),
		Emitter: s.emitter,
		Steer:   s,
		Retry:   engine.RetryPolicy{MaxAttempts: 1},
	}
	s.loop = loop

	go s.run()

	// The failed turn parks the session in StatusError with a session_error row.
	waitStatus(t, s, event.StatusError)
	if !hasType(s.log.Snapshot(), event.SessionError) {
		t.Fatal("no session_error recorded after the failed turn")
	}
	if turner.calls != 1 {
		t.Fatalf("turner called %d times before retry, want 1", turner.calls)
	}

	// Retry: nudge the parked loop. Poll Resume until it takes effect to close the
	// microscopic window between setStatus(error) and the run loop's idle wait.
	deadline := time.Now().Add(2 * time.Second)
	for !hasType(s.log.Snapshot(), event.SessionIdle) {
		if time.Now().After(deadline) {
			t.Fatal("session never recovered to idle after Resume-triggered retry")
		}
		if err := s.Resume(); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	if turner.calls < 2 {
		t.Fatalf("turner called %d times, want the failed turn re-run (>=2)", turner.calls)
	}
	// The retry re-ran the SAME turn — no bogus user_input was injected. Exactly
	// one user_input (the original prompt echo) should exist.
	if n := countType(s.log.Snapshot(), event.UserInput); n != 1 {
		t.Fatalf("user_input count = %d, want 1 (retry must not inject a message)", n)
	}
	// The recovered turn is recorded.
	if !hasType(s.log.Snapshot(), event.ModelTurn) {
		t.Fatal("no model_turn recorded after the retry")
	}

	s.Stop()
}
