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

func TestImplementerPromptIncludesHints(t *testing.T) {
	task := &docs.Task{ID: "0001", Title: "do thing", Body: "## Body"}
	prompt := implementerPrompt(task, "the plan", []string{"internal/foo.go", "func Bar"})
	for _, hint := range []string{"internal/foo.go", "func Bar"} {
		if !strings.Contains(prompt, hint) {
			t.Fatalf("hint %q missing from implementer context", hint)
		}
	}
}

func TestBoundHints(t *testing.T) {
	long := strings.Repeat("x", maxContextHintLen+50)
	got := boundHints([]string{long})
	if len(got) != 1 || len([]rune(got[0])) >= len([]rune(long)) {
		t.Fatalf("over-long hint was not truncated: %q", got)
	}

	var many []string
	for i := 0; i < maxContextHints+5; i++ {
		many = append(many, fmt.Sprintf("hint-%d", i))
	}
	if got := boundHints(many); len(got) > maxContextHints+1 {
		t.Fatalf("hint cap exceeded: got %d entries", len(got))
	}
	if got := boundHints([]string{"", "  ", "\n"}); len(got) != 0 {
		t.Fatalf("blank hints should be dropped, got %v", got)
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
