package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/project"
)

func presetTestManager(t *testing.T, binding string) (*Manager, string, []byte) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "ycc.toml")
	contents := []byte(`[models.a]
backend = "ollama"
base_url = "http://localhost:1"
model = "model-a"

[models.b]
backend = "ollama"
base_url = "http://localhost:2"
model = "model-b"

[models.c]
backend = "ollama"
base_url = "http://localhost:3"
model = "model-c"

[roles]
coordinator = "a"
implementer = "a"
reviewers = ["a"]

[roles.presets]
memory-groom = "` + binding + `"
`)
	if err := os.WriteFile(configPath, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	m := NewManager(config.NewRegistry(cfg), t.TempDir())
	m.SetProjects(project.NewMemory())
	return m, configPath, contents
}

func waitForSessionEvent(t *testing.T, s *Session, typ event.Type) event.Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range s.Log().Snapshot() {
			if ev.Type == typ {
				return ev
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; events=%+v", typ, s.Log().Snapshot())
	return event.Event{}
}

func TestStartBoundPresetUsesSessionOnlyCoordinatorAndReopenRestoresIt(t *testing.T) {
	m, configPath, before := presetTestManager(t, "b")
	ws := t.TempDir()
	s, err := m.Start(Config{Workspace: ws, Mode: "chat", Prompt: "hi", Preset: "memory-groom"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s.coordinator != "b" || s.loop.ModelName != "b" {
		t.Fatalf("preset coordinator = %q (loop %q), want b", s.coordinator, s.loop.ModelName)
	}
	started := waitForSessionEvent(t, s, event.SessionStarted)
	if got, _ := started.Data["preset"].(string); got != "memory-groom" {
		t.Fatalf("session_started preset = %q", got)
	}
	if got := m.reg.CoordinatorName(); got != "a" {
		t.Fatalf("persisted default changed in memory to %q", got)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("starting preset rewrote ycc.toml:\n%s", after)
	}

	id := s.ID
	if err := m.Stop(id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	reopened, err := m.Reopen("", id)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if reopened.coordinator != "b" || reopened.loop.ModelName != "b" {
		t.Fatalf("reopened preset coordinator = %q (loop %q), want b", reopened.coordinator, reopened.loop.ModelName)
	}

	// If the bound model is removed later, another reopen degrades safely to the
	// current default and records the same visible warning as a fresh start.
	if err := m.Stop(id); err != nil {
		t.Fatalf("Stop reopened session: %v", err)
	}
	if err := m.RemoveModel("b", false); err != nil {
		t.Fatalf("Remove bound model: %v", err)
	}
	fallback, err := m.Reopen("", id)
	if err != nil {
		t.Fatalf("Reopen after model removal: %v", err)
	}
	defer m.Stop(id)
	if fallback.coordinator != "a" || fallback.loop.ModelName != "a" {
		t.Fatalf("removed-model reopen coordinator = %q (loop %q), want a", fallback.coordinator, fallback.loop.ModelName)
	}
	waitForSessionEvent(t, fallback, event.SessionNotice)
}

func TestReopenPresetPreservesMidSessionCoordinatorChange(t *testing.T) {
	m, _, _ := presetTestManager(t, "b")
	s, err := m.Start(Config{Workspace: t.TempDir(), Mode: "chat", Prompt: "hi", Preset: "memory-groom"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForSessionEvent(t, s, event.SessionStarted)
	if err := s.SetRoleConfig("c", "", nil); err != nil {
		t.Fatalf("SetRoleConfig coordinator: %v", err)
	}
	waitForSessionEvent(t, s, event.RoleConfigChanged)
	if s.coordinator != "c" {
		t.Fatalf("changed live coordinator = %q, want c", s.coordinator)
	}

	id := s.ID
	if err := m.Stop(id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	reopened, err := m.Reopen("", id)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer m.Stop(id)
	if reopened.coordinator != "c" || reopened.loop.ModelName != "c" {
		t.Fatalf("reopened coordinator = %q (loop %q), want changed model c rather than preset model b", reopened.coordinator, reopened.loop.ModelName)
	}
}

func TestStartUnknownPresetModelFallsBackWithVisibleWarning(t *testing.T) {
	m, _, _ := presetTestManager(t, "removed-model")
	s, err := m.Start(Config{Workspace: t.TempDir(), Mode: "chat", Prompt: "hi", Preset: "memory-groom"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(s.ID)
	if s.coordinator != "a" || s.loop.ModelName != "a" {
		t.Fatalf("fallback coordinator = %q (loop %q), want a", s.coordinator, s.loop.ModelName)
	}
	warning := waitForSessionEvent(t, s, event.SessionNotice)
	msg, _ := warning.Data["msg"].(string)
	for _, want := range []string{"memory-groom", "removed-model", `default coordinator "a"`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("warning %q does not contain %q", msg, want)
		}
	}
	if warning.Data["level"] != "warning" {
		t.Fatalf("notice level = %v, want warning", warning.Data["level"])
	}
}
