package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/tools"
)

func preloadRegistry(t *testing.T, root string) *tools.Registry {
	t.Helper()
	reg := tools.New()
	reg.Add(tools.Worker(&tools.Workspace{Root: root})...)
	return reg
}

func TestBuildPreloadHistoryUsesRealRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	offset, limit := 2, 1
	got := buildPreloadHistory(context.Background(), preloadRegistry(t, root), []preloadFile{
		{Path: "sample.txt", Offset: &offset, Limit: &limit},
		{Path: "missing.txt"},
	})
	if len(got.History) != 4 {
		t.Fatalf("history len = %d, want nudge + assistant + two results", len(got.History))
	}
	if got.History[0].Role != "user" || got.History[0].Content != preloadNudge {
		t.Fatalf("nudge = %+v", got.History[0])
	}
	assistant := got.History[1]
	if assistant.Role != "assistant" || assistant.Content != "" || len(assistant.ToolCalls) != 2 {
		t.Fatalf("assistant exchange = %+v", assistant)
	}
	if call := assistant.ToolCalls[0]; call.ID != "preload_1" || call.Type != "function" || call.Function.Name != "Read" || call.Function.Arguments != `{"file_path":"sample.txt","offset":2,"limit":1}` {
		t.Fatalf("first call = %+v", call)
	}
	if got.History[2].Role != "tool" || got.History[2].ToolCallID != "preload_1" || got.History[2].Content != "     2\tbeta\n" {
		t.Fatalf("real Read formatting not preserved: %+v", got.History[2])
	}
	if !got.Results[1].IsError || got.History[3].ToolCallID != "preload_2" || !strings.Contains(got.History[3].Content, "no such file") {
		t.Fatalf("stale path should be an honest tool error: result=%+v message=%+v", got.Results[1], got.History[3])
	}
}

func TestBuildPreloadHistoryBoundsAndSkips(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat(strings.Repeat("x", 1000)+"\n", 100)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "later.txt"), []byte("must not enter seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := buildPreloadHistory(context.Background(), preloadRegistry(t, root), []preloadFile{{Path: "big.txt"}, {Path: "later.txt"}})
	if got.Bytes > maxPreloadBytes {
		t.Fatalf("preload result bytes = %d, cap %d", got.Bytes, maxPreloadBytes)
	}
	if !strings.Contains(got.Results[0].Content, preloadTruncated) {
		t.Fatalf("first result lacks truncation marker: tail=%q", got.Results[0].Content[len(got.Results[0].Content)-200:])
	}
	if got.Results[1].Content != preloadSkipped {
		t.Fatalf("later result = %q, want skipped marker", got.Results[1].Content)
	}

	many := make([]preloadFile, maxPreloadFiles+3)
	for i := range many {
		many[i] = preloadFile{Path: "later.txt"}
	}
	capped := buildPreloadHistory(context.Background(), preloadRegistry(t, root), many)
	if capped.Files != maxPreloadFiles || capped.Omitted != 3 || len(capped.Calls) != maxPreloadFiles {
		t.Fatalf("cap = files %d omitted %d calls %d", capped.Files, capped.Omitted, len(capped.Calls))
	}
	if !strings.Contains(capped.History[0].Content, "3 more preload file(s) omitted") {
		t.Fatalf("nudge lacks omission marker: %q", capped.History[0].Content)
	}
}

func TestBuildPreloadHistoryStripsMedia(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "tiny.png"), []byte("not actually decoded by Read"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := buildPreloadHistory(context.Background(), preloadRegistry(t, root), []preloadFile{{Path: "tiny.png"}})
	if len(got.Results) != 1 || len(got.Results[0].Images) != 0 || len(got.Results[0].Documents) != 0 {
		t.Fatalf("media attachments survived preload: %+v", got.Results)
	}
	if !strings.Contains(got.Results[0].Content, "attachment omitted") || !strings.Contains(got.Results[0].Content, "Read this file yourself") {
		t.Fatalf("media result lacks text-only guidance: %q", got.Results[0].Content)
	}
	if len(got.History[2].Images) != 0 || len(got.History[2].Documents) != 0 || len(got.History[2].MultiContent) != 0 {
		t.Fatalf("seed history is not text-only: %+v", got.History[2])
	}
}

func TestSpawnImplementerPreloadHistoryEventsAndBreadcrumb(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "needed.go"), []byte("package needed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("a task", "## Work log\n", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	rec := &captureRec{}
	turner := &scripted{resp: []*gollama.ResponseMessageGenerate{call("finish", `{"report":"done"}`)}}
	d := &Deps{
		Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(rec, "coordinator"),
		Implementer: AgentSpec{Name: "impl-name", Backend: "anthropic", Model: "impl-id", NewClient: func() engine.Turner { return turner }},
		Asker:       noopAsker{},
	}
	res, err := spawnImplementer(d).Call(context.Background(), map[string]any{
		"task_id": "0001", "plan": "implement it", "preload_files": []any{map[string]any{"path": "needed.go", "limit": float64(10)}},
	})
	if err != nil || res.IsError {
		t.Fatalf("spawn failed: res=%+v err=%v", res, err)
	}
	msgs := turner.messages
	if len(msgs) != 4 {
		t.Fatalf("first request history len = %d, want 4: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" || msgs[2].Role != "tool" || msgs[3].Role != "user" {
		t.Fatalf("history role order = %q, %q, %q, %q", msgs[0].Role, msgs[1].Role, msgs[2].Role, msgs[3].Role)
	}
	if !strings.Contains(msgs[2].Content, "package needed") || !strings.Contains(msgs[3].Content, "Implement this task") {
		t.Fatalf("genuine result or trailing seed absent: %+v", msgs)
	}
	if msgs[len(msgs)-1].ToolCallID != "" || msgs[len(msgs)-1].Role != "user" {
		t.Fatalf("seed prompt was not last: %+v", msgs[len(msgs)-1])
	}
	var syntheticTurns, syntheticCalls, syntheticResults int
	for _, ev := range rec.events {
		if ev.Actor != "implementer" || ev.Data["synthetic"] != true {
			continue
		}
		switch ev.Type {
		case event.ModelTurn:
			syntheticTurns++
			if ev.Data["model_name"] != "impl-name" || ev.Data["backend"] != "anthropic" || ev.Data["model_id"] != "impl-id" {
				t.Fatalf("synthetic model identity = %+v", ev.Data)
			}
		case event.ToolCall:
			syntheticCalls++
		case event.ToolResult:
			syntheticResults++
			if ev.Data["result"] != msgs[2].Content {
				t.Fatalf("event result differs from seeded result\nevent: %q\nseed: %q", ev.Data["result"], msgs[2].Content)
			}
		}
	}
	if syntheticTurns != 1 || syntheticCalls != 1 || syntheticResults != 1 {
		t.Fatalf("synthetic events = turn %d call %d result %d", syntheticTurns, syntheticCalls, syntheticResults)
	}
	if !workLogContains(t, store, "0001", "preload: 1 file(s)") {
		t.Fatal("work log lacks preload breadcrumb")
	}
}
