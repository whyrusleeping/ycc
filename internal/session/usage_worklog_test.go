package session

import (
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// pricedRegistry builds a registry whose model "a" is priced so summarizeUsage
// produces a dollar figure.
func pricedRegistry() *config.Registry {
	in, out := 3.0, 15.0
	cfg := &config.Config{
		Models: map[string]config.Model{
			"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a", PriceInput: &in, PriceOutput: &out},
		},
		Roles: config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	}
	return config.NewRegistry(cfg)
}

// summarizeUsage appends exactly one usage line to the focused task's work log,
// even across repeated idle cycles (spec §6.2, §20.5).
func TestSummarizeUsageAppendsOnceToWorkLog(t *testing.T) {
	ws := t.TempDir()
	store := docs.NewStore(ws)
	task, err := store.Create("test task", "", 1, nil, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	logPath := ws + "/.ycc/sessions/s_test/events.jsonl"
	log, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer log.Close()
	emitter := event.NewEmitter(log, "coordinator")
	emitter.Emit(event.TaskFocus, map[string]any{"task": task.ID})
	emitter.Emit(event.ModelTurn, map[string]any{
		"model_name": "a",
		"usage":      event.Usage{Input: 1000, Output: 100, Total: 1100},
	})

	s := &Session{
		ID:              "s_test",
		Mode:            "work",
		log:             log,
		emitter:         emitter,
		reg:             pricedRegistry(),
		deps:            &orchestrator.Deps{Docs: store, Emitter: emitter},
		usageSummarized: map[string]bool{},
	}

	s.summarizeUsage()
	s.summarizeUsage() // second idle cycle must not append again

	got, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	n := strings.Count(got.Body, "usage: ")
	if n != 1 {
		t.Fatalf("usage lines = %d, want exactly 1\nbody:\n%s", n, got.Body)
	}
	if !strings.Contains(got.Body, "$") {
		t.Fatalf("priced summary should contain a dollar cost:\n%s", got.Body)
	}
}
