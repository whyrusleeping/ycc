package orchestrator

import (
	"context"
	"io"
	"os"
	"os/exec"
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
	resp   []*gollama.ResponseMessageGenerate
	i      int
	system string
}

func (s *scripted) Turn(opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	s.system = opts.System
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

// Delegating a task to the implementer establishes focus on it (spec §20.2), and
// dedupes against a focus already set (e.g. by an earlier update_task→in_progress
// or the pm hand-off) so the same task isn't recorded twice.
func TestSpawnImplementerEmitsTaskFocus(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("a task", "## Work log\n", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	rec := &captureRec{}
	implTurner := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("finish", `{"report":"done"}`),
		call("finish", `{"report":"done again"}`),
	}}
	d := &Deps{
		Workspace:   ws,
		Docs:        store,
		Repo:        repo,
		Emitter:     event.NewEmitter(rec, "coordinator"),
		Implementer: AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return implTurner }},
		Asker:       noopAsker{},
	}
	ctx := context.Background()
	if _, err := spawnImplementer(d).Call(ctx, map[string]any{"task_id": "0001", "plan": "go"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.focusTasks(); len(got) != 1 || got[0] != "0001" {
		t.Fatalf("focus events = %v, want [0001]", got)
	}
	// Already focused → no duplicate.
	if _, err := spawnImplementer(d).Call(ctx, map[string]any{"task_id": "0001", "plan": "again"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.focusTasks(); len(got) != 1 {
		t.Fatalf("spawn re-emitted focus for the same task: %v", got)
	}
}

// An implementer that yields without editing anything (empty report, no new
// diff) must surface an actionable error to the coordinator — not a blank
// "report" that reads as success — so the coordinator retries instead of being
// puzzled that nothing happened (the motivating bug).
func TestSpawnImplementerNoOpGuard(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("a task", "## Work log\n", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Yields immediately with empty text and no tool call → no workspace changes.
	implTurner := &scripted{resp: []*gollama.ResponseMessageGenerate{text("")}}
	d := &Deps{
		Workspace:   ws,
		Docs:        store,
		Repo:        repo,
		Emitter:     event.NewEmitter(&captureRec{}, "coordinator"),
		Implementer: AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return implTurner }},
		Asker:       noopAsker{},
	}
	res, err := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "go"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("expected an error result for a no-op implementer, got ok: %q", res.Content)
	}
	if !strings.Contains(res.Content, "no changes") {
		t.Fatalf("error result should explain the no-op, got: %q", res.Content)
	}
}

// An implementer that ends its run via report_blocked (a decision it can't make)
// must surface a distinct BLOCKED outcome to the coordinator — an OK result the
// coordinator can act on, NOT the no-op error — with the reason recorded in the
// task work log, even when no workspace changes were made.
func TestSpawnImplementerBlocked(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("a task", "## Work log\n", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Blocks immediately with a reason and no workspace changes.
	implTurner := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("report_blocked", `{"reason":"which storage backend should this use?"}`),
	}}
	d := &Deps{
		Workspace:   ws,
		Docs:        store,
		Repo:        repo,
		Emitter:     event.NewEmitter(&captureRec{}, "coordinator"),
		Implementer: AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return implTurner }},
		Asker:       noopAsker{},
	}
	res, err := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "go"})
	if err != nil {
		t.Fatal(err)
	}
	// Must NOT trip the no-op error path even though nothing changed.
	if res.IsError {
		t.Fatalf("blocked outcome should be an OK result, got error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "BLOCKED") || !strings.Contains(res.Content, "which storage backend should this use?") {
		t.Fatalf("blocked result missing header/reason:\n%s", res.Content)
	}
	if !workLogContains(t, store, "0001", "BLOCKED — which storage backend should this use?") {
		task, _ := store.Get("0001")
		t.Fatalf("work log missing BLOCKED line:\n%s", task.Body)
	}
}

type noopAsker struct{}

func (noopAsker) Ask(context.Context, string, []string) (string, error) { return "ok", nil }
func (noopAsker) AskMany(_ context.Context, qs []Question) ([]string, error) {
	out := make([]string, len(qs))
	for i := range qs {
		out[i] = "ok"
	}
	return out, nil
}
func (noopAsker) Confirm(context.Context, string) (bool, error) { return true, nil }

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

	// Commit must leave the working tree clean: the work-log line is appended
	// before committing, so no backlog files are left uncommitted afterward.
	out, gerr := exec.Command("git", "-C", ws, "status", "--porcelain").Output()
	if gerr != nil {
		t.Fatalf("git status: %v", gerr)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("working tree not clean after commit:\n%s", out)
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

// reviewLogLines returns the work-log lines a task accumulated (for assertions).
func workLogContains(t *testing.T, store *docs.Store, id, want string) bool {
	t.Helper()
	task, err := store.Get(id)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	return strings.Contains(task.Body, want)
}

// With a SelfReview ReviewPlan (the 'simple' tier), spawn_reviewers spawns no
// reviewer loop: it returns self-review guidance, records the tier in the work
// log, and emits a review_tier_selected event.
func TestSpawnReviewersSelfReviewTier(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("a task", "## Work log\n", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	rec := &captureRec{}
	d := &Deps{
		Workspace: ws,
		Docs:      store,
		Repo:      repo,
		Emitter:   event.NewEmitter(rec, "coordinator"),
		Asker:     noopAsker{},
		ReviewTier: func(name string) ReviewPlan {
			return ReviewPlan{Tier: "simple", Requested: name, SelfReview: true}
		},
	}
	res, err := spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": "0001", "review_tier": "simple"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "review this change yourself") {
		t.Fatalf("self-review guidance missing:\n%s", res.Content)
	}
	if !workLogContains(t, store, "0001", "review tier: simple (coordinator self-review)") {
		t.Fatalf("work log missing self-review tier line")
	}
	// review_tier_selected emitted with self_review=true.
	found := false
	for _, ev := range rec.events {
		if ev.Type == event.ReviewTierSelected {
			found = true
			if sr, _ := ev.Data["self_review"].(bool); !sr {
				t.Fatalf("review_tier_selected self_review = false, want true")
			}
		}
	}
	if !found {
		t.Fatal("no review_tier_selected event emitted")
	}
}

// With a ReviewPlan carrying Specs, spawn_reviewers runs those reviewers and
// records the tier line in the work log.
func TestSpawnReviewersTierWithSpecs(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("a task", "## Work log\n", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	revTurner := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("submit_review", `{"verdict":"accept","summary":"ok"}`),
	}}
	rec := &captureRec{}
	d := &Deps{
		Workspace: ws,
		Docs:      store,
		Repo:      repo,
		Emitter:   event.NewEmitter(rec, "coordinator"),
		Asker:     noopAsker{},
		ReviewTier: func(name string) ReviewPlan {
			return ReviewPlan{Tier: "single-opus", Requested: name, Specs: []AgentSpec{
				{Name: "rev", Model: "m", NewClient: func() engine.Turner { return revTurner }},
			}}
		},
	}
	res, err := spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": "0001"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "1/1 reviewers accept") {
		t.Fatalf("expected one reviewer to accept:\n%s", res.Content)
	}
	if !workLogContains(t, store, "0001", "review tier: single-opus — reviewers: rev") {
		t.Fatalf("work log missing tier-with-reviewers line")
	}
}
