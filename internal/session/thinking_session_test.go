package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/project"
)

// thinkingTestManager returns a manager whose registry persists to a real
// ycc.toml, so any write a session-only change made would show up in the file.
// Model a offers the full scale; model o keeps the Ollama on/off default.
func thinkingTestManager(t *testing.T) (*Manager, string, []byte) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "ycc.toml")
	contents := []byte(`[models.a]
backend = "ollama"
base_url = "http://localhost:1"
model = "model-a"
effort = "medium"
thinking_levels = ["off", "low", "medium", "high", "xhigh", "max"]

[models.o]
backend = "ollama"
base_url = "http://localhost:2"
model = "model-o"

[roles]
coordinator = "a"
implementer = "a"
reviewers = ["a"]
`)
	if err := os.WriteFile(configPath, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	reg := config.NewRegistry(cfg)
	reg.SetPath(configPath)
	m := NewManager(reg, t.TempDir())
	m.SetProjects(project.NewMemory())
	t.Cleanup(m.ReclaimAll)
	return m, configPath, contents
}

func assertConfigUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("ycc.toml changed:\n%s", after)
	}
}

func reopenStopped(t *testing.T, m *Manager, id string) *Session {
	t.Helper()
	if err := m.Stop(id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	s, err := m.Reopen("", id)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	return s
}

// A start-time coordinator level runs the coordinator (and roles sharing its
// model) at that level, leaves ycc.toml alone, and is restored on reopen.
func TestStartCoordinatorThinkingIsSessionOnly(t *testing.T) {
	m, configPath, before := thinkingTestManager(t)
	s, err := m.Start(Config{Workspace: t.TempDir(), Mode: "chat", Prompt: "hi", CoordinatorThinking: "low"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s.loop.Effort != "low" || s.deps.Implementer.Effort != "low" {
		t.Fatalf("coordinator effort = %q implementer = %q, want low", s.loop.Effort, s.deps.Implementer.Effort)
	}
	started := waitForSessionEvent(t, s, event.SessionStarted)
	if got, _ := started.Data["coordinator_thinking"].(string); got != "low" {
		t.Fatalf("session_started coordinator_thinking = %q", got)
	}
	if got := m.reg.ModelThinkingLevel("a"); got != "medium" {
		t.Fatalf("saved level = %q, want medium", got)
	}
	assertConfigUnchanged(t, configPath, before)

	if _, _, _, ct, _, _ := s.RoleConfigSnapshot(); ct != "low" {
		t.Fatalf("session coordinator thinking = %q, want low", ct)
	}
	reopened := reopenStopped(t, m, s.ID)
	if reopened.loop.Effort != "low" || reopened.deps.Implementer.Effort != "low" {
		t.Fatalf("reopened effort = %q implementer = %q, want low", reopened.loop.Effort, reopened.deps.Implementer.Effort)
	}

	if _, err := m.Start(Config{Workspace: t.TempDir(), Mode: "chat", Prompt: "hi", CoordinatorThinking: "huge"}); !errors.Is(err, ErrUnknownThinkingLevel) {
		t.Fatalf("unknown level error = %v", err)
	}
}

// A start-time level the coordinator's model does not offer is clamped.
func TestStartCoordinatorThinkingClampsToModelLevels(t *testing.T) {
	m, _, _ := thinkingTestManager(t)
	s, err := m.Start(Config{Workspace: t.TempDir(), Mode: "chat", Prompt: "hi", CoordinatorModel: "o", CoordinatorThinking: "low"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s.coordinatorThinking != "high" || s.loop.Effort != "high" {
		t.Fatalf("clamped level = %q (loop %q), want high", s.coordinatorThinking, s.loop.Effort)
	}
}

// SetThinking on a live session changes only that session, survives reopen,
// and never writes ycc.toml.
func TestSessionSetThinkingIsSessionOnlyAndSurvivesReopen(t *testing.T) {
	m, configPath, before := thinkingTestManager(t)
	s, err := m.Start(Config{Workspace: t.TempDir(), Mode: "chat", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForSessionEvent(t, s, event.SessionStarted)
	if err := s.SetThinking(roleCoordinator, "xhigh"); err != nil {
		t.Fatalf("SetThinking: %v", err)
	}
	if s.loop.Effort != "xhigh" {
		t.Fatalf("live effort = %q, want xhigh", s.loop.Effort)
	}
	if got := m.reg.ModelThinkingLevel("a"); got != "medium" {
		t.Fatalf("saved level = %q, want medium", got)
	}
	assertConfigUnchanged(t, configPath, before)

	// The reopened instance replays the override from the durable log.
	reopened := reopenStopped(t, m, s.ID)
	if reopened.loop.Effort != "xhigh" {
		t.Fatalf("reopened effort = %q, want xhigh", reopened.loop.Effort)
	}
	if _, _, _, ct, _, _ := reopened.RoleConfigSnapshot(); ct != "xhigh" {
		t.Fatalf("reopened coordinator thinking = %q, want xhigh", ct)
	}
	// Another session still uses the saved level.
	other, err := m.Start(Config{Workspace: t.TempDir(), Mode: "chat", Prompt: "hi"})
	if err != nil {
		t.Fatalf("Start other: %v", err)
	}
	if other.loop.Effort != "medium" {
		t.Fatalf("other session effort = %q, want medium", other.loop.Effort)
	}
	assertConfigUnchanged(t, configPath, before)
}

// A watch's thinking level applies to its run sessions; empty uses the model
// default, and an unknown level is rejected when the watch is defined.
func TestWatchThinkingAppliesToRuns(t *testing.T) {
	m, _, _ := thinkingTestManager(t)
	def := WatchDefinition{Name: "live", Brief: "check", Interval: Duration(time.Hour), Thinking: "max"}
	if err := m.validateWatchDefinition(&def, true); err != nil {
		t.Fatalf("validate: %v", err)
	}
	run, err := m.startWatchRunSession(t.TempDir(), def, "")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if run.loop.Effort != "max" {
		t.Fatalf("watch run effort = %q, want max", run.loop.Effort)
	}
	def.Thinking = ""
	plain, err := m.startWatchRunSession(t.TempDir(), def, "")
	if err != nil {
		t.Fatalf("start plain run: %v", err)
	}
	if plain.loop.Effort != "medium" {
		t.Fatalf("default watch run effort = %q, want medium", plain.loop.Effort)
	}
	bad := WatchDefinition{Name: "bad", Brief: "check", Interval: Duration(time.Hour), Thinking: "huge"}
	if err := m.validateWatchDefinition(&bad, true); !errors.Is(err, ErrInvalidWatch) {
		t.Fatalf("unknown watch thinking error = %v", err)
	}
}
