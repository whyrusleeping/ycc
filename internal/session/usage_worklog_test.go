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

// summarizeUsage records a usage line for every task worked in the session, not
// just the currently-focused one (spec §6.2, §20.5).
func TestSummarizeUsageMultipleTasks(t *testing.T) {
	ws := t.TempDir()
	store := docs.NewStore(ws)
	taskA, err := store.Create("task A", "", 1, nil, nil)
	if err != nil {
		t.Fatalf("create task A: %v", err)
	}
	taskB, err := store.Create("task B", "", 1, nil, nil)
	if err != nil {
		t.Fatalf("create task B: %v", err)
	}

	logPath := ws + "/.ycc/sessions/s_test/events.jsonl"
	log, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer log.Close()
	emitter := event.NewEmitter(log, "coordinator")
	emitter.Emit(event.TaskFocus, map[string]any{"task": taskA.ID})
	emitter.Emit(event.ModelTurn, map[string]any{
		"model_name": "a",
		"usage":      event.Usage{Input: 1000, Output: 100, Total: 1100},
	})
	emitter.Emit(event.TaskFocus, map[string]any{"task": taskB.ID})
	emitter.Emit(event.ModelTurn, map[string]any{
		"model_name": "a",
		"usage":      event.Usage{Input: 2000, Output: 200, Total: 2200},
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

	for _, task := range []string{taskA.ID, taskB.ID} {
		got, err := store.Get(task)
		if err != nil {
			t.Fatalf("get task %s: %v", task, err)
		}
		n := strings.Count(got.Body, "usage: ")
		if n != 1 {
			t.Fatalf("task %s usage lines = %d, want exactly 1\nbody:\n%s", task, n, got.Body)
		}
		if !strings.Contains(got.Body, "$") {
			t.Fatalf("task %s priced summary should contain a dollar cost:\n%s", task, got.Body)
		}
	}
}

// summarizeUsage breaks the per-task usage line down by agent role, listing
// reviewers individually by name (spec §6.2, §20.5; task 0089).
func TestSummarizeUsageRoleBreakdown(t *testing.T) {
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

	coordinator := event.NewEmitter(log, "coordinator")
	implementer := event.NewEmitter(log, "implementer")
	reviewerGPT := event.NewEmitter(log, "reviewer:gpt")
	reviewerClaude := event.NewEmitter(log, "reviewer:claude")

	coordinator.Emit(event.TaskFocus, map[string]any{"task": task.ID})
	coordinator.Emit(event.ModelTurn, map[string]any{
		"model_name": "a", "usage": event.Usage{Input: 100, Output: 10, Total: 110},
	})
	implementer.Emit(event.ModelTurn, map[string]any{
		"model_name": "a", "usage": event.Usage{Input: 2000, Output: 200, Total: 2200},
	})
	reviewerGPT.Emit(event.ModelTurn, map[string]any{
		"model_name": "a", "usage": event.Usage{Input: 500, Output: 50, Total: 550},
	})
	reviewerClaude.Emit(event.ModelTurn, map[string]any{
		"model_name": "a", "usage": event.Usage{Input: 300, Output: 30, Total: 330},
	})

	s := &Session{
		ID:              "s_test",
		Mode:            "work",
		log:             log,
		emitter:         coordinator,
		reg:             pricedRegistry(),
		deps:            &orchestrator.Deps{Docs: store, Emitter: coordinator},
		usageSummarized: map[string]bool{},
	}

	s.summarizeUsage()

	got, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if n := strings.Count(got.Body, "usage: "); n != 1 {
		t.Fatalf("usage lines = %d, want exactly 1\nbody:\n%s", n, got.Body)
	}
	for _, role := range []string{"coordinator", "implementer", "reviewer:gpt", "reviewer:claude"} {
		if !strings.Contains(got.Body, role) {
			t.Fatalf("breakdown missing role %q:\n%s", role, got.Body)
		}
	}
}
