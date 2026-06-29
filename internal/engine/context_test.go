package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
)

// errTurner is a fake Turner that always fails with a fixed error, used to
// exercise the loop's error handling.
type errTurner struct{ err error }

func (e *errTurner) Turn(gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return nil, e.err
}

func TestIsContextLengthError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			"anthropic prompt too long",
			fmt.Errorf("API returned non-200 status code 400: {\"error\":{\"message\":\"prompt is too long: 250000 tokens > 200000 maximum\"}}"),
			true,
		},
		{
			"openai context_length_exceeded",
			fmt.Errorf("API returned non-200 status code 400: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"This model's maximum context length is 128000 tokens.\"}}"),
			true,
		},
		{
			"openai reduce length hint",
			errors.New("API returned non-200 status code 400: please reduce the length of the messages"),
			true,
		},
		{"transient 503", errors.New("API returned non-200 status code 503: service unavailable"), false},
		{"network error", errors.New("error sending request: connection refused"), false},
		{"output truncation", errors.New("turn 3 truncated at the output token cap; raise max_tokens"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsContextLengthError(c.err); got != c.want {
				t.Fatalf("IsContextLengthError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestApproxContextTokens(t *testing.T) {
	if n := approxContextTokens("", nil); n != 0 {
		t.Fatalf("empty estimate = %d, want 0", n)
	}
	small := approxContextTokens("system", []gollama.Message{{Role: "user", Content: "hello"}})
	larger := approxContextTokens("system", []gollama.Message{
		{Role: "user", Content: strings.Repeat("hello ", 100)},
		{Role: "assistant", Content: strings.Repeat("world ", 100)},
	})
	if larger <= small {
		t.Fatalf("expected larger estimate to grow: small=%d larger=%d", small, larger)
	}
	// MultiContent text blocks are counted too.
	withBlocks := approxContextTokens("", []gollama.Message{{
		Role:         "user",
		MultiContent: []gollama.ContentBlock{{Type: "text", Text: strings.Repeat("x", 400)}},
	}})
	if withBlocks <= 0 {
		t.Fatalf("MultiContent text not counted: got %d", withBlocks)
	}
}

// A context-length error from the backend fails the loop with a clear, actionable
// message (mentioning "context window exceeded") rather than the opaque provider
// error, and emits a matching SessionError event (task 0010).
func TestLoopFailsOnContextLengthError(t *testing.T) {
	turner := &errTurner{err: errors.New("API returned non-200 status code 400: prompt is too long: 250000 tokens > 200000 maximum")}
	rec := &captureRecorder{}
	loop := newLoop(t, turner)
	loop.Emitter = event.NewEmitter(rec, "agent")
	loop.Seed("do the thing")

	_, err := loop.Run(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "context window exceeded") {
		t.Fatalf("error = %q, want it to mention 'context window exceeded'", err.Error())
	}

	var sawSessionErr bool
	for _, ev := range rec.evs {
		if ev.Type == event.SessionError {
			sawSessionErr = true
			msg, _ := ev.Data["msg"].(string)
			if msg != err.Error() {
				t.Fatalf("SessionError msg = %q, want it to match returned error %q", msg, err.Error())
			}
		}
	}
	if !sawSessionErr {
		t.Fatal("no SessionError event emitted")
	}
}
