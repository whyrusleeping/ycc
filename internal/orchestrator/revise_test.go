package orchestrator

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

// scripted returns a fixed sequence of responses, one per Turn call. Because a
// subagent's Loop is reused across rounds, one scripted turner serves both the
// initial run and the revision run.
type scripted struct {
	resp []*gollama.ResponseMessageGenerate
	i    int
}

func (s *scripted) Turn(gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	if s.i >= len(s.resp) {
		return text("(no more scripted responses)"), nil
	}
	r := s.resp[s.i]
	s.i++
	return r, nil
}

func call(name, args string) *gollama.ResponseMessageGenerate {
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{
		Role:      "assistant",
		ToolCalls: []gollama.ToolCall{{ID: "c1", Type: "function", Function: gollama.ToolCallFunction{Name: name, Arguments: args}}},
	}}}}
}
func text(s string) *gollama.ResponseMessageGenerate {
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{Role: "assistant", Content: s}}}}
}

type noopAsker struct{}

func (noopAsker) Ask(context.Context, string, []string) (string, error) { return "ok", nil }
func (noopAsker) Confirm(context.Context, string) (bool, error)         { return true, nil }

// TestReviseLoop exercises the full M3 revise cycle deterministically: the
// implementer ships a buggy Add (a-b), the reviewer says "revise", the
// coordinator sends fix instructions to the SAME implementer (context reused,
// using edit_file on the prior file), and the SAME reviewer re-reviews and
// accepts — after which commit succeeds.
func TestReviseLoop(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("Add function", "## Acceptance\n- add.go has Add returning a+b\n\n## Work log\n", 1, nil, nil); err != nil {
		t.Fatal(err)
	}

	implTurner := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("Write", `{"file_path":"add.go","content":"package demo\n\nfunc Add(a, b int) int { return a - b }\n"}`),
		call("finish", `{"report":"created add.go"}`),
		// after send_to_implementer (context reused → edit the existing file):
		call("Edit", `{"file_path":"add.go","old_string":"a - b","new_string":"a + b"}`),
		call("finish", `{"report":"fixed Add to return a + b"}`),
	}}
	revTurner := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("submit_review", `{"verdict":"revise","summary":"Add subtracts","findings":[{"severity":"blocker","message":"returns a-b, should be a+b"}]}`),
		// after re_review:
		call("submit_review", `{"verdict":"accept","summary":"now returns a+b"}`),
	}}

	d := &Deps{
		Workspace:   ws,
		Docs:        store,
		Repo:        repo,
		Emitter:     event.NewEmitter(event.NewStdoutRecorder(io.Discard), "coordinator"),
		Implementer: AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return implTurner }},
		Reviewers:   []AgentSpec{{Name: "rev", Model: "m", NewClient: func() engine.Turner { return revTurner }}},
		Asker:       noopAsker{},
	}
	ctx := context.Background()
	args := func(kv ...string) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}

	if _, err := spawnImplementer(d).Call(ctx, args("task_id", "0001", "plan", "add it")); err != nil {
		t.Fatal(err)
	}
	r2, _ := spawnReviewers(d).Call(ctx, args("task_id", "0001"))
	if !strings.Contains(r2.Content, "0/1 reviewers accept") {
		t.Fatalf("first review should reject:\n%s", r2.Content)
	}

	if _, err := sendToImplementer(d).Call(ctx, args("task_id", "0001", "instructions", "make Add return a+b")); err != nil {
		t.Fatal(err)
	}
	// The fix must have edited the prior file (proves implementer context reuse).
	body, _ := os.ReadFile(filepath.Join(ws, "add.go"))
	if !strings.Contains(string(body), "a + b") {
		t.Fatalf("implementer did not reuse context to fix file:\n%s", body)
	}

	r4, _ := reReview(d).Call(ctx, args("task_id", "0001"))
	if !strings.Contains(r4.Content, "1/1 reviewers accept") {
		t.Fatalf("re-review should accept:\n%s", r4.Content)
	}

	r5, err := commitTool(d).Call(ctx, args("task_id", "0001", "message", "add Add"))
	if err != nil || r5.IsError {
		t.Fatalf("commit failed: %v %s", err, r5.Content)
	}

	// Work log should record plan?(no, we skipped) but at least implementer report,
	// a revision, two reviews, and the commit decision.
	task, _ := store.Get("0001")
	for _, want := range []string{"implementer report:", "revision:", "review (rev): revise", "review (rev): accept", "decision: accept — commit"} {
		if !strings.Contains(task.Body, want) {
			t.Fatalf("work log missing %q:\n%s", want, task.Body)
		}
	}
}
