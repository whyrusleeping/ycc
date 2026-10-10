package session

import (
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

func TestReopenKeepsWorkImplementation(t *testing.T) {
	for _, strategy := range []string{config.ImplementationDirect, config.ImplementationDelegate} {
		t.Run(strategy, func(t *testing.T) {
			ws := t.TempDir()
			if _, err := git.Open(ws); err != nil {
				t.Fatal(err)
			}
			id := "s_strategy"
			writeSession(t, ws, id, []event.Event{
				{Seq: 1, Type: event.SessionStarted, Data: map[string]any{"mode": "work", "workspace": ws, "work_implementation": strategy}},
				{Seq: 2, Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "done"}},
				{Seq: 3, Type: event.SessionIdle},
			})
			reg := testRegistry()
			other := config.ImplementationDirect
			if strategy == other {
				other = config.ImplementationDelegate
			}
			if err := reg.SetWorkImplementation(other); err != nil {
				t.Fatal(err)
			}
			m := NewManager(reg, ws)
			s, err := m.Reopen("", id)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Stop()
			if s.deps.WorkImplementation != strategy {
				t.Fatalf("reopen adopted changed config: got %s want %s", s.deps.WorkImplementation, strategy)
			}
			hasImplementer, hasReview := false, false
			for _, tool := range s.currentLoop().Tools.APIDefs() {
				hasImplementer = hasImplementer || tool.Function.Name == "spawn_implementer"
				hasReview = hasReview || tool.Function.Name == "spawn_reviewers"
			}
			if hasImplementer != (strategy == config.ImplementationDelegate) || !hasReview {
				t.Fatalf("wrong tool shape: implementer=%t review=%t", hasImplementer, hasReview)
			}
		})
	}
}

func TestLegacyWorkStrategyInference(t *testing.T) {
	calls := []event.Event{{Actor: "coordinator", Type: event.ToolCall, Data: map[string]any{"name": "spawn_implementer"}}}
	if got := replayWorkImplementation(calls, config.ImplementationDirect); got != config.ImplementationDelegate {
		t.Fatalf("lost legacy implementer tools: %s", got)
	}
	calls = append(calls, event.Event{Type: event.SessionReopened, Data: map[string]any{"work_implementation": config.ImplementationDirect}})
	if got := replayWorkImplementation(calls, config.ImplementationDelegate); got != config.ImplementationDirect {
		t.Fatalf("explicit recorded strategy did not win: %s", got)
	}
	if got := replayWorkImplementation(nil, config.ImplementationDirect); got != config.ImplementationDirect {
		t.Fatalf("legacy fallback = %s", got)
	}
}
