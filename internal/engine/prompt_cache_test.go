package engine

import (
	"context"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
)

// keyTurner records the prompt_cache_key seen on every request so a test can
// assert routing stability across the turns of one loop.
type keyTurner struct {
	responses []*gollama.ResponseMessageGenerate
	calls     int
	keys      []any
	extra     []map[string]any
}

func (k *keyTurner) TurnCtx(_ context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	k.extra = append(k.extra, opts.ExtraBody)
	k.keys = append(k.keys, opts.ExtraBody["prompt_cache_key"])
	r := k.responses[k.calls]
	k.calls++
	return r, nil
}

func twoTurnScript() []*gollama.ResponseMessageGenerate {
	return []*gollama.ResponseMessageGenerate{
		assistantToolCall("bash", `{"cmd":"true"}`),
		assistantText("done"),
	}
}

// On the openai backend every request of a loop carries the same
// prompt_cache_key; an unset key is derived once per loop and differs between
// loops so concurrent agents stop competing for one cache shard.
func TestLoopPromptCacheKeyStablePerLoopOnOpenAI(t *testing.T) {
	run := func() (*keyTurner, *captureRecorder) {
		turner := &keyTurner{responses: twoTurnScript()}
		rec := &captureRecorder{}
		loop := newLoop(t, turner)
		loop.Backend = "openai"
		loop.Emitter = event.NewEmitter(rec, "agent")
		if _, err := loop.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return turner, rec
	}
	a, rec := run()
	b, _ := run()

	if len(a.keys) != 2 || len(b.keys) != 2 {
		t.Fatalf("expected two requests per loop, got %d and %d", len(a.keys), len(b.keys))
	}
	ka, ok := a.keys[0].(string)
	if !ok || ka == "" {
		t.Fatalf("openai request must carry a non-empty prompt_cache_key, got %v", a.keys[0])
	}
	if a.keys[1] != ka {
		t.Fatalf("key changed within one loop: %v then %v", a.keys[0], a.keys[1])
	}
	if b.keys[0] == ka {
		t.Fatalf("two loops derived the same key %q", ka)
	}

	var turns int
	for _, ev := range rec.evs {
		if ev.Type != event.ModelTurn {
			continue
		}
		turns++
		if got := ev.Data["prompt_cache_key"]; got != ka {
			t.Fatalf("model_turn prompt_cache_key = %v, want %q", got, ka)
		}
	}
	if turns != 2 {
		t.Fatalf("model_turn events = %d, want 2", turns)
	}
}

// An owner-assigned key (session/actor identity) is sent verbatim.
func TestLoopPromptCacheKeyExplicit(t *testing.T) {
	turner := &keyTurner{responses: twoTurnScript()}
	loop := newLoop(t, turner)
	loop.Backend = "openai"
	loop.PromptCacheKey = "s_abc/coordinator"
	if _, err := loop.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i, k := range turner.keys {
		if k != "s_abc/coordinator" {
			t.Fatalf("request %d prompt_cache_key = %v", i, k)
		}
	}
}

// Backends without the knob (Anthropic, strict openai-compatible servers that
// reject unknown body fields) get no ExtraBody at all — not even when the owner
// configured a key.
func TestLoopPromptCacheKeyOmittedOffOpenAI(t *testing.T) {
	for _, backend := range []string{"anthropic", "openai-compatible", "glm", "ollama", ""} {
		turner := &keyTurner{responses: twoTurnScript()}
		loop := newLoop(t, turner)
		loop.Backend = backend
		loop.PromptCacheKey = "s_abc/coordinator"
		if _, err := loop.Run(context.Background()); err != nil {
			t.Fatalf("%s: Run: %v", backend, err)
		}
		for i, extra := range turner.extra {
			if extra != nil {
				t.Fatalf("%s: request %d carried ExtraBody %v", backend, i, extra)
			}
		}
	}
}
