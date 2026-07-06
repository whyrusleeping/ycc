package session

import (
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
)

// retryRegistry builds a registry whose config carries a [retry] block so the
// configured policy can be traced through newSession into deps + coordinator loop.
func retryRegistry() *config.Registry {
	cfg := &config.Config{
		Models: map[string]config.Model{
			"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"},
		},
		Roles: config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
		Retry: config.Retry{MaxAttempts: 1, BaseDelayMS: 100, MaxDelayMS: 5000},
	}
	return config.NewRegistry(cfg)
}

// newSession threads the registry's RetryPolicy onto orchestrator.Deps (subagent
// loops) and buildLoop puts it on the coordinator loop, so a configured [retry]
// block reaches both the coordinator and its subagents (task 0133).
func TestRetryPolicyPlumbedToSession(t *testing.T) {
	reg := retryRegistry()
	m := NewManager(reg, t.TempDir())
	ws := t.TempDir()

	log, err := event.OpenLog(t.TempDir() + "/events.jsonl")
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	s, err := m.newSession(ws, "s_retry", "work", "judgement", "go", log, false)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}

	want := engine.RetryPolicy{MaxAttempts: 1, BaseDelay: 100 * time.Millisecond, MaxDelay: 5 * time.Second}
	if s.deps.Retry != want {
		t.Fatalf("deps.Retry = %+v, want %+v", s.deps.Retry, want)
	}

	loop, err := s.buildLoop("work", "go")
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	if loop.Retry != want {
		t.Fatalf("coordinator loop.Retry = %+v, want %+v", loop.Retry, want)
	}
}
