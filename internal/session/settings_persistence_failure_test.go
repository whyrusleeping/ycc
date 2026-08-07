package session

import (
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/event"
)

func TestSetRoleConfigFailureDoesNotPersistDefaults(t *testing.T) {
	s, _ := newTestSession(t)
	recorder := &sessionFailAtRecorder{failAt: event.RoleConfigChanged}
	s.emitter = event.NewEmitter(recorder, "coordinator")

	err := s.SetRoleConfig("b", "c", []string{"b", "c"})
	if err == nil || !strings.Contains(err.Error(), "session event log failed") {
		t.Fatalf("SetRoleConfig error = %v, want terminal log error", err)
	}
	if got := s.reg.CoordinatorName(); got != "a" {
		t.Fatalf("persisted coordinator = %q, want unchanged a", got)
	}
	if got := s.reg.ImplementerName(); got != "a" {
		t.Fatalf("persisted implementer = %q, want unchanged a", got)
	}
	if got := s.reg.ReviewerNames(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("persisted reviewers = %v, want unchanged [a]", got)
	}
}

func TestSetThinkingFailureDoesNotPersistDefault(t *testing.T) {
	s, _ := newTestSession(t)
	if level, ok := s.reg.RoleThinking(roleCoordinator); ok || level != "" {
		t.Fatalf("initial coordinator thinking = %q,%v, want unset", level, ok)
	}
	recorder := &sessionFailAtRecorder{failAt: event.ThinkingLevelChanged}
	s.emitter = event.NewEmitter(recorder, "coordinator")

	err := s.SetThinking(roleCoordinator, "low")
	if err == nil || !strings.Contains(err.Error(), "session event log failed") {
		t.Fatalf("SetThinking error = %v, want terminal log error", err)
	}
	if level, ok := s.reg.RoleThinking(roleCoordinator); ok || level != "" {
		t.Fatalf("persisted coordinator thinking = %q,%v, want unchanged", level, ok)
	}
}
