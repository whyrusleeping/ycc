package session

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
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

// thinkingForLevel maps levels to engine.Thinking; "off" disables reasoning and
// every effort level enables adaptive/summarized at that effort.
func TestThinkingForLevel(t *testing.T) {
	if th, ok := thinkingForLevel("off"); !ok || th != (engine.Thinking{}) {
		t.Fatalf("off => %+v ok=%v, want disabled", th, ok)
	}
	for _, lvl := range []string{"low", "medium", "high", "xhigh", "max"} {
		th, ok := thinkingForLevel(lvl)
		if !ok {
			t.Fatalf("%s reported invalid", lvl)
		}
		want := engine.Thinking{Thinking: "adaptive", Effort: lvl, ThinkingDisplay: "summarized"}
		if th != want {
			t.Fatalf("%s => %+v, want %+v", lvl, th, want)
		}
	}
	if _, ok := thinkingForLevel("bogus"); ok {
		t.Fatal("expected bogus level to be invalid")
	}
}

// SetThinking validates the level, updates the live coordinator loop's reasoning
// settings, rebuilds the implementer/reviewer specs with the override, and emits
// a log event. The override also wins over per-model config in thinkingFor.
func TestSetThinkingUpdatesLoopAndSpecs(t *testing.T) {
	s, buf := newTestSession(t)

	if err := s.SetThinking("low"); err != nil {
		t.Fatal(err)
	}

	// Live coordinator loop now reflects the override.
	if s.loop.Thinking != "adaptive" || s.loop.Effort != "low" || s.loop.ThinkingDisplay != "summarized" {
		t.Fatalf("coordinator loop thinking=%q effort=%q display=%q",
			s.loop.Thinking, s.loop.Effort, s.loop.ThinkingDisplay)
	}
	// Implementer + reviewer specs rebuilt with the override.
	if s.deps.Implementer.Effort != "low" || s.deps.Implementer.Thinking != "adaptive" {
		t.Fatalf("implementer spec not overridden: %+v", s.deps.Implementer)
	}
	if len(s.deps.Reviewers) != 1 || s.deps.Reviewers[0].Effort != "low" {
		t.Fatalf("reviewer specs not overridden: %+v", s.deps.Reviewers)
	}
	// thinkingFor honors the override regardless of model name.
	if th := s.thinkingFor("a"); th.Effort != "low" {
		t.Fatalf("thinkingFor override = %+v", th)
	}
	if !logHas(buf, string(event.ThinkingLevelChanged)) {
		t.Fatalf("no thinking_level_changed event:\n%s", buf.String())
	}

	// "off" disables reasoning across the live loop + specs.
	if err := s.SetThinking("off"); err != nil {
		t.Fatal(err)
	}
	if s.loop.Thinking != "" || s.loop.Effort != "" || s.loop.ThinkingDisplay != "" {
		t.Fatalf("off did not disable loop reasoning: %+v", s.loop)
	}
	if s.deps.Implementer.Thinking != "" || s.deps.Implementer.Effort != "" {
		t.Fatalf("off did not disable implementer spec: %+v", s.deps.Implementer)
	}

	// Unknown level rejected, override unchanged.
	if err := s.SetThinking("bogus"); err == nil {
		t.Fatal("expected error for unknown thinking level")
	}
}

// A mode switch after SetThinking must produce a NEW coordinator loop that honors
// the override (not the per-model config). buildLoop is the seam Manager.Start
// uses on mode transitions; we install the same closure here and drive it.
func TestSetThinkingHonoredByModeSwitchBuildLoop(t *testing.T) {
	s, _ := newTestSession(t)
	s.deps.Workspace = t.TempDir()
	s.deps.Docs = docs.NewStore(s.deps.Workspace)

	// Install a buildLoop equivalent to the production one (Manager.Start): it
	// resolves coordinator reasoning through s.thinkingFor, so a session-wide
	// override flows into the next mode's loop.
	s.buildLoop = func(mode, prompt string) (*engine.Loop, error) {
		reg, sys := orchestrator.BuildMode(mode, s.deps, s.inter.Level())
		s.mu.Lock()
		coord := s.coordinator
		s.mu.Unlock()
		client, model, err := s.reg.Build(coord)
		if err != nil {
			return nil, err
		}
		th := s.thinkingFor(coord)
		loop := &engine.Loop{
			Client: client, Model: model, System: sys, Tools: reg, Emitter: s.emitter,
			Thinking: th.Thinking, Effort: th.Effort, ThinkingDisplay: th.ThinkingDisplay,
		}
		loop.Seed(prompt)
		return loop, nil
	}

	// Sanity: with no override, the new loop uses the per-model config (default
	// high effort for the ollama test models).
	base, err := s.buildLoop("work", "go")
	if err != nil {
		t.Fatal(err)
	}
	if base.Effort != "high" {
		t.Fatalf("pre-override mode-switch loop effort = %q, want config default high", base.Effort)
	}

	// Set a non-default override, then drive a mode switch.
	if err := s.SetThinking("max"); err != nil {
		t.Fatal(err)
	}
	next, err := s.buildLoop("work", "go")
	if err != nil {
		t.Fatal(err)
	}
	if next.Thinking != "adaptive" || next.Effort != "max" || next.ThinkingDisplay != "summarized" {
		t.Fatalf("mode-switch loop did not honor override: thinking=%q effort=%q display=%q",
			next.Thinking, next.Effort, next.ThinkingDisplay)
	}
}
