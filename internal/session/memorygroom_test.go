package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// overBudgetMemory writes a memory.md whose active prompt rendering is over the
// soft budget but under the hard backstop.
func overBudgetMemory(t *testing.T, ws string, lines int) docs.MemoryStatus {
	t.Helper()
	body := "# Project memory\n\n## Lessons learned\n" + strings.Repeat("- 2020-01-01: filler line\n", lines)
	for i := 0; i < lines; i++ {
		// Distinct lines keep legacy ids distinct.
		body += "- distinct note " + strconv.Itoa(i) + "\n"
	}
	if err := os.WriteFile(filepath.Join(ws, "memory.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	st := docs.MemoryStatusOf(body)
	if !st.OverSoftBudget() || st.ActiveBytes >= docs.MemoryHardBudget {
		t.Fatalf("fixture active size %d not in [soft, hard)", st.ActiveBytes)
	}
	return st
}

func waitNoGroom(t *testing.T, m *Manager, primary string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.groomMu.Lock()
		_, running := m.grooms[primary]
		m.groomMu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("automatic groom did not finish")
}

func TestMemoryPressureSchedulesOneAutomaticGroom(t *testing.T) {
	m, _, _ := presetTestManager(t, "b")
	// Create the workspace before registering ReclaimAll: cleanups run LIFO, so
	// sessions and the watcher are torn down before the directory is removed.
	ws, _ := filepath.Abs(t.TempDir())
	t.Cleanup(m.ReclaimAll)
	status := overBudgetMemory(t, ws, 60)

	// Starting any session in an over-budget project schedules a groom.
	chat, err := m.Start(Config{Workspace: ws, Mode: "chat", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := m.MemoryGroomInfo(filepath.Base(ws))
	if err != nil {
		t.Fatal(err)
	}
	groomID := info.RunningSession
	if groomID == "" || groomID == chat.ID || !info.Enabled {
		t.Fatalf("expected a running automatic groom, got %+v", info)
	}
	groom, ok := m.Get(groomID)
	if !ok {
		t.Fatal("groom session is not live")
	}
	// Not a cheaper model: the memory-groom preset binding (b) wins, the mode is
	// unattended pm, and the prompt carries the measured size.
	if groom.Mode != "pm" || !groom.inter.unattended || groom.preset != memoryGroomPreset || groom.coordinator != "b" {
		t.Fatalf("groom session = mode %q unattended %v preset %q coordinator %q", groom.Mode, groom.inter.unattended, groom.preset, groom.coordinator)
	}
	if !strings.Contains(groom.prompt, "AUTOMATIC MEMORY-GROOM") || !strings.Contains(groom.prompt, strconv.Itoa(status.ActiveBytes)) {
		t.Fatalf("groom prompt = %q", groom.prompt)
	}
	if st := loadMemoryGroomState(ws); st.LastSession != groomID || st.ActiveBefore != status.ActiveBytes {
		t.Fatalf("persisted state = %+v", st)
	}

	// Other writers are told it is handled; the groom itself gets "".
	if msg := m.memoryPressure(ws, chat.ID); !strings.Contains(msg, groomID) || !strings.Contains(msg, "no action needed") {
		t.Fatalf("pressure status for other session = %q", msg)
	}
	if msg := m.memoryPressure(ws, groomID); msg != "" {
		t.Fatalf("groom's own pressure status = %q, want empty", msg)
	}
	if n := len(m.ListByProject(filepath.Base(ws))); n != 2 {
		t.Fatalf("expected exactly the chat + one groom session, got %d", n)
	}

	// When it ends, the watcher reclaims it, records the outcome and measurement,
	// and clears the in-flight marker.
	if err := m.Stop(groomID); err != nil {
		t.Fatal(err)
	}
	waitNoGroom(t, m, ws)
	st := loadMemoryGroomState(ws)
	if st.LastFinished.IsZero() || st.Outcome == "" || st.ActiveAfter != status.ActiveBytes {
		t.Fatalf("finished state = %+v", st)
	}

	// Cooldown: a later over-budget write does not start another groom.
	if msg := m.memoryPressure(ws, chat.ID); !strings.Contains(msg, "ran recently") {
		t.Fatalf("cooldown status = %q", msg)
	}
	if info, _ := m.MemoryGroomInfo(filepath.Base(ws)); info.RunningSession != "" {
		t.Fatalf("cooldown started another groom: %+v", info)
	}

	// Past the cooldown, a groom that did not finish (error/stop/outage) sets no
	// regrowth baseline — otherwise one outage could block grooming forever.
	st.LastStarted = time.Now().Add(-2 * memoryGroomCooldown)
	saveMemoryGroomState(ws, st)
	var fakeStarts int
	m.startMemoryGroom = func(string, docs.MemoryStatus) (*Session, error) {
		fakeStarts++
		return nil, os.ErrInvalid
	}
	if msg := m.memoryPressure(ws, chat.ID); fakeStarts != 1 || strings.Contains(msg, "has not grown") {
		t.Fatalf("stopped groom should not gate regrowth: starts=%d %q", fakeStarts, msg)
	}
	m.startMemoryGroom = nil

	// A finished groom's result does gate: no regrowth since, no groom.
	st.Outcome = "finished"
	saveMemoryGroomState(ws, st)
	if msg := m.memoryPressure(ws, chat.ID); !strings.Contains(msg, "has not grown") {
		t.Fatalf("regrowth status = %q", msg)
	}
	// After enough growth, grooming resumes.
	overBudgetMemory(t, ws, 90)
	if msg := m.memoryPressure(ws, chat.ID); !strings.Contains(msg, "started an automatic memory-groom") {
		t.Fatalf("regrown memory should start a groom: %q", msg)
	}
}

func TestMemoryPressureIgnoresSmallMemoryAndHonoursKillSwitch(t *testing.T) {
	m, _, _ := presetTestManager(t, "b")
	ws, _ := filepath.Abs(t.TempDir())
	t.Cleanup(m.ReclaimAll)
	var started int
	m.startMemoryGroom = func(string, docs.MemoryStatus) (*Session, error) {
		started++
		return nil, os.ErrInvalid
	}
	if err := os.WriteFile(filepath.Join(ws, "memory.md"), []byte("# Project memory\n\n## Lessons learned\n- small\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := m.memoryPressure(ws, "s_x"); msg != "" || started != 0 {
		t.Fatalf("under-budget memory must not groom: %q (%d)", msg, started)
	}

	off := false
	cfg := &config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
		Memory: config.Memory{AutoGroom: &off},
	}
	disabled := NewManager(config.NewRegistry(cfg), "")
	disabled.SetProjects(project.NewMemory())
	t.Cleanup(disabled.ReclaimAll)
	disabled.startMemoryGroom = m.startMemoryGroom
	overBudgetMemory(t, ws, 60)
	if msg := disabled.memoryPressure(ws, "s_x"); msg != "" || started != 0 {
		t.Fatalf("auto_groom=false must not groom: %q (%d)", msg, started)
	}
	// A failed launch reports nothing and leaves no in-flight marker.
	if msg := m.memoryPressure(ws, "s_x"); msg != "" || started != 1 || len(m.grooms) != 0 {
		t.Fatalf("failed launch: %q started=%d grooms=%v", msg, started, m.grooms)
	}

	// A linked worktree (not a primary tree) is never auto-groomed, even when its
	// own copy and its primary are both over budget.
	proj, err := m.projects.EnsureWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	m.worktreesRoot = t.TempDir()
	wt := filepath.Join(m.worktreesRoot, workstream.SafeProjectDir(proj.Name), "ws-1")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	overBudgetMemory(t, wt, 60)
	if m.primaryTreeFor(wt) != ws {
		t.Fatalf("fixture worktree does not resolve to its primary: %q", m.primaryTreeFor(wt))
	}
	if msg := m.memoryPressure(wt, "s_x"); msg != "" || started != 1 {
		t.Fatalf("worktree pressure should not groom: %q started=%d", msg, started)
	}
}

// A preset session started by name with no prompt gets the preset's opening
// prompt (the iOS one-tap "Groom now" relies on this).
func TestStartPresetWithoutPromptUsesPresetPrompt(t *testing.T) {
	m, _, _ := presetTestManager(t, "b")
	ws := t.TempDir()
	t.Cleanup(m.ReclaimAll)
	s, err := m.Start(Config{Workspace: ws, Mode: "pm", Preset: memoryGroomPreset})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.prompt, "MEMORY-GROOM flow") {
		t.Fatalf("prompt = %q", s.prompt)
	}
}
