package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

type failAtRecorder struct {
	failAt event.Type
	failed error
	seq    int
	events []event.Event
}

func (r *failAtRecorder) Record(actor string, typ event.Type, data map[string]any) event.Event {
	if r.failed != nil {
		return event.Event{Actor: actor, Type: typ, Data: data}
	}
	if typ == r.failAt {
		r.failed = errors.New("injected recorder failure")
		return event.Event{Actor: actor, Type: typ, Data: data}
	}
	r.seq++
	ev := event.Event{Seq: r.seq, Actor: actor, Type: typ, Data: data}
	r.events = append(r.events, ev)
	return ev
}

func (r *failAtRecorder) Err() error { return r.failed }

type failingCheckpoint struct {
	emitter *event.Emitter
}

func (s *failingCheckpoint) Checkpoint(context.Context) ([]string, error) {
	s.emitter.Emit("user_input_delivered", map[string]any{"text": "not durable"})
	// Deliberately return a message despite the failed record: Loop's continuation
	// barrier must independently prevent it reaching history or a model request.
	return []string{"not durable"}, nil
}

func TestLoopCheckpointPersistenceFailureStopsBeforeModelRequest(t *testing.T) {
	recorder := &failAtRecorder{failAt: event.UserInputDelivered}
	emitter := event.NewEmitter(recorder, "agent")
	turner := &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{assistantText("unexpected")}}
	loop := &Loop{
		Client:  turner,
		Model:   "test",
		Tools:   tools.New(),
		Emitter: emitter,
		Steer:   &failingCheckpoint{emitter: emitter},
	}

	res, err := loop.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "event log persistence failed") {
		t.Fatalf("Run result/error = %+v / %v, want checkpoint persistence failure", res, err)
	}
	if turner.calls != 0 {
		t.Fatalf("model requests = %d, want 0", turner.calls)
	}
	if history := loop.History(); len(history) != 0 {
		t.Fatalf("checkpoint message entered history after failed record: %+v", history)
	}
}

func TestLoopAbortsBeforeMutationAfterDurableEmitFailure(t *testing.T) {
	tests := []struct {
		name          string
		failAt        event.Type
		wantToolCalls int
		wantHistory   int
	}{
		{
			name:          "model turn",
			failAt:        event.ModelTurn,
			wantToolCalls: 0,
			wantHistory:   0,
		},
		{
			name:          "tool call",
			failAt:        event.ToolCall,
			wantToolCalls: 0,
			// The assistant tool-use turn was durably recorded before the
			// tool_call failure; no post-failure tool result enters history.
			wantHistory: 1,
		},
		{
			name:          "tool result",
			failAt:        event.ToolResult,
			wantToolCalls: 1,
			// Dispatch happened only after a durable tool_call, but its
			// non-durable result must not be appended to model history.
			wantHistory: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatched := 0
			reg := tools.New()
			reg.Add(&gollama.Tool{
				Name:   "spy",
				Params: gollama.ToolFunctionParams{Type: "object", Properties: map[string]any{}},
				Call: func(context.Context, any) (*gollama.ToolResult, error) {
					dispatched++
					return &gollama.ToolResult{Content: "mutated"}, nil
				},
			})
			recorder := &failAtRecorder{failAt: tt.failAt}
			loop := &Loop{
				Client:  &scriptedTurner{responses: []*gollama.ResponseMessageGenerate{assistantToolCall("spy", `{}`)}},
				Model:   "test",
				Tools:   reg,
				Emitter: event.NewEmitter(recorder, "agent"),
			}

			res, err := loop.Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), "event log persistence failed") {
				t.Fatalf("Run result/error = %+v / %v, want persistence failure", res, err)
			}
			if !errors.Is(err, recorder.failed) {
				t.Fatalf("Run error %v does not wrap recorder failure %v", err, recorder.failed)
			}
			if dispatched != tt.wantToolCalls {
				t.Fatalf("tool dispatches = %d, want %d", dispatched, tt.wantToolCalls)
			}
			if got := len(loop.History()); got != tt.wantHistory {
				t.Fatalf("history length = %d, want %d; history=%+v", got, tt.wantHistory, loop.History())
			}
		})
	}
}
