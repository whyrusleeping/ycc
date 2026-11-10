//go:build liveeval

package orchestrator

// Paid, opt-in behavioural check of the production chat prompt (0517): a turn
// that leaves long-running external work going proposes a watch with an
// expiry; a turn whose work completes proposes none.
//
//	go test -tags liveeval -run TestLiveWatchProposal -v ./internal/orchestrator
//
// YCC_LIVE_CONFIG, YCC_LIVE_MODELS and YCC_LIVE_REPEATS work as in
// live_decision_eval_test.go.

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
	"github.com/whyrusleeping/ycc/internal/jobs"
)

// watchAsker confirms every schedule_watch proposal and answers questions
// with a go-ahead, so a missing watch is the agent's choice, not a refusal.
type watchAsker struct {
	mu       sync.Mutex
	confirms []string
	asks     int
}

func (a *watchAsker) Ask(context.Context, string, []string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asks++
	return "Yes, go ahead.", nil
}

func (a *watchAsker) AskMany(_ context.Context, qs []Question) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asks += len(qs)
	out := make([]string, len(qs))
	for i := range out {
		out[i] = "Yes, go ahead."
	}
	return out, nil
}

func (a *watchAsker) Confirm(_ context.Context, q string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.confirms = append(a.confirms, q)
	return true, nil
}

func TestLiveWatchProposal(t *testing.T) {
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
		// wantWatch: the turn leaves remote work running past its end.
		wantWatch bool
	}{
		{
			name: "remote-soak-left-running",
			files: map[string]string{
				"soak.sh": "#!/bin/sh\necho 'Submitted soak soak-4821 to the staging cluster. It runs remotely for about 6 hours.'\n" +
					"echo 'Check progress with ./soak-status.sh soak-4821'\n",
				"soak-status.sh": "#!/bin/sh\necho \"$1: running, 2% complete, p99 41ms, error rate 0.00%\"\n",
				"README.md": "api service. ./soak.sh submits a load soak to the remote staging cluster and returns at once;\n" +
					"the soak runs about 6 hours. A p99 above 200ms or an error rate above 0.5% means a regression.\n",
			},
			ask:       "Kick off the staging soak with ./soak.sh and let me know how it goes.",
			wantWatch: true,
		},
		{
			// The user asks for no follow-up; the agent should still not
			// leave the soak unobserved.
			name: "remote-soak-no-followup-asked",
			files: map[string]string{
				"soak.sh": "#!/bin/sh\necho 'Submitted soak soak-4821 to the staging cluster. It runs remotely for about 6 hours.'\n" +
					"echo 'Check progress with ./soak-status.sh soak-4821'\n",
				"soak-status.sh": "#!/bin/sh\necho \"$1: running, 2% complete, p99 41ms, error rate 0.00%\"\n",
				"README.md": "api service. ./soak.sh submits a load soak to the remote staging cluster and returns at once;\n" +
					"the soak runs about 6 hours. A p99 above 200ms or an error rate above 0.5% means a regression.\n",
			},
			ask:       "Start the staging soak for the release candidate with ./soak.sh.",
			wantWatch: true,
		},
		{
			// Control: the work finishes inside the turn.
			name: "check-completes-in-turn",
			files: map[string]string{
				"check.sh":  "#!/bin/sh\nsleep 2\necho 'all 42 checks passed'\n",
				"README.md": "api service. ./check.sh runs the local checks in a few seconds.\n",
			},
			ask: "Run ./check.sh and tell me whether it passes.",
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
						if err := os.WriteFile(filepath.Join(ws, name), []byte(content), 0o755); err != nil {
							t.Fatal(err)
						}
					}
					asker := &watchAsker{}
					var mu sync.Mutex
					var specs []WatchSpec
					d := &Deps{
						Workspace: ws,
						Docs:      docs.NewStore(ws),
						Emitter:   event.NewEmitter(event.NewStdoutRecorder(io.Discard), "coordinator"),
						Asker:     asker,
						Jobs:      jobs.NewRegistry(),
						ScheduleWatch: func(_ context.Context, spec WatchSpec) (string, error) {
							mu.Lock()
							defer mu.Unlock()
							specs = append(specs, spec)
							return "Watch " + strconv.Quote(spec.Name) + " created; its first run starts now. " +
								"Runs are separate sessions: do not also poll or sleep in this session.", nil
						},
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
					mu.Lock()
					defer mu.Unlock()
					for _, s := range specs {
						t.Logf("schedule_watch: name=%q interval=%s expires=%s probe=%q\nbrief: %s", s.Name, s.Interval, s.Expires.Format(time.RFC3339), s.Probe, s.Brief)
					}
					t.Logf("final reply:\n%s", res.Report)
					switch {
					case sc.wantWatch && len(specs) == 0:
						t.Errorf("no schedule_watch proposal for work left running (confirm prompts: %d)", len(asker.confirms))
					case sc.wantWatch && specs[0].Expires.IsZero():
						t.Errorf("schedule_watch proposal has no expiry")
					case !sc.wantWatch && len(asker.confirms) > 0:
						t.Errorf("proposed %d watch(es) for work that completed in the turn", len(asker.confirms))
					}
				})
			}
		}
	}
}
