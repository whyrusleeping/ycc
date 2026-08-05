package session

import (
	"errors"
	"testing"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/project"
)

// A per-session coordinator override (StartSession's coordinator_model, picked
// e.g. from the iOS composer) selects the model this session's coordinator runs
// on WITHOUT touching the persisted role defaults or the other roles.
func TestNewSessionCoordinatorOverride(t *testing.T) {
	reg := testRegistry() // models a/b/c, all roles = a
	m := NewManager(reg, t.TempDir())
	log, err := event.OpenLog(t.TempDir() + "/events.jsonl")
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	defer log.Close()

	s, err := m.newSession(t.TempDir(), "s_over", "chat", false, "hi", log, false, "b")
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	if s.coordinator != "b" {
		t.Fatalf("coordinator = %q, want b", s.coordinator)
	}
	// Other roles keep the configured defaults.
	if s.implementer != "a" || len(s.reviewers) != 1 || s.reviewers[0] != "a" {
		t.Fatalf("roles drifted: impl=%q reviewers=%v", s.implementer, s.reviewers)
	}
	// The coordinator loop really runs on the override's backend model.
	loop, err := s.buildLoop("chat", "hi")
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	if loop.Model != "model-b" {
		t.Fatalf("coordinator loop model = %q, want model-b", loop.Model)
	}
	// The persisted default is untouched: the next session still gets "a".
	if got := reg.CoordinatorName(); got != "a" {
		t.Fatalf("default coordinator = %q, want a (unchanged)", got)
	}
}

// An unknown override name is a client-input error (ErrUnknownModel → the RPC
// layer maps it to InvalidArgument) and is rejected before any session state,
// log, or project registration is created.
func TestStartRejectsUnknownCoordinatorModel(t *testing.T) {
	m := NewManager(testRegistry(), t.TempDir())
	m.SetProjects(project.NewMemory())
	ws := t.TempDir()
	if _, err := m.AddProject(ws, "demo"); err != nil {
		t.Fatal(err)
	}

	_, err := m.Start(Config{Project: "demo", Prompt: "hi", CoordinatorModel: "nope"})
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("Start with unknown model err = %v, want ErrUnknownModel", err)
	}
	if got := m.List(); len(got) != 0 {
		t.Fatalf("sessions after rejected start = %+v, want none", got)
	}
}

// newSession also guards the override (defence in depth for the Reopen path).
func TestNewSessionRejectsUnknownCoordinatorModel(t *testing.T) {
	m := NewManager(testRegistry(), t.TempDir())
	log, err := event.OpenLog(t.TempDir() + "/events.jsonl")
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	defer log.Close()

	if _, err := m.newSession(t.TempDir(), "s_bad", "chat", false, "hi", log, false, "nope"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("newSession err = %v, want ErrUnknownModel", err)
	}
}
