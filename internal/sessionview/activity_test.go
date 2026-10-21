package sessionview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
)

func TestBuildActivityTree(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	seq := 0
	ev := func(actor string, typ event.Type, data map[string]any) event.Event {
		seq++
		return event.Event{Seq: seq, TS: base.Add(time.Duration(seq) * time.Second), Actor: actor, Type: typ, Data: data}
	}
	events := []event.Event{
		ev("coordinator", event.SessionStarted, nil),
		ev("coordinator", event.JobStarted, map[string]any{"id": "job_1", "kind": "agent", "label": "agent_1 turn 1 (opus)"}),
		ev("coordinator", event.SubagentSpawned, map[string]any{"role": "generic", "agent_id": "agent_1", "job_id": "job_1", "model": "m", "logical_model": "opus", "round": 1}),
		ev("agent:agent_1", event.ModelTurn, map[string]any{"usage": map[string]any{"total": 100.0}}),
		ev("agent:agent_1", event.ToolCall, map[string]any{"name": "Bash"}),
		ev("coordinator", event.JobFinished, map[string]any{"id": "job_1", "kind": "agent", "status": "killed", "tail": "killed"}),
		ev("coordinator", event.SubagentFinished, map[string]any{"role": "generic", "agent_id": "agent_1", "job_id": "job_1"}),
		// A background implementer whose job fails after the loop reported success.
		ev("coordinator", event.JobStarted, map[string]any{"id": "job_2", "kind": "agent", "label": "implementer 0001"}),
		ev("coordinator", event.SubagentSpawned, map[string]any{"role": "implementer", "model": "sonnet", "round": 1, "job_id": "job_2"}),
		ev("implementer", event.JobStarted, map[string]any{"id": "job_3", "kind": "bash", "label": "go test ./..."}),
		ev("implementer", event.JobFinished, map[string]any{"id": "job_3", "kind": "bash", "status": "failed", "tail": "FAIL"}),
		ev("coordinator", event.SubagentFinished, map[string]any{"role": "implementer", "job_id": "job_2"}),
		ev("coordinator", event.JobFinished, map[string]any{"id": "job_2", "kind": "agent", "status": "failed", "tail": "no progress"}),
		// A foreground revision round, then a context-recovery respawn of it.
		ev("coordinator", event.SubagentSpawned, map[string]any{"role": "implementer", "model": "sonnet", "round": 2}),
		ev("coordinator", event.SubagentSpawned, map[string]any{"role": "implementer", "model": "sonnet", "round": 2}),
		// A background reviewer fan-out shares one job.
		ev("coordinator", event.JobStarted, map[string]any{"id": "job_4", "kind": "agent", "label": "reviewers 0001"}),
		ev("coordinator", event.SubagentSpawned, map[string]any{"role": "reviewer", "model": "A", "round": 1, "job_id": "job_4"}),
		ev("coordinator", event.SubagentSpawned, map[string]any{"role": "reviewer", "model": "B", "round": 1, "job_id": "job_4"}),
		ev("coordinator", event.SubagentFinished, map[string]any{"role": "reviewer", "model": "A", "job_id": "job_4"}),
		ev("coordinator", event.JobFinished, map[string]any{"id": "job_4", "kind": "agent", "status": "killed", "tail": "both"}),
		ev("coordinator", event.JobStarted, map[string]any{"id": "job_5", "kind": "bash", "label": "watch"}),
		ev("coordinator", event.SessionReopened, nil),
	}
	// Go through the index so progress aggregation and compaction are covered.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	var raw []byte
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(append(raw, line...), '\n')
	}
	if err := os.WriteFile(logPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(dir, "view.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, indexed, progress, err := store.Activity(context.Background(), "s", logPath, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]ActivityNode)
	nodes := BuildActivity(indexed, progress)
	for _, n := range nodes {
		byID[n.ID] = n
	}
	if len(nodes) != 7 || nodes[0].ID != "coordinator" {
		t.Fatalf("nodes = %+v", nodes)
	}
	for _, id := range []string{"job_1", "job_2", "job_4"} {
		if _, ok := byID[id]; ok {
			t.Fatalf("agent job %s should be presented by its subagent actors, not as its own node", id)
		}
	}
	agent := byID["agent:agent_1"]
	if agent.ParentID != "coordinator" || agent.Model != "opus" || agent.Status != "killed" || agent.Result != "killed" ||
		agent.Turns != 1 || agent.TotalTokens != 100 || agent.CurrentTool != "" || len(agent.Spans) != 1 {
		t.Fatalf("agent = %+v", agent)
	}
	impl := byID["implementer"]
	if len(impl.Spans) != 2 || impl.Spans[0].Status != "failed" || impl.Status != "lost" || impl.JobID != "" {
		t.Fatalf("implementer = %+v", impl)
	}
	if bash := byID["job_3"]; bash.ParentID != "implementer" || bash.Status != "failed" || bash.Result != "FAIL" {
		t.Fatalf("bash = %+v", bash)
	}
	if a, b := byID["reviewer:A"], byID["reviewer:B"]; a.Status != "killed" || b.Status != "killed" || a.Result != "" || a.JobID != "job_4" {
		t.Fatalf("reviewers = %+v / %+v", a, b)
	}
	if watch := byID["job_5"]; watch.Status != "lost" {
		t.Fatalf("watch = %+v", watch)
	}
}
