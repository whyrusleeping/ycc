//go:build liveeval

package orchestrator

// Paid, opt-in behavioural check of the production chat prompt (0512): when a
// turn ends on a decision the user must make, the agent asks through ask_user
// with options instead of ending its reply on a prose question.
//
//	go test -tags liveeval -run TestLiveDecisionAsksWithOptions -v ./internal/orchestrator
//
// YCC_LIVE_CONFIG selects the ycc.toml (default: the user config), YCC_LIVE_MODELS
// a comma-separated list of logical models (default "claude,sol"), and
// YCC_LIVE_REPEATS the runs per scenario and model (default 1).

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
)

// optionAsker records every ask_user question with its options and declines,
// so the run ends without touching the fixture.
type optionAsker struct {
	mu   sync.Mutex
	asks []Question
}

func (a *optionAsker) Ask(_ context.Context, q string, opts []string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asks = append(a.asks, Question{Prompt: q, Options: opts})
	return "Not now, leave it as is.", nil
}

func (a *optionAsker) AskMany(_ context.Context, qs []Question) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asks = append(a.asks, qs...)
	out := make([]string, len(qs))
	for i := range out {
		out[i] = "Not now, leave it as is."
	}
	return out, nil
}

func (a *optionAsker) Confirm(context.Context, string) (bool, error) { return false, nil }

func (a *optionAsker) withOptions() []Question {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Question
	for _, q := range a.asks {
		if len(q.Options) >= 2 {
			out = append(out, q)
		}
	}
	return out
}

// endsWithQuestion reports whether the reply's last line is a question.
func endsWithQuestion(reply string) bool {
	lines := strings.Split(strings.TrimSpace(reply), "\n")
	last := strings.TrimRight(strings.TrimSpace(lines[len(lines)-1]), "*_` )")
	return strings.HasSuffix(last, "?")
}

func TestLiveDecisionAsksWithOptions(t *testing.T) {
	cfgPath := os.Getenv("YCC_LIVE_CONFIG")
	if cfgPath == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			t.Fatal(err)
		}
		cfgPath = filepath.Join(root, "ycc", "ycc.toml")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	reg := config.NewRegistry(cfg)
	models := strings.Split(os.Getenv("YCC_LIVE_MODELS"), ",")
	if os.Getenv("YCC_LIVE_MODELS") == "" {
		models = []string{"claude", "sol"}
	}
	repeats := 1
	if n, err := strconv.Atoi(os.Getenv("YCC_LIVE_REPEATS")); err == nil && n > 0 {
		repeats = n
	}

	scenarios := []struct {
		name  string
		files map[string]string
		ask   string
		// needsAsk: the turn cannot finish without the user's go-ahead.
		needsAsk bool
	}{
		{
			name: "go-ahead-before-change",
			files: map[string]string{
				"deploy.toml": "[service]\nname = \"api\"\nreplicas = 1\nmemory_mb = 256\n",
				"README.md":   "api service; deploy.toml is applied to production by CI on every push.\n",
			},
			ask:      "Production keeps falling over under load. Look at deploy.toml and tell me what you would change. Don't edit anything until I say so.",
			needsAsk: true,
		},
		{
			name: "pick-one-approach",
			files: map[string]string{
				"cache.go":  "package main\n\n// cache has no eviction yet; it grows without bound.\nvar cache = map[string][]byte{}\n",
				"README.md": "Small service. The owner has not decided between LRU and TTL eviction.\n",
			},
			ask:      "The cache in cache.go grows without bound. Explain the options for fixing it; I'll pick one before you write any code.",
			needsAsk: true,
		},
		{
			// Control: a plain explanation must not grow a question.
			name: "no-decision-needed",
			files: map[string]string{
				"deploy.toml": "[service]\nname = \"api\"\nreplicas = 1\nmemory_mb = 256\n",
			},
			ask: "What does deploy.toml configure? Just explain it briefly.",
		},
	}

	for _, model := range models {
		model = strings.TrimSpace(model)
		mc, ok := reg.GetModel(model)
		if !ok {
			t.Fatalf("unknown model %q", model)
		}
		for _, sc := range scenarios {
			for rep := 1; rep <= repeats; rep++ {
				t.Run(model+"/"+sc.name+"/"+strconv.Itoa(rep), func(t *testing.T) {
					ws := t.TempDir()
					for name, content := range sc.files {
						if err := os.WriteFile(filepath.Join(ws, name), []byte(content), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					asker := &optionAsker{}
					d := &Deps{
						Workspace: ws,
						Docs:      docs.NewStore(ws),
						Emitter:   event.NewEmitter(event.NewStdoutRecorder(io.Discard), "coordinator"),
						Asker:     asker,
					}
					tools, system := BuildMode("chat", d, false)
					ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
					defer cancel()
					client, modelID, err := reg.BuildContext(ctx, model)
					if err != nil {
						t.Fatal(err)
					}
					thinking := reg.ThinkingFor(model)
					loop := &engine.Loop{
						Client: client, Model: modelID, ModelName: model, Backend: mc.Backend,
						System: system, Tools: tools, Emitter: d.Emitter,
						MaxTurns: 12, MaxTok: reg.MaxTokens(),
						Thinking: thinking.Thinking, Effort: thinking.Effort, ThinkingDisplay: thinking.ThinkingDisplay,
						Retry: reg.RetryPolicy(),
					}
					loop.Seed(sc.ask)
					res, err := loop.Run(ctx)
					if err != nil {
						t.Fatal(err)
					}
					asked := asker.withOptions()
					for _, q := range asked {
						t.Logf("ask_user: %q options=%q", q.Prompt, q.Options)
					}
					t.Logf("final reply:\n%s", res.Report)
					if sc.needsAsk && len(asked) == 0 {
						t.Errorf("no ask_user question with options")
					}
					if !sc.needsAsk && len(asker.asks) > 0 {
						t.Errorf("asked %d question(s) when no decision was needed", len(asker.asks))
					}
					if endsWithQuestion(res.Report) {
						t.Errorf("final reply ends with a prose question")
					}
				})
			}
		}
	}
}
