package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

// implementerPrompt with hints surfaces an advisory, non-prescriptive "starting
// points" preload that names each hint; with no hints the prompt is byte-identical
// to the no-hints form (so tasks without hints behave exactly as today).
func TestImplementerPromptHints(t *testing.T) {
	task := &docs.Task{ID: "0001", Title: "do thing", Body: "## Body"}

	with := implementerPrompt(task, "the plan", []string{"internal/foo.go", "func Bar"})
	if !strings.Contains(with, "Starting points") {
		t.Fatalf("expected advisory starting-points block, got:\n%s", with)
	}
	if !strings.Contains(with, "advisory, NOT prescriptive") || !strings.Contains(with, "not mandated steps") {
		t.Fatalf("expected non-prescriptive framing, got:\n%s", with)
	}
	for _, h := range []string{"internal/foo.go", "func Bar"} {
		if !strings.Contains(with, h) {
			t.Fatalf("hint %q missing from prompt:\n%s", h, with)
		}
	}

	// No hints (nil and empty) must match each other and contain no preload.
	noneNil := implementerPrompt(task, "the plan", nil)
	noneEmpty := implementerPrompt(task, "the plan", []string{"", "   "})
	if noneNil != noneEmpty {
		t.Fatalf("nil vs blank hints differ:\n%q\n%q", noneNil, noneEmpty)
	}
	if strings.Contains(noneNil, "Starting points") {
		t.Fatalf("no-hints prompt should not contain a starting-points block:\n%s", noneNil)
	}
}

// boundHints/contextHintsBlock bound token cost: an over-long hint is truncated,
// and a list longer than the cap is capped with a "more hints omitted" note.
func TestBoundHints(t *testing.T) {
	long := strings.Repeat("x", maxContextHintLen+50)
	got := boundHints([]string{long})
	if len(got) != 1 || !strings.HasSuffix(got[0], "…[truncated]") {
		t.Fatalf("over-long hint not truncated: %q", got)
	}
	if r := []rune(got[0]); len(r) > maxContextHintLen+len([]rune("…[truncated]")) {
		t.Fatalf("truncated hint too long: %d runes", len(r))
	}

	var many []string
	for i := 0; i < maxContextHints+5; i++ {
		many = append(many, fmt.Sprintf("hint-%d", i))
	}
	capped := boundHints(many)
	if len(capped) != maxContextHints+1 {
		t.Fatalf("expected %d entries (cap + omitted note), got %d", maxContextHints+1, len(capped))
	}
	last := capped[len(capped)-1]
	if !strings.Contains(last, "more hints omitted") {
		t.Fatalf("expected omitted-note last entry, got %q", last)
	}

	// Blank entries are dropped entirely.
	if g := boundHints([]string{"", "  ", "\n"}); len(g) != 0 {
		t.Fatalf("blank hints should be dropped, got %v", g)
	}
}

// spawn_implementer with context_hints records a work-log breadcrumb noting the
// hints (consistent with task 0020's plan persistence).
func TestSpawnImplementerHintsBreadcrumb(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("a task", "## Work log\n", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	implTurner := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("finish", `{"report":"done"}`),
	}}
	d := &Deps{
		Workspace:   ws,
		Docs:        store,
		Repo:        repo,
		Emitter:     event.NewEmitter(&captureRec{}, "coordinator"),
		Implementer: AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return implTurner }},
		Asker:       noopAsker{},
	}
	_, err = spawnImplementer(d).Call(context.Background(), map[string]any{
		"task_id": "0001", "plan": "go", "context_hints": []any{"internal/foo.go", "func Bar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !workLogContains(t, store, "0001", "context hints: internal/foo.go; func Bar") {
		task, _ := store.Get("0001")
		t.Fatalf("work log missing context-hints breadcrumb:\n%s", task.Body)
	}
}

// propose_plan with context_hints persists a "### Starting points" subsection in
// the durable plan artifact and records a work-log breadcrumb.
func TestProposePlanHintsArtifact(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("a task", "## Work log\n", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	d := &Deps{
		Workspace: ws,
		Docs:      store,
		Repo:      repo,
		Emitter:   event.NewEmitter(&captureRec{}, "coordinator"),
		Asker:     noopAsker{},
	}
	_, err = proposePlan(d).Call(context.Background(), map[string]any{
		"task_id": "0001", "plan": "the plan", "context_hints": []any{"internal/foo.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, _ := store.Get("0001")
	if !strings.Contains(task.Body, "### Starting points") || !strings.Contains(task.Body, "- internal/foo.go") {
		t.Fatalf("plan artifact missing starting-points subsection:\n%s", task.Body)
	}
	if !strings.Contains(task.Body, "context hints: 1 recorded with plan") {
		t.Fatalf("work log missing context-hints breadcrumb:\n%s", task.Body)
	}
}
