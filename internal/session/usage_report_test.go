package session

import (
	"path/filepath"
	"testing"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/usage"
)

func TestUsageReportAggregatesAllRegisteredProjects(t *testing.T) {
	m := NewManager(testRegistry(), "")
	m.SetProjects(project.NewMemory())

	first := t.TempDir()
	second := t.TempDir()
	if _, err := m.AddProject(first, "first"); err != nil {
		t.Fatalf("AddProject(first): %v", err)
	}
	if _, err := m.AddProject(second, "second"); err != nil {
		t.Fatalf("AddProject(second): %v", err)
	}
	writeUsageLog(t, first, "sess_first", "0287", 100)
	writeUsageLog(t, second, "sess_second", "0290", 250)

	overall, err := m.UsageReport("", usage.Options{GroupBy: []usage.Dim{usage.DimTask}})
	if err != nil {
		t.Fatalf("UsageReport(overall): %v", err)
	}
	if overall.Workspace != "" {
		t.Fatalf("overall workspace = %q, want empty", overall.Workspace)
	}
	if overall.Total.Tokens.Total != 350 {
		t.Fatalf("overall total = %d, want 350", overall.Total.Tokens.Total)
	}
	gotTasks := make(map[string]int, len(overall.Rows))
	for _, row := range overall.Rows {
		gotTasks[row.Task] = row.Tokens.Total
	}
	if len(gotTasks) != 2 || gotTasks["0287"] != 100 || gotTasks["0290"] != 250 {
		t.Fatalf("overall rows = %+v, want task 0287=100 and 0290=250", overall.Rows)
	}

	named, err := m.UsageReport("first", usage.Options{GroupBy: []usage.Dim{usage.DimTask}})
	if err != nil {
		t.Fatalf("UsageReport(first): %v", err)
	}
	absFirst, err := filepath.Abs(first)
	if err != nil {
		t.Fatal(err)
	}
	if named.Workspace != absFirst {
		t.Fatalf("named workspace = %q, want %q", named.Workspace, absFirst)
	}
	if named.Total.Tokens.Total != 100 || len(named.Rows) != 1 || named.Rows[0].Task != "0287" {
		t.Fatalf("named report = %+v, want only task 0287 with 100 tokens", named)
	}
}

func writeUsageLog(t *testing.T, workspace, sessionID, task string, total int) {
	t.Helper()
	logPath := filepath.Join(workspace, ".ycc", "sessions", sessionID, "events.jsonl")
	log, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("OpenLog(%s): %v", sessionID, err)
	}
	log.Record("coordinator", event.TaskFocus, map[string]any{"task": task})
	log.Record("coordinator", event.ModelTurn, map[string]any{
		"model_name": "coordinator",
		"usage":      event.Usage{Input: total, Total: total},
	})
	if err := log.Close(); err != nil {
		t.Fatalf("Close(%s): %v", sessionID, err)
	}
}
