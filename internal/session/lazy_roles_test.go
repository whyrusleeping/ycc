package session

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

func TestUnavailableReviewDoesNotSilentlyBecomeSelfReview(t *testing.T) {
	s, _ := newTestSession(t)
	model, _ := s.reg.GetModel("a")
	model.Disabled = true
	if err := s.reg.UpsertModel("a", model, false); err != nil {
		t.Fatal(err)
	}
	plan := s.resolveReviewTier("")
	if plan.Err == nil || plan.SelfReview || len(plan.Specs) != 0 {
		t.Fatalf("unavailable independent review failed open: %+v", plan)
	}
	if explicit := s.resolveReviewTier("self-review"); explicit.Err != nil || !explicit.SelfReview {
		t.Fatalf("explicit self-review was lost: %+v", explicit)
	}
}

func TestSessionStartupDefersUnusedRoleAuthentication(t *testing.T) {
	// Keep this independent of the operator's subscription credentials.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{
			"coord":  {Backend: "ollama", BaseURL: "http://localhost:1", Model: "coordinator"},
			"unused": {Backend: "anthropic", Auth: "oauth", Model: "review-model"},
		},
		Roles: config.Roles{Coordinator: "coord", Implementer: "unused", Reviewers: []string{"unused"}},
	})
	if _, _, err := reg.Build("unused"); err == nil {
		t.Fatal("fixture unexpectedly has usable subscription credentials")
	}
	for _, mode := range []string{"chat", "pm", "work"} {
		t.Run(mode, func(t *testing.T) {
			ws := t.TempDir()
			log, err := event.OpenLog(filepath.Join(ws, ".ycc", "sessions", "s_lazy", "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			m := NewManager(reg, ws)
			s, err := m.newSession(ws, "s_lazy", mode, false, "test", log, false, "")
			if err != nil {
				t.Fatalf("unused role blocked session assembly: %v", err)
			}
			defer s.cancel()
			if _, err := s.buildLoop(mode, "test"); err != nil {
				t.Fatalf("unused role blocked coordinator loop: %v", err)
			}
			plan := s.resolveReviewTier("")
			if plan.SelfReview || len(plan.Specs) != 1 || plan.Specs[0].Name != "unused" {
				t.Fatalf("unavailable reviewer silently became self-review: %+v", plan)
			}
			for _, spec := range []orchestrator.AgentSpec{s.deps.Implementer, plan.Specs[0]} {
				client := spec.NewClient()
				if client == nil {
					t.Fatalf("%s factory returned nil", spec.Name)
				}
				if _, err := client.TurnCtx(context.Background(), gollama.RequestOptions{}); err == nil {
					t.Fatalf("invoked %s did not report unavailable credentials", spec.Name)
				}
			}
		})
	}
}
