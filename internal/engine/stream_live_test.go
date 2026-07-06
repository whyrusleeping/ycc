package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// TestLoopStreamsLiveAnthropic is the end-to-end proof (folded task 0114) that a
// real gollama Anthropic client streams a turn through the engine seam onto the
// transient turn_delta path: TurnStream deltas → throttled turn_delta broadcasts
// → a clearing done delta, while the durable model_turn carries the final text
// and NO delta ever touches events.jsonl. Set ANTHROPIC_API_KEY to run.
func TestLoopStreamsLiveAnthropic(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping live streaming e2e test")
	}

	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := event.OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	stop := collectDeltas(t, l)

	// A real gollama client satisfies engine.StreamTurner with no adapter, so the
	// loop's turnOnce takes the streaming path automatically.
	client := gollama.NewClient("https://api.anthropic.com")
	client.SetAnthropicMode(true)
	client.SetAPIKey(key)
	client.SetMaxRetries(0)

	loop := &Loop{
		Client:    client,
		Model:     "claude-opus-4-8",
		ModelName: "claude",
		Backend:   "anthropic",
		System:    "You are concise.",
		Tools:     tools.New(), // no tools => a single text-only turn, then yield
		Emitter:   event.NewEmitter(l, "agent"),
		MaxTurns:  2,
		MaxTok:    1024,
	}
	loop.Post("In about 150 words, explain what a hash map is and why lookups are fast.")

	res, err := loop.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Report) == "" {
		t.Fatalf("expected non-empty final report")
	}

	deltas := stop()

	var texts []string
	var sawDone bool
	for _, d := range deltas {
		if d.Seq != 0 || !d.Transient {
			t.Fatalf("delta not transient/seq0: %+v", d)
		}
		if done, _ := d.Data["done"].(bool); done {
			sawDone = true
			if txt, _ := d.Data["text"].(string); txt != "" {
				t.Fatalf("done delta carried text %q, want empty", txt)
			}
			continue
		}
		texts = append(texts, d.Data["text"].(string))
	}
	if len(texts) < 2 {
		t.Fatalf("expected >=2 streamed turn_delta snapshots, got %d", len(texts))
	}
	if !sawDone {
		t.Fatalf("no clearing done delta; deltas=%+v", deltas)
	}
	// Snapshots are full accumulated text: each must be a growing prefix.
	for i := 1; i < len(texts); i++ {
		if !strings.HasPrefix(texts[i], texts[i-1]) {
			t.Errorf("snapshot %d %q is not a prefix-extension of %q", i, texts[i], texts[i-1])
		}
	}

	// The durable model_turn carries the final text, and every streamed snapshot
	// is a prefix of it (deltas are throttled, so the last need not equal it).
	onDisk, err := event.ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	var modelTurnText string
	var found bool
	for _, ev := range onDisk {
		if ev.Type == event.TurnDelta {
			t.Fatalf("turn_delta persisted to events.jsonl: %+v", ev)
		}
		if ev.Type == event.ModelTurn {
			modelTurnText, _ = ev.Data["text"].(string)
			found = true
		}
	}
	if !found {
		t.Fatalf("no persisted model_turn found")
	}
	if strings.TrimSpace(modelTurnText) == "" {
		t.Fatalf("persisted model_turn has empty text")
	}
	last := texts[len(texts)-1]
	if !strings.HasPrefix(modelTurnText, last) {
		t.Errorf("last streamed snapshot is not a prefix of the final model_turn text\n snapshot: %q\n final:    %q", last, modelTurnText)
	}
	t.Logf("snapshots=%d final_len=%d", len(texts), len(modelTurnText))
}
