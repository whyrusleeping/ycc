package session

import (
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
)

// RemoveModel must reject removing a model still referenced by a running
// session's LIVE role config (set via SetRoleConfig, stored on the Session
// rather than cfg.Roles), and allow removing an unreferenced one.
func TestRemoveModelRejectsLiveSessionRoleReference(t *testing.T) {
	s, _ := newTestSession(t)

	// Point every live role at model "b". The static cfg.Roles still reference
	// "a" only, so "b" is referenced solely by this running session.
	if err := s.SetRoleConfig("b", "b", []string{"b"}); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(s.reg, "")
	mgr.sessions[s.ID] = s

	// "b" is referenced by the live session (not by cfg.Roles) -> rejected.
	if err := mgr.RemoveModel("b", false); err == nil {
		t.Fatal("expected RemoveModel(b) to be rejected: referenced by running session")
	}

	// Add an unreferenced model "d" -> removing it succeeds.
	if err := s.reg.UpsertModel("d", config.Model{Backend: "ollama", BaseURL: "http://localhost:4", Model: "model-d"}, false); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RemoveModel("d", false); err != nil {
		t.Fatalf("RemoveModel(d) unreferenced should succeed: %v", err)
	}
}
