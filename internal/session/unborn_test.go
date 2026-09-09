package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

func TestChatStartsInUnbornRepositoryAndOwnershipToolsRefuse(t *testing.T) {
	ws := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = ws
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(ws, "user.txt"), []byte("uncommitted user work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(testRegistry(), t.TempDir())
	defer m.ReclaimAll()
	s, err := m.Start(Config{Workspace: ws, Mode: "chat", Prompt: "hello"})
	if err != nil {
		t.Fatalf("chat Start in unborn repository: %v", err)
	}
	defer s.Stop()
	if s.deps.Baseline != nil || s.deps.BaselineErr == nil || !strings.Contains(s.deps.BaselineErr.Error(), "review and commit are disabled") {
		t.Fatalf("baseline state = baseline=%v err=%v", s.deps.Baseline, s.deps.BaselineErr)
	}

	task, err := s.deps.Docs.Create("unborn refusal", "## Description\n\nTest ownership refusal.\n", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := orchestrator.BuildMode("work", s.deps, false)
	for _, tc := range []struct {
		name string
		args string
	}{
		{name: "spawn_reviewers", args: fmt.Sprintf(`{"task_id":%q}`, task.ID)},
		{name: "commit", args: fmt.Sprintf(`{"task_id":%q,"message":"test","outcome":"test"}`, task.ID)},
	} {
		res := reg.Dispatch(context.Background(), gollama.ToolCall{
			ID: "test", Type: "function",
			Function: gollama.ToolCallFunction{Name: tc.name, Arguments: tc.args},
		})
		if !res.IsError || !strings.Contains(res.Content, "review and commit are disabled") {
			t.Fatalf("%s result = %+v, want actionable baseline refusal", tc.name, res)
		}
	}
}
