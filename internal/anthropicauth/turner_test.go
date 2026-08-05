package anthropicauth

import (
	"errors"
	"reflect"
	"testing"

	"github.com/whyrusleeping/gollama"
)

type captureTurner struct {
	turnOpts   gollama.RequestOptions
	streamOpts gollama.RequestOptions
	delta      string
	err        error
}

func (c *captureTurner) Turn(opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	c.turnOpts = opts
	return nil, c.err
}

func (c *captureTurner) TurnStream(opts gollama.RequestOptions, onDelta func(string)) (*gollama.ResponseMessageGenerate, error) {
	c.streamOpts = opts
	if c.delta != "" {
		onDelta(c.delta)
	}
	return nil, c.err
}

func wantBlocks(prompt string) []gollama.SystemBlock {
	return []gollama.SystemBlock{
		{Text: BillingSystemPrefix},
		{Text: AgentSystemPrefix},
		{Text: prompt, Cache: true},
	}
}

func TestPrefixSystemString(t *testing.T) {
	opts := gollama.RequestOptions{System: "You are ycc."}
	got := PrefixSystem(opts)
	if got.System != "" {
		t.Fatalf("System = %q, want empty after promotion", got.System)
	}
	if want := wantBlocks("You are ycc."); !reflect.DeepEqual(got.SystemBlocks, want) {
		t.Fatalf("SystemBlocks = %#v, want %#v", got.SystemBlocks, want)
	}
	// The input value and repeated wrapping are stable.
	if opts.System != "You are ycc." || len(opts.SystemBlocks) != 0 {
		t.Fatalf("input mutated: %#v", opts)
	}
	if twice := PrefixSystem(got); !reflect.DeepEqual(twice.SystemBlocks, got.SystemBlocks) {
		t.Fatalf("PrefixSystem is not idempotent: %#v", twice.SystemBlocks)
	}
}

func TestPrefixSystemPreservesExistingBlocks(t *testing.T) {
	original := []gollama.SystemBlock{{Text: "static", Cache: true}, {Text: "dynamic"}}
	got := PrefixSystem(gollama.RequestOptions{System: "ignored by gollama precedence", SystemBlocks: original})
	want := []gollama.SystemBlock{{Text: BillingSystemPrefix}, {Text: AgentSystemPrefix}, original[0], original[1]}
	if !reflect.DeepEqual(got.SystemBlocks, want) || got.System != "" {
		t.Fatalf("got System=%q blocks=%#v, want blocks=%#v", got.System, got.SystemBlocks, want)
	}
}

func TestTurnerPrefixesStreamingAndNonStreaming(t *testing.T) {
	inner := &captureTurner{delta: "live", err: errors.New("stop")}
	turner := NewTurner(inner)
	opts := gollama.RequestOptions{System: "You are ycc."}
	if _, err := turner.Turn(opts); !errors.Is(err, inner.err) {
		t.Fatalf("Turn error = %v", err)
	}
	var delta string
	if _, err := turner.TurnStream(opts, func(s string) { delta = s }); !errors.Is(err, inner.err) {
		t.Fatalf("TurnStream error = %v", err)
	}
	if delta != "live" {
		t.Fatalf("delta = %q", delta)
	}
	want := wantBlocks("You are ycc.")
	if !reflect.DeepEqual(inner.turnOpts.SystemBlocks, want) {
		t.Fatalf("Turn blocks = %#v", inner.turnOpts.SystemBlocks)
	}
	if !reflect.DeepEqual(inner.streamOpts.SystemBlocks, want) {
		t.Fatalf("TurnStream blocks = %#v", inner.streamOpts.SystemBlocks)
	}
}
