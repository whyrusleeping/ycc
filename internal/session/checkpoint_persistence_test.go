package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

type sessionFailAtRecorder struct {
	failAt event.Type
	failed error
	seq    int
	events []event.Event
}

func (r *sessionFailAtRecorder) Record(actor string, typ event.Type, data map[string]any) event.Event {
	if r.failed != nil {
		return event.Event{Actor: actor, Type: typ, Data: data}
	}
	if typ == r.failAt {
		r.failed = errors.New("injected session recorder failure")
		return event.Event{Actor: actor, Type: typ, Data: data}
	}
	r.seq++
	ev := event.Event{Seq: r.seq, Actor: actor, Type: typ, Data: data}
	r.events = append(r.events, ev)
	return ev
}

func (r *sessionFailAtRecorder) Err() error { return r.failed }

type countingTurner struct {
	calls int
	resp  *gollama.ResponseMessageGenerate
}

func (t *countingTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	t.calls++
	return t.resp, nil
}

func TestCheckpointDeliveryFailureNeverReachesModel(t *testing.T) {
	recorder := &sessionFailAtRecorder{failAt: event.UserInputDelivered}
	emitter := event.NewEmitter(recorder, "coordinator")
	s := &Session{
		emitter: emitter,
		corrections: []correction{{
			seq:     1,
			message: engine.UserMessage{Text: "must stay out of history"},
		}},
	}
	turner := &countingTurner{resp: &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{Role: "assistant", Content: "unexpected"}}}}}
	loop := &engine.Loop{
		Client:  turner,
		Model:   "test",
		Tools:   tools.New(),
		Emitter: emitter,
		Steer:   s,
	}

	res, err := loop.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "session event log failed") {
		t.Fatalf("Run result/error = %+v / %v, want checkpoint persistence failure", res, err)
	}
	if turner.calls != 0 {
		t.Fatalf("model requests = %d, want 0 after failed user_input_delivered", turner.calls)
	}
	if history := loop.History(); len(history) != 0 {
		t.Fatalf("failed correction entered model history: %+v", history)
	}
	for _, ev := range recorder.events {
		if ev.Type == event.UserInputDelivered {
			t.Fatalf("failed user_input_delivered reported as recorded: %+v", ev)
		}
	}
}

func TestSessionIdleFailureStopsBeforeUsageAndCorrections(t *testing.T) {
	recorder := &sessionFailAtRecorder{failAt: event.SessionIdle}
	emitter := event.NewEmitter(recorder, "coordinator")
	turner := &countingTurner{resp: &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{Role: "assistant", Content: "done"}}}}}
	loop := &engine.Loop{Client: turner, Model: "test", Tools: tools.New(), Emitter: emitter}
	loop.Seed("initial prompt")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Session{
		ID:        "idle-failure",
		Mode:      "work",
		emitter:   emitter,
		loop:      loop,
		inter:     newInteraction(false, emitter),
		prompt:    "initial prompt",
		inputCh:   make(chan string, 1),
		messageCh: make(chan engine.UserMessage, 1),
		retryCh:   make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
		status:    event.StatusRunning,
		corrections: []correction{{
			seq:     3,
			message: engine.UserMessage{Text: "late correction"},
		}},
		usageSummarized: map[string]bool{},
	}

	// With Mode=work and no real Log, reaching summarizeUsage would panic. A
	// SessionIdle durability failure must return first and must not consume the
	// buffered correction.
	s.run()
	if turner.calls != 1 {
		t.Fatalf("model requests = %d, want 1", turner.calls)
	}
	if recorder.Err() == nil {
		t.Fatal("SessionIdle did not trigger recorder failure")
	}
	if len(s.usageSummarized) != 0 {
		t.Fatalf("usage summary state mutated after SessionIdle failure: %+v", s.usageSummarized)
	}
	if len(s.corrections) != 1 {
		t.Fatalf("buffered corrections consumed after SessionIdle failure: %+v", s.corrections)
	}
	for _, msg := range loop.History() {
		if msg.Content == "late correction" {
			t.Fatalf("late correction entered history after SessionIdle failure: %+v", loop.History())
		}
	}
}
