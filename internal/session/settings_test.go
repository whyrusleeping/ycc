package session

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// fakeTurner is a no-op backend (unused placeholder kept minimal).
type fakeTurner struct{}

func (fakeTurner) Turn(gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return &gollama.ResponseMessageGenerate{}, nil
}

func testRegistry() *config.Registry {
	cfg := &config.Config{
		Models: map[string]config.Model{
			"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"},
			"b": {Backend: "ollama", BaseURL: "http://localhost:2", Model: "model-b"},
			"c": {Backend: "ollama", BaseURL: "http://localhost:3", Model: "model-c"},
		},
		Roles: config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	}
	return config.NewRegistry(cfg)
}

// newTestSession builds a Session WITHOUT launching run(), so SetInteractionLevel
// and SetRoleConfig can be exercised deterministically against an in-memory log.
func newTestSession(t *testing.T) (*Session, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	rec := event.NewStdoutRecorder(&buf)
	emitter := event.NewEmitter(rec, "coordinator")
	inter := newInteraction("judgement", emitter)
	reg := testRegistry()
	client, model, err := reg.Build("a")
	if err != nil {
		t.Fatal(err)
	}
	deps := &orchestrator.Deps{Emitter: emitter, Asker: inter}
	s := &Session{
		ID:               "s_test",
		InteractionLevel: "judgement",
		emitter:          emitter,
		inter:            inter,
		deps:             deps,
		reg:              reg,
		loop:             &engine.Loop{Client: client, Model: model, Emitter: emitter},
		coordinator:      "a",
		implementer:      "a",
		reviewers:        []string{"a"},
		status:           event.StatusRunning,
	}
	return s, &buf
}

func logHas(buf *bytes.Buffer, typ string) bool {
	return strings.Contains(buf.String(), typ)
}

// SetInteractionLevel updates the live policy and emits a log event; the change
// is observed by the NEXT gate (Ask).
func TestSetInteractionLevelTakesEffectAtNextGate(t *testing.T) {
	s, buf := newTestSession(t)

	// A blocking gate would be judgement; switch to autonomous so the next Ask
	// returns immediately without a human.
	if err := s.SetInteractionLevel("autonomous"); err != nil {
		t.Fatal(err)
	}
	if s.inter.Level() != "autonomous" {
		t.Fatalf("level = %q", s.inter.Level())
	}
	if !logHas(buf, string(event.InteractionLevelChanged)) {
		t.Fatalf("no interaction_level_changed event:\n%s", buf.String())
	}

	// Next gate: autonomous mode does not block.
	ans, err := s.inter.Ask(context.Background(), "proceed?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(ans), "autonomous") {
		t.Fatalf("answer = %q", ans)
	}

	if err := s.SetInteractionLevel("bogus"); err == nil {
		t.Fatal("expected error for unknown level")
	}
}

// SetRoleConfig validates names, rebuilds the coordinator loop backend and the
// implementer/reviewer specs, and emits a log event.
func TestSetRoleConfigRebuildsClients(t *testing.T) {
	s, buf := newTestSession(t)

	if err := s.SetRoleConfig("b", "c", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}

	// Coordinator loop swapped to model-b.
	if s.loop.Model != "model-b" {
		t.Fatalf("coordinator model = %q, want model-b", s.loop.Model)
	}
	// Implementer spec rebuilt to model-c.
	if got := s.deps.Implementer.Model; got != "model-c" {
		t.Fatalf("implementer model = %q, want model-c", got)
	}
	// Reviewers rebuilt to two specs (multi-select).
	if len(s.deps.Reviewers) != 2 || s.deps.Reviewers[0].Model != "model-a" || s.deps.Reviewers[1].Model != "model-b" {
		t.Fatalf("reviewers = %+v", s.deps.Reviewers)
	}
	if !logHas(buf, string(event.RoleConfigChanged)) {
		t.Fatalf("no role_config_changed event:\n%s", buf.String())
	}

	// Empty fields leave roles unchanged.
	if err := s.SetRoleConfig("", "", nil); err != nil {
		t.Fatal(err)
	}
	if s.coordinator != "b" || s.implementer != "c" {
		t.Fatalf("unchanged roles drifted: coord=%s impl=%s", s.coordinator, s.implementer)
	}

	// Unknown model rejected.
	if err := s.SetRoleConfig("nope", "", nil); err == nil {
		t.Fatal("expected error for unknown coordinator model")
	}
	// The NewClient closure on a rebuilt spec yields a working client.
	if c := s.deps.Reviewers[0].NewClient(); c == nil {
		t.Fatal("reviewer NewClient returned nil")
	}
	_ = fakeTurner{}
}
