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

func TestActivityStepsTiming(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	seq := 0
	ev := func(actor string, at time.Duration, typ event.Type, data map[string]any) event.Event {
		seq++
		return event.Event{Seq: seq, TS: base.Add(at), Actor: actor, Type: typ, Data: data}
	}
	events := []event.Event{
		// The coordinator's legacy empty actor normalizes to "coordinator".
		ev("", 2*time.Second, event.ModelTurn, map[string]any{"model_name": "opus", "duration_ms": 2000.0, "tool_calls": 1.0,
			"text": "looking", "usage": map[string]any{"total": 10.0, "output": 7.0}}),
		ev("", 2*time.Second, event.ToolCall, map[string]any{"id": "c1", "name": "Bash", "args": `{"command":"go  test\n./..."}`}),
		ev("agent:a", 3*time.Second, event.ToolCall, map[string]any{"id": "c0", "name": "Read", "args": `{"file_path":"x.go"}`}),
		ev("agent:a", 4*time.Second, event.ToolResult, map[string]any{"id": "c0", "name": "Read", "duration_ms": 1000.0}),
		ev("", 32*time.Second, event.ToolResult, map[string]any{"id": "c1", "name": "Bash", "duration_ms": 30000.0, "error": true}),
		ev("coordinator", 33*time.Second, event.ToolCall, map[string]any{"id": "c2", "name": "wait", "args": `{}`}),
	}
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
	ctx := context.Background()
	steps, err := store.ActivitySteps(ctx, "s", logPath, -1, -1, "coordinator", time.Time{}, time.Time{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 {
		t.Fatalf("steps = %+v", steps)
	}
	model, bash, wait := steps[0], steps[1], steps[2]
	if model.Kind != "model" || model.Name != "opus" || model.Summary != "1 tool call" || model.OutputTokens != 7 ||
		!model.Started.Equal(base) || model.DurationMS != 2000 || model.RowID != "seq-1" {
		t.Fatalf("model = %+v", model)
	}
	if bash.Kind != "tool" || bash.Status != "error" || bash.Summary != "go test ./..." || bash.DurationMS != 30000 ||
		!bash.Started.Equal(base.Add(2*time.Second)) || !bash.Finished.Equal(base.Add(32*time.Second)) || bash.RowID != "tool-c1" {
		t.Fatalf("bash = %+v", bash)
	}
	if wait.Status != "running" || !wait.Finished.IsZero() {
		t.Fatalf("wait = %+v", wait)
	}
	// A range that starts after a call still times its result; a closed range
	// reports an unanswered call as lost rather than running.
	steps, err = store.ActivitySteps(ctx, "s", logPath, -1, -1, "coordinator", base.Add(10*time.Second), base.Add(40*time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].Name != "Bash" || !steps[0].Started.Equal(base.Add(2*time.Second)) || steps[1].Status != "lost" {
		t.Fatalf("ranged steps = %+v", steps)
	}
}
