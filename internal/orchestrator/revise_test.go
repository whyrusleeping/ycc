package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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
	resp     []*gollama.ResponseMessageGenerate
	i        int
	system   string
	messages []gollama.Message
}

type failingTurner struct{ err error }

func (f failingTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return nil, f.err
}

// contextAfterFirst succeeds once (the initial subagent run), then rejects the
// next retained continuation before it can execute a tool.
type contextAfterFirst struct{ calls int }

func (t *contextAfterFirst) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	t.calls++
	if t.calls == 1 {
		return call("finish", `{"report":"initial verification passed"}`), nil
	}
	return nil, errors.New("context_length_exceeded")
}

// contextAfterMutation fails only after the revision has dispatched one write;
// orchestrator recovery must not replay that mutating Run.
type contextAfterMutation struct{ calls int }

func (t *contextAfterMutation) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	t.calls++
	switch t.calls {
	case 1:
		return call("finish", `{"report":"initial"}`), nil
	case 2:
		return call("Write", `{"file_path":"once.txt","content":"once\n"}`), nil
	default:
		return nil, errors.New("context_length_exceeded")
	}
}

func (s *scripted) TurnCtx(_ context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	s.system = opts.System
	s.messages = append([]gollama.Message(nil), opts.Messages...)
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
	var retainedEvidence string
	for _, msg := range revTurner.messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "CURRENT SCOPED CHANGESET") {
			retainedEvidence = msg.Content
		}
	}
	if !strings.Contains(retainedEvidence, "Exact retrieval: git diff") || !strings.Contains(retainedEvidence, "add.go") {
		t.Fatalf("retained re-review missing current identified evidence: %q", retainedEvidence)
	}

	r5, err := commitTool(d).Call(ctx, args("task_id", "0001", "message", "add Add", "outcome", "Added and verified Add."))
	if err != nil || r5.IsError {
		t.Fatalf("commit failed: %v %s", err, r5.Content)
	}

	// Commit must leave the working tree clean: compaction happens before the
	// commit, so no backlog files are left uncommitted afterward.
	out, gerr := exec.Command("git", "-C", ws, "status", "--porcelain").Output()
	if gerr != nil {
		t.Fatalf("git status: %v", gerr)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("working tree not clean after commit:\n%s", out)
	}

	task, _ := store.Get("0001")
	if task.Status != docs.StatusDone || !strings.Contains(task.Body, "Added and verified Add.") || !strings.Contains(task.Body, "Commit: add Add") {
		t.Fatalf("completed task is not compact: %+v\n%s", task, task.Body)
	}
	for _, removed := range []string{"implementer report:", "revision:", "review (rev):"} {
		if strings.Contains(task.Body, removed) {
			t.Fatalf("completed task retained operational detail %q:\n%s", removed, task.Body)
		}
	}
}

func TestRevisionContextModeValidationAndHandoffBound(t *testing.T) {
	if _, err := revisionContextMode(map[string]any{"context_mode": "discard"}); err == nil {
		t.Fatal("invalid context mode accepted")
	}
	long := strings.Repeat("é", maxRevisionHandoffBytes)
	got := boundedRevisionHandoff(long)
	if len(got) > maxRevisionHandoffBytes || !strings.Contains(got, "truncated") || !utf8.ValidString(got) {
		t.Fatalf("handoff bound invalid: bytes=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestFreshImplementerRevisionReplacesHistory(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("fresh revision", "## Acceptance\n- fixed\n\n## Work log\n", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	first := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("Write", `{"file_path":"x.txt","content":"old\n"}`),
		call("finish", `{"report":"initial"}`),
	}}
	fresh := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("Edit", `{"file_path":"x.txt","old_string":"old","new_string":"fixed"}`),
		call("finish", `{"report":"fresh fix"}`),
	}}
	calls := 0
	rec := &captureRec{}
	d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(rec, "coordinator"),
		Implementer: AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner {
			calls++
			if calls == 1 {
				return first
			}
			return fresh
		}}, Asker: noopAsker{}}
	ctx := context.Background()
	if res, _ := spawnImplementer(d).Call(ctx, map[string]any{"task_id": "0001", "plan": "initial"}); res.IsError {
		t.Fatal(res.Content)
	}
	res, _ := sendToImplementer(d).Call(ctx, map[string]any{"task_id": "0001", "instructions": "replace old with fixed and verify it", "context_mode": "fresh"})
	if res.IsError {
		t.Fatal(res.Content)
	}
	if calls != 2 {
		t.Fatalf("NewClient calls = %d, want replacement client", calls)
	}
	if len(fresh.messages) == 0 || !strings.Contains(fresh.messages[0].Content, "fresh conversation context") || !strings.Contains(fresh.messages[0].Content, "replace old with fixed") {
		t.Fatalf("fresh seed is not self-contained: %+v", fresh.messages)
	}
	for _, m := range fresh.messages {
		if strings.Contains(m.Content, "Coordinator's plan:\ninitial") {
			t.Fatalf("fresh revision retained old implementer history: %+v", fresh.messages)
		}
	}
	if !strings.Contains(res.Content, "mode=fresh round=2") {
		t.Fatalf("missing fresh context metadata: %s", res.Content)
	}
	manualFresh := false
	for _, ev := range rec.events {
		if ev.Type == event.SubagentSpawned && ev.Data["rollover_reason"] == "coordinator_fresh" &&
			ev.Data["old_context_tokens_est"] != nil && ev.Data["new_context_tokens_est"] != nil {
			manualFresh = true
		}
	}
	if !manualFresh {
		t.Fatal("coordinator-selected fresh lifecycle metadata missing")
	}
}

func TestFreshImplementerRecoversAfterContextLengthFailure(t *testing.T) {
	ws := t.TempDir()
	repo, _ := git.Open(ws)
	store := docs.NewStore(ws)
	_, _ = store.Create("recover", "## Work log\n", 1, nil, nil)
	fresh := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("Write", `{"file_path":"ok.txt","content":"ok\n"}`),
		call("finish", `{"report":"recovered"}`),
	}}
	calls := 0
	d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(&captureRec{}, "coordinator"),
		Implementer: AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner {
			calls++
			if calls == 1 {
				return failingTurner{err: errors.New("context_length_exceeded")}
			}
			return fresh
		}}, Asker: noopAsker{}}
	first, _ := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "go"})
	if !first.IsError || !strings.Contains(first.Content, "context window exceeded") {
		t.Fatalf("expected context failure, got: %s", first.Content)
	}
	res, _ := sendToImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "instructions": "implement from current state", "context_mode": "fresh"})
	if res.IsError || !strings.Contains(res.Content, "mode=fresh") {
		t.Fatalf("fresh recovery failed: %s", res.Content)
	}
}

func TestImplementerRevisionRollsOverAtContextBudget(t *testing.T) {
	ws := t.TempDir()
	repo, _ := git.Open(ws)
	store := docs.NewStore(ws)
	_, _ = store.Create("pressure", "## Acceptance\n- fixed\n\n## Work log\n", 1, nil, nil)
	first := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("Write", `{"file_path":"x.txt","content":"old\n"}`),
		call("finish", `{"report":"wrote old; not yet verified"}`),
	}}
	fresh := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("Edit", `{"file_path":"x.txt","old_string":"old","new_string":"fixed"}`),
		call("finish", `{"report":"fixed and verified"}`),
	}}
	clients := 0
	rec := &captureRec{}
	d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(rec, "coordinator"),
		Implementer: AgentSpec{Name: "impl", Model: "m", ContextWindow: 100, ContextSafeFraction: .5, NewClient: func() engine.Turner {
			clients++
			if clients == 1 {
				return first
			}
			return fresh
		}}, Asker: noopAsker{}}
	ctx := context.Background()
	if res, _ := spawnImplementer(d).Call(ctx, map[string]any{"task_id": "0001", "plan": "write it"}); res.IsError {
		t.Fatal(res.Content)
	}
	res, _ := sendToImplementer(d).Call(ctx, map[string]any{"task_id": "0001", "instructions": "fix x and verify"})
	if res.IsError || !strings.Contains(res.Content, "mode=fresh round=2") {
		t.Fatalf("automatic rollover failed: %s", res.Content)
	}
	if clients != 2 {
		t.Fatalf("NewClient calls = %d, want pressure replacement", clients)
	}
	if len(fresh.messages) == 0 || !strings.Contains(fresh.messages[0].Content, "Current bounded scoped changeset") ||
		!strings.Contains(fresh.messages[0].Content, "not yet verified") || !strings.Contains(fresh.messages[0].Content, "fix x and verify") {
		t.Fatalf("fresh pressure handoff lacks current state: %+v", fresh.messages)
	}
	for _, m := range fresh.messages {
		if len(m.ToolCalls) > 0 && m.ToolCalls[0].Function.Name == "Write" {
			t.Fatalf("fresh loop replayed old tool history: %+v", fresh.messages)
		}
	}
	found := false
	for _, ev := range rec.events {
		if ev.Type == event.SubagentSpawned && ev.Data["rollover_reason"] == "automatic_pressure" {
			found = ev.Data["old_context_tokens_est"] != nil && ev.Data["new_context_tokens_est"] != nil
		}
	}
	if !found {
		t.Fatal("pressure rollover event missing reason or old/new estimates")
	}
}

func TestImplementerContextErrorRecoversOnceWithoutDuplicateMutation(t *testing.T) {
	ws := t.TempDir()
	repo, _ := git.Open(ws)
	store := docs.NewStore(ws)
	_, _ = store.Create("recover automatically", "## Work log\n", 1, nil, nil)
	retained := &contextAfterFirst{}
	fresh := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("Write", `{"file_path":"ok.txt","content":"ok\n"}`),
		call("finish", `{"report":"recovered and verified"}`),
	}}
	clients := 0
	rec := &captureRec{}
	d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(rec, "coordinator"),
		Implementer: AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner {
			clients++
			if clients == 1 {
				return retained
			}
			return fresh
		}}, Asker: noopAsker{}}
	ctx := context.Background()
	if res, _ := spawnImplementer(d).Call(ctx, map[string]any{"task_id": "0001", "plan": "go"}); res.IsError {
		t.Fatal(res.Content)
	}
	res, _ := sendToImplementer(d).Call(ctx, map[string]any{"task_id": "0001", "instructions": "continue safely"})
	if res.IsError || !strings.Contains(res.Content, "recovered and verified") {
		t.Fatalf("context recovery failed: %s", res.Content)
	}
	var recoverySpawns, writes, sessionErrors int
	for _, ev := range rec.events {
		if ev.Type == event.SubagentSpawned && ev.Data["rollover_reason"] == "context_error_recovery" {
			recoverySpawns++
		}
		if ev.Type == event.ToolCall && ev.Data["name"] == "Write" {
			writes++
		}
		if ev.Type == event.SessionError {
			sessionErrors++
		}
	}
	if clients != 2 || recoverySpawns != 1 || writes != 1 || sessionErrors != 0 {
		t.Fatalf("clients=%d recovery spawns=%d Write calls=%d session errors=%d; want 2,1,1,0", clients, recoverySpawns, writes, sessionErrors)
	}
}

func TestImplementerContextErrorAfterMutationIsNotReplayed(t *testing.T) {
	ws := t.TempDir()
	repo, _ := git.Open(ws)
	store := docs.NewStore(ws)
	_, _ = store.Create("do not replay", "## Work log\n", 1, nil, nil)
	retained := &contextAfterMutation{}
	clients := 0
	rec := &captureRec{}
	d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(rec, "coordinator"),
		Implementer: AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner {
			clients++
			return retained
		}}, Asker: noopAsker{}}
	ctx := context.Background()
	if res, _ := spawnImplementer(d).Call(ctx, map[string]any{"task_id": "0001", "plan": "go"}); res.IsError {
		t.Fatal(res.Content)
	}
	res, _ := sendToImplementer(d).Call(ctx, map[string]any{"task_id": "0001", "instructions": "write once"})
	if !res.IsError || !strings.Contains(res.Content, "context window exceeded") {
		t.Fatalf("unsafe context error should be returned without replay: %s", res.Content)
	}
	var writes, recoveries int
	for _, ev := range rec.events {
		if ev.Type == event.ToolCall && ev.Data["name"] == "Write" {
			writes++
		}
		if ev.Type == event.SubagentSpawned && ev.Data["rollover_reason"] == "context_error_recovery" {
			recoveries++
		}
	}
	if clients != 1 || writes != 1 || recoveries != 0 {
		t.Fatalf("clients=%d Write calls=%d recoveries=%d; mutating Run was replayed", clients, writes, recoveries)
	}
	if body, err := os.ReadFile(filepath.Join(ws, "once.txt")); err != nil || string(body) != "once\n" {
		t.Fatalf("single mutation result = %q, %v", body, err)
	}
}

func TestReviewerContextErrorRecreatesSameSlotAndSubmitsOnce(t *testing.T) {
	ws := t.TempDir()
	repo, _ := git.Open(ws)
	store := docs.NewStore(ws)
	_, _ = store.Create("review recover", "## Acceptance\n- safe\n\n## Work log\n", 1, nil, nil)
	retained := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("submit_review", `{"verdict":"revise","summary":"verification failed","findings":[{"severity":"blocker","message":"race remains"}]}`),
	}}
	fresh := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("submit_review", `{"verdict":"accept","summary":"race fixed"}`),
	}}
	clients, resolved := 0, 0
	spec := AgentSpec{Name: "review-model", Label: "concurrency", Focus: "Focus on races.", Model: "m", NewClient: func() engine.Turner {
		clients++
		switch clients {
		case 1:
			return &reviewContextAfterFirst{first: retained}
		default:
			return fresh
		}
	}}
	rec := &captureRec{}
	d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(rec, "coordinator"), Asker: noopAsker{},
		ReviewTier: func(string) ReviewPlan {
			resolved++
			return ReviewPlan{Tier: "deep", Specs: []AgentSpec{spec}}
		}}
	ctx := context.Background()
	if res, _ := spawnReviewers(d).Call(ctx, map[string]any{"task_id": "0001"}); !strings.Contains(res.Content, "0/1") {
		t.Fatal(res.Content)
	}
	res, _ := reReview(d).Call(ctx, map[string]any{"task_id": "0001", "handoff": "reran race test"})
	if !strings.Contains(res.Content, "1/1") {
		t.Fatalf("review recovery failed: %s", res.Content)
	}
	var submissions, recoveries int
	for _, ev := range rec.events {
		if ev.Type == event.ReviewSubmitted {
			submissions++
		}
		if ev.Type == event.SubagentSpawned && ev.Data["rollover_reason"] == "context_error_recovery" {
			recoveries++
		}
	}
	if resolved != 1 || clients != 2 || submissions != 2 || recoveries != 1 {
		t.Fatalf("resolved=%d clients=%d submissions=%d recoveries=%d; want 1,2,2,1", resolved, clients, submissions, recoveries)
	}
	if !strings.Contains(fresh.system, "Focus on races") || len(fresh.messages) == 0 ||
		!strings.Contains(fresh.messages[len(fresh.messages)-1].Content, "race remains") ||
		!strings.Contains(fresh.messages[len(fresh.messages)-1].Content, "reran race test") {
		t.Fatalf("same-slot recovery lost focus/findings/verification: system=%q messages=%+v", fresh.system, fresh.messages)
	}
}

func TestReviewerPressureRolloverIncludesImplementationEvidenceWithoutHandoff(t *testing.T) {
	ws := t.TempDir()
	repo, _ := git.Open(ws)
	store := docs.NewStore(ws)
	_, _ = store.Create("review pressure", "## Acceptance\n- verified\n\n## Work log\n", 1, nil, nil)
	impl := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("finish", `{"report":"go test ./... passed; implementation complete"}`),
	}}
	first := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("submit_review", `{"verdict":"revise","summary":"needs correction","findings":[{"severity":"major","message":"edge case remains"}]}`),
	}}
	fresh := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("submit_review", `{"verdict":"accept","summary":"verified"}`),
	}}
	reviewerClients := 0
	rec := &captureRec{}
	d := &Deps{
		Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(rec, "coordinator"), Asker: noopAsker{},
		Implementer: AgentSpec{Name: "impl", Model: "impl-model", NewClient: func() engine.Turner { return impl }},
		ReviewTier: func(string) ReviewPlan {
			return ReviewPlan{Tier: "standard", Specs: []AgentSpec{{
				Name: "review", Model: "review-model", ContextWindow: 100, ContextSafeFraction: .5,
				NewClient: func() engine.Turner {
					reviewerClients++
					if reviewerClients == 1 {
						return first
					}
					return fresh
				},
			}}}
		},
	}
	ctx := context.Background()
	if res, _ := spawnImplementer(d).Call(ctx, map[string]any{"task_id": "0001", "plan": "verify"}); res.IsError {
		t.Fatal(res.Content)
	}
	if res, _ := spawnReviewers(d).Call(ctx, map[string]any{"task_id": "0001"}); !strings.Contains(res.Content, "0/1") {
		t.Fatal(res.Content)
	}
	res, _ := reReview(d).Call(ctx, map[string]any{"task_id": "0001"})
	if !strings.Contains(res.Content, "1/1") || reviewerClients != 2 {
		t.Fatalf("automatic reviewer rollover failed: clients=%d result=%s", reviewerClients, res.Content)
	}
	seed := fresh.messages[len(fresh.messages)-1].Content
	for _, want := range []string{"Latest implementation/verification evidence", "go test ./... passed", "edge case remains"} {
		if !strings.Contains(seed, want) {
			t.Fatalf("fresh reviewer seed missing %q:\n%s", want, seed)
		}
	}
	foundPressure := false
	for _, ev := range rec.events {
		if ev.Type == event.SubagentSpawned && ev.Data["role"] == "reviewer" && ev.Data["rollover_reason"] == "automatic_pressure" {
			foundPressure = true
		}
	}
	if !foundPressure {
		t.Fatal("automatic reviewer pressure rollover was not recorded")
	}
}

func TestFailedReReviewPreservesLastSuccessfulMajorFindings(t *testing.T) {
	ws := t.TempDir()
	repo, _ := git.Open(ws)
	store := docs.NewStore(ws)
	_, _ = store.Create("retry review", "## Acceptance\n- correct\n\n## Work log\n", 1, nil, nil)
	first := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("submit_review", `{"verdict":"revise","summary":"found issue","findings":[{"severity":"blocker","message":"preserve this finding"}]}`),
	}}
	fresh := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("submit_review", `{"verdict":"accept","summary":"fixed"}`),
	}}
	clients := 0
	spec := AgentSpec{Name: "review", Model: "m", NewClient: func() engine.Turner {
		clients++
		if clients == 1 {
			return &reviewFailureAfterFirst{first: first}
		}
		return fresh
	}}
	d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(&captureRec{}, "coordinator"), Asker: noopAsker{},
		ReviewTier: func(string) ReviewPlan { return ReviewPlan{Tier: "standard", Specs: []AgentSpec{spec}} }}
	ctx := context.Background()
	if res, _ := spawnReviewers(d).Call(ctx, map[string]any{"task_id": "0001"}); !strings.Contains(res.Content, "0/1") {
		t.Fatal(res.Content)
	}
	failed, _ := reReview(d).Call(ctx, map[string]any{"task_id": "0001"})
	if !strings.Contains(failed.Content, "reviewer error") {
		t.Fatalf("expected failed review result: %s", failed.Content)
	}
	retried, _ := reReview(d).Call(ctx, map[string]any{"task_id": "0001", "context_mode": "fresh"})
	if !strings.Contains(retried.Content, "1/1") {
		t.Fatalf("fresh retry failed: %s", retried.Content)
	}
	seed := fresh.messages[len(fresh.messages)-1].Content
	if !strings.Contains(seed, "preserve this finding") {
		t.Fatalf("failed re-review erased the last successful blocker:\n%s", seed)
	}
}

// reviewContextAfterFirst lets the initial review submit, then fails the retained
// re-review before submission. It records both requests through scripted.
type reviewContextAfterFirst struct {
	first *scripted
	calls int
}

func (t *reviewContextAfterFirst) TurnCtx(ctx context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	t.calls++
	if t.calls == 1 {
		return t.first.TurnCtx(ctx, opts)
	}
	return nil, errors.New("maximum context length exceeded")
}

type reviewFailureAfterFirst struct {
	first *scripted
	calls int
}

func (t *reviewFailureAfterFirst) TurnCtx(ctx context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	t.calls++
	if t.calls == 1 {
		return t.first.TurnCtx(ctx, opts)
	}
	return nil, errors.New("review backend unavailable")
}

func TestFreshReReviewPreservesResolvedSlotsAndSeedsCurrentDiff(t *testing.T) {
	ws := t.TempDir()
	repo, _ := git.Open(ws)
	store := docs.NewStore(ws)
	_, _ = store.Create("review fresh", "## Acceptance\n- correct\n\n## Work log\n", 1, nil, nil)
	if err := os.WriteFile(filepath.Join(ws, "change.go"), []byte("package demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := &scripted{resp: []*gollama.ResponseMessageGenerate{call("submit_review", `{"verdict":"revise","summary":"fix"}`)}}
	fresh := &scripted{resp: []*gollama.ResponseMessageGenerate{call("submit_review", `{"verdict":"accept","summary":"fixed"}`)}}
	clients := 0
	resolved := 0
	spec := AgentSpec{Name: "sol", Label: "correctness", Focus: "Focus on runtime correctness.", Model: "m", NewClient: func() engine.Turner {
		clients++
		if clients == 1 {
			return first
		}
		return fresh
	}}
	d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(&captureRec{}, "coordinator"), Asker: noopAsker{},
		ReviewTier: func(string) ReviewPlan { resolved++; return ReviewPlan{Tier: "deep", Specs: []AgentSpec{spec}} }}
	if res, _ := spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": "0001"}); !strings.Contains(res.Content, "0/1") {
		t.Fatal(res.Content)
	}
	res, _ := reReview(d).Call(context.Background(), map[string]any{"task_id": "0001", "context_mode": "fresh", "handoff": "runtime was revised; verify prior deadlock"})
	if !strings.Contains(res.Content, "1/1") || !strings.Contains(res.Content, "mode=fresh round=2") {
		t.Fatalf("fresh re-review failed: %s", res.Content)
	}
	if resolved != 1 {
		t.Fatalf("review tier re-resolved %d times, want once", resolved)
	}
	if clients != 2 || !strings.Contains(fresh.system, "runtime correctness") {
		t.Fatalf("resolved slot/focus not preserved: clients=%d system=%q", clients, fresh.system)
	}
	if len(fresh.messages) < 4 || fresh.messages[2].Role != "tool" || !strings.Contains(fresh.messages[2].Content, "change.go") || !strings.Contains(fresh.messages[len(fresh.messages)-1].Content, "prior deadlock") {
		t.Fatalf("fresh reviewer missing bounded diff/handoff: %+v", fresh.messages)
	}
	seed := fresh.messages[len(fresh.messages)-1].Content
	if !strings.Contains(seed, "CURRENT SCOPED CHANGESET") || !strings.Contains(seed, "Exact retrieval: git diff") {
		t.Fatalf("fresh re-review missing current identified evidence: %q", seed)
	}
}

func TestFreshReReviewRefusesWhenCurrentChangesetCannotBeDerived(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	_, _ = store.Create("review stale", "## Acceptance\n- correct\n\n## Work log\n", 1, nil, nil)
	reviewer := &scripted{resp: []*gollama.ResponseMessageGenerate{
		call("submit_review", `{"verdict":"revise","summary":"fix"}`),
	}}
	spec := AgentSpec{Name: "review", Model: "m", NewClient: func() engine.Turner { return reviewer }}
	d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(&captureRec{}, "coordinator"), Asker: noopAsker{},
		ReviewTier: func(string) ReviewPlan { return ReviewPlan{Tier: "standard", Specs: []AgentSpec{spec}} }}
	if res, _ := spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": "0001"}); res.IsError {
		t.Fatalf("initial review: %s", res.Content)
	}
	cmd := exec.Command("git", "-C", ws, "add", "-A")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	cmd = exec.Command("git", "-C", ws, "commit", "-m", "external head move")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	res, _ := reReview(d).Call(context.Background(), map[string]any{"task_id": "0001", "context_mode": "fresh"})
	if !res.IsError || !strings.Contains(res.Content, "HEAD moved") {
		t.Fatalf("fresh re-review did not propagate changeset failure: %+v", res)
	}
	if reviewer.i != 1 {
		t.Fatalf("reviewer ran %d turns; unsafe fresh rollover should be refused before a second turn", reviewer.i)
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

func TestReviewTierBlurbUsesCanonicalBuiltins(t *testing.T) {
	blurb := reviewTierBlurb(&Deps{})
	for _, name := range []string{"self-review", "standard", "comprehensive"} {
		if !strings.Contains(blurb, "'"+name+"'") {
			t.Errorf("fallback blurb does not name %q: %s", name, blurb)
		}
	}
	for _, legacy := range []string{"'simple'", "'single-opus'", "'high-powered'"} {
		if strings.Contains(blurb, legacy) {
			t.Errorf("fallback blurb exposes legacy alias %q: %s", legacy, blurb)
		}
	}
}

// With a SelfReview ReviewPlan (the 'self-review' tier), spawn_reviewers spawns no
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
			return ReviewPlan{Tier: "self-review", Requested: name, SelfReview: true}
		},
	}
	res, err := spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": "0001", "review_tier": "self-review"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "review this change yourself") {
		t.Fatalf("self-review guidance missing:\n%s", res.Content)
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
// records the tier in session events.
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
			return ReviewPlan{Tier: "standard", Requested: name, Specs: []AgentSpec{
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
	msgs := revTurner.messages
	if len(msgs) != 4 || msgs[0].Role != "user" || msgs[1].Role != "assistant" || msgs[2].Role != "tool" || msgs[3].Role != "user" {
		t.Fatalf("reviewer history should be diff exchange followed by seed prompt: %+v", msgs)
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Function.Name != "Bash" || msgs[1].ToolCalls[0].ID != reviewDiffCallID {
		t.Fatalf("synthetic diff call = %+v", msgs[1])
	}
	if !strings.Contains(msgs[2].Content, "0001-a-task.md") {
		t.Fatalf("preloaded result does not contain current staged diff: %q", msgs[2].Content)
	}
	var syntheticTurns, syntheticCalls, syntheticResults int
	for _, ev := range rec.events {
		if ev.Type == event.UserInput && ev.Data["synthetic"] == true {
			t.Fatalf("synthetic reviewer history emitted user_input: %+v", ev)
		}
		if ev.Actor != "reviewer:rev" || ev.Data["synthetic"] != true {
			continue
		}
		switch ev.Type {
		case event.ModelTurn:
			syntheticTurns++
		case event.ToolCall:
			syntheticCalls++
		case event.ToolResult:
			syntheticResults++
			if ev.Data["result"] != msgs[2].Content {
				t.Fatalf("synthetic event and seeded result differ")
			}
		}
	}
	if syntheticTurns != 1 || syntheticCalls != 1 || syntheticResults != 1 {
		t.Fatalf("synthetic reviewer events = turn %d call %d result %d", syntheticTurns, syntheticCalls, syntheticResults)
	}
}

// A review tier may task several reviewers with distinct focuses — including two
// slots on the SAME logical model under different labels (spec §13.1). Each
// reviewer's system prompt carries its own focus and actor labels stay distinct.
func TestSpawnReviewersFocusedTier(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	store := docs.NewStore(ws)
	if _, err := store.Create("a task", "## Work log\n", 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	accept := func() *scripted {
		return &scripted{resp: []*gollama.ResponseMessageGenerate{
			call("submit_review", `{"verdict":"accept","summary":"ok"}`),
		}}
	}
	readable, fast := accept(), accept()
	rec := &captureRec{}
	d := &Deps{
		Workspace: ws,
		Docs:      store,
		Repo:      repo,
		Emitter:   event.NewEmitter(rec, "coordinator"),
		Asker:     noopAsker{},
		ReviewTier: func(name string) ReviewPlan {
			return ReviewPlan{Tier: "deep", Requested: name, Specs: []AgentSpec{
				{Name: "claude", Label: "readability", Focus: "Focus on conciseness and code readability.",
					Model: "m", NewClient: func() engine.Turner { return readable }},
				{Name: "claude", Label: "performance", Focus: "Focus on performance characteristics.",
					Model: "m", NewClient: func() engine.Turner { return fast }},
			}}
		},
	}
	res, err := spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": "0001", "review_tier": "deep"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "2/2 reviewers accept") {
		t.Fatalf("expected both reviewers to accept:\n%s", res.Content)
	}
	// Each reviewer's system prompt carries only its own focus.
	if !strings.Contains(readable.system, "conciseness and code readability") || strings.Contains(readable.system, "performance characteristics") {
		t.Fatalf("readability reviewer got the wrong focus:\n%s", readable.system)
	}
	if !strings.Contains(fast.system, "performance characteristics") {
		t.Fatalf("performance reviewer missing its focus:\n%s", fast.system)
	}
	// Distinct actor labels, both attributed to the same logical model.
	actors := map[string]bool{}
	for _, ev := range rec.events {
		actors[ev.Actor] = true
	}
	for _, want := range []string{"reviewer:readability", "reviewer:performance"} {
		if !actors[want] {
			t.Fatalf("missing actor %q; got %v", want, actors)
		}
	}
}
