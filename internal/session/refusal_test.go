package session

import (
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// refusalTurner returns a provider-side safety refusal (stop_reason "refusal",
// empty content — Anthropic's streaming classifier shape) for its first
// `refusals` calls, then a normal text turn. Used to exercise the refusal
// park/gate/retry flow without a network call.
type refusalTurner struct {
	refusals int
	calls    int
	text     string
}

func (f *refusalTurner) Turn(gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	f.calls++
	if f.calls <= f.refusals {
		return &gollama.ResponseMessageGenerate{
			StopReason: "refusal",
			Choices:    []gollama.GenChoice{{Message: gollama.Message{Role: "assistant"}}},
		}, nil
	}
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{
		Message: gollama.Message{Role: "assistant", Content: f.text},
	}}}, nil
}

// findEvent returns the first event of type t, ok=false when absent.
func findEvent(events []event.Event, t event.Type) (event.Event, bool) {
	for _, ev := range events {
		if ev.Type == t {
			return ev, true
		}
	}
	return event.Event{}, false
}

// A provider refusal parks the session in StatusError with a kind:"refusal"
// session_error, gates SendInput (refusals are sticky — another message would
// just be refused too), and Resume() retries the pending turn as-is; recovery
// clears the gate and input flows again.
func TestRefusalGatesInputUntilRetry(t *testing.T) {
	s := newStopSession(t)
	s.inter = newInteraction(true, s.emitter)
	s.Mode = "chat"
	s.prompt = "work on task 70"
	s.retryCh = make(chan struct{})
	s.messageCh = make(chan engine.UserMessage, 4)

	turner := &refusalTurner{refusals: 1, text: "recovered"}
	loop := &engine.Loop{
		Client:  turner,
		Model:   "test",
		Tools:   tools.New(),
		Emitter: s.emitter,
		Steer:   s,
	}
	loop.Seed(s.prompt)
	s.loop = loop

	go s.run()

	// The refused turn parks the session in StatusError with the gate set.
	waitStatus(t, s, event.StatusError)
	if !s.Refused() {
		t.Fatal("Refused() = false after a refusal parked the session")
	}
	ev, ok := findEvent(s.log.Snapshot(), event.SessionError)
	if !ok {
		t.Fatal("no session_error recorded for the refusal")
	}
	if kind, _ := ev.Data["kind"].(string); kind != "refusal" {
		t.Fatalf("session_error kind = %q, want refusal", kind)
	}
	if msg, _ := ev.Data["msg"].(string); !strings.Contains(msg, "refused") {
		t.Fatalf("session_error msg should explain the refusal, got %q", msg)
	}

	// Input is gated: the send is rejected with guidance and no user_input echo
	// is recorded (the message never entered the conversation).
	if err := s.SendInput("hello? are you there?"); err == nil {
		t.Fatal("SendInput while refused should be rejected")
	} else if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("gate error should mention the refusal, got %v", err)
	}
	if n := countType(s.log.Snapshot(), event.UserInput); n != 1 {
		t.Fatalf("user_input count = %d, want 1 (gated send must not echo)", n)
	}

	// Resume() retries the pending turn — no new user message — and recovers.
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
		t.Fatalf("turner called %d times, want the refused turn re-run (>=2)", turner.calls)
	}
	if n := countType(s.log.Snapshot(), event.UserInput); n != 1 {
		t.Fatalf("user_input count = %d, want 1 (retry must not inject a message)", n)
	}
	if s.Refused() {
		t.Fatal("Refused() still true after recovery")
	}
	// The gate is open again.
	if err := s.SendInput("thanks"); err != nil {
		t.Fatalf("SendInput after recovery: %v", err)
	}

	s.Stop()
}

// A coordinator model change via SetRoleConfig is the provider-documented
// recovery from a refusal: it clears the input gate and nudges the parked run
// loop (retryCh) so the pending turn re-runs on the new backend automatically.
func TestSetRoleConfigClearsRefusalAndRetries(t *testing.T) {
	s, _ := newTestSession(t)
	s.retryCh = make(chan struct{}, 1)
	s.setRefused(true)

	if err := s.SetRoleConfig("b", "", nil); err != nil {
		t.Fatal(err)
	}
	if s.Refused() {
		t.Fatal("Refused() still true after a coordinator model change")
	}
	select {
	case <-s.retryCh:
	default:
		t.Fatal("no retry nudge after a coordinator model change cleared the refusal")
	}

	// A role change that does NOT touch the coordinator leaves the gate alone:
	// the conversation would still be refused on the same coordinator model.
	s.setRefused(true)
	if err := s.SetRoleConfig("", "c", nil); err != nil {
		t.Fatal(err)
	}
	if !s.Refused() {
		t.Fatal("implementer-only change must not clear the refusal gate")
	}
	select {
	case <-s.retryCh:
		t.Fatal("implementer-only change must not nudge a retry")
	default:
	}
}
