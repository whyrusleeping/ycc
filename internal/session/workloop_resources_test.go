package session

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
)

func TestWorkLoopResourceEnvelopeAtStart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget config.Budget
	}{
		{name: "unbounded"},
		{name: "bounded", budget: config.Budget{
			SessionTokens: 12_000, SessionCost: 1.25,
			LoopTokens: 100_000, LoopCost: 8.50,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := config.NewRegistry(&config.Config{
				Models: map[string]config.Model{"c": {Backend: "ollama", Model: "m"}},
				Roles:  config.Roles{Coordinator: "c", Implementer: "c", Reviewers: []string{"c"}},
				Budget: tc.budget,
			})
			m := NewManager(reg, t.TempDir())
			defer m.ReclaimAll()

			got, err := m.StartWorkLoop("")
			if err != nil {
				t.Fatal(err)
			}
			if got.SessionTokenLimit != tc.budget.SessionTokens || got.SessionCostLimit != tc.budget.SessionCost ||
				got.LoopTokenLimit != tc.budget.LoopTokens || got.LoopCostLimit != tc.budget.LoopCost {
				t.Fatalf("captured limits = %+v, want %+v", got, tc.budget)
			}
			if got.SessionTimeLimitSecs != 0 || got.LoopTimeLimitSecs != 0 {
				t.Fatalf("time limits = %d/%d, want explicitly unbounded", got.SessionTimeLimitSecs, got.LoopTimeLimitSecs)
			}
			if !got.ResourceEnvelopeCaptured || !got.CostLimitsPricedOnly {
				t.Fatal("resource-envelope semantics were not exposed")
			}
		})
	}
}

func TestWorkLoopUnpricedUsageDoesNotInventCostCapBreach(t *testing.T) {
	decision := decideLoop(loopDecideInput{
		next: "0366", loopStarted: true,
		cumTokens: 1_000_000, cumCost: 0, loopCost: 0.01,
	})
	if decision.stop {
		t.Fatalf("unpriced usage tripped priced-cost cap: %+v", decision)
	}
}

func TestWorkLoopRepeatedNonCommittingProgressRemainsActionable(t *testing.T) {
	var m *Manager
	var taskID string
	calls := 0
	factory := func(*workLoop) func(context.Context) (loopSessRec, bool, error) {
		return func(context.Context) (loopSessRec, bool, error) {
			calls++
			if calls == 4 {
				if _, err := m.StopWorkLoop("demo"); err != nil {
					return loopSessRec{}, false, err
				}
			}
			return loopSessRec{
				id: fmt.Sprintf("attempt-%d", calls), focus: taskID,
				report: fmt.Sprintf("Experiment %d ruled out a local cause.\nRemaining criteria: provider reproduction.\nNext step: inspect trace %d.", calls, calls),
				tokens: 25, priceStatus: "unpriced",
			}, false, nil
		}
	}
	var ws string
	m, _, ws = loopTestManager(t, factory)
	store := docs.NewStore(ws)
	task, err := store.Create("diagnose intermittent failure", "## Acceptance criteria\n- Reproduce the provider failure\n\n## Work log\n- inspect the next trace\n", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	taskID = task.ID

	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	got := waitLoopFinished(t, m, "demo")
	if got.Outcome != "loop stopped: requested" || got.SessionsRun != 4 {
		t.Fatalf("loop stopped before explicit request: %+v", got)
	}
	if got.TotalTokens != 100 || got.TotalCost != 0 || got.CostStatus != "unpriced" {
		t.Fatalf("unpriced totals were not honest: %+v", got)
	}
	if len(got.Unfinished) != 1 {
		t.Fatalf("unfinished digest = %+v", got.Unfinished)
	}
	unfinished := got.Unfinished[0]
	if unfinished.ID != taskID || unfinished.Attempts != 4 || !strings.Contains(unfinished.LatestEvidence, "Experiment 4") ||
		!strings.Contains(unfinished.RemainingCriteria, "provider reproduction") || !strings.Contains(unfinished.NextStep, "inspect trace 4") {
		t.Fatalf("unfinished evidence is not actionable: %+v", unfinished)
	}
	for i, session := range got.Sessions {
		if session.Focus != taskID || session.Attempt != i+1 || session.Evidence == "" {
			t.Fatalf("attempt %d = %+v", i+1, session)
		}
	}
}

func TestWorkLoopBudgetWrapUpLeavesUnfinishedEvidence(t *testing.T) {
	var taskID string
	factory := func(wl *workLoop) func(context.Context) (loopSessRec, bool, error) {
		wl.loopTokens = 50
		return func(context.Context) (loopSessRec, bool, error) {
			return loopSessRec{
				id: "budget-wrap-up", focus: taskID,
				report: "Remaining criteria: verify the fix.\nNext step: run the focused integration test.",
				tokens: 50, priceStatus: "unpriced",
			}, false, nil
		}
	}
	m, _, ws := loopTestManager(t, factory)
	store := docs.NewStore(ws)
	task, err := store.Create("finish after budget reset", "## Acceptance criteria\n- Verify the fix\n", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	taskID = task.ID
	if _, err := m.StartWorkLoop("demo"); err != nil {
		t.Fatal(err)
	}
	got := waitLoopFinished(t, m, "demo")
	if got.Outcome != "loop stopped: budget reached (50 tokens, cap 50)" || len(got.Unfinished) != 1 {
		t.Fatalf("budget wrap-up = %+v", got)
	}
	if got.Unfinished[0].RemainingCriteria != "verify the fix." || got.Unfinished[0].NextStep != "run the focused integration test." {
		t.Fatalf("budget wrap-up is not actionable: %+v", got.Unfinished[0])
	}
}

func TestRealRunSessionSynthesizesFailureEvidenceForContinuation(t *testing.T) {
	m := NewManager(testRegistry(), "")
	defer m.ReclaimAll()
	ws := t.TempDir()
	s := newStopSession(t)
	s.ID = "failed-without-idle"
	s.Workspace = ws
	started := make(chan struct{})
	wl := &workLoop{
		m: m, loopID: "loop-failure-evidence", project: "demo", workspace: ws,
		state: "running", startedAt: time.Now(), baseline: map[string]docs.Status{},
		startSession: func(Config) (*Session, error) {
			m.mu.Lock()
			m.sessions[s.ID] = s
			m.mu.Unlock()
			close(started)
			return s, nil
		},
	}

	type result struct {
		rec loopSessRec
		err error
	}
	done := make(chan result, 1)
	go func() {
		rec, _, err := wl.realRunSession(context.Background())
		done <- result{rec: rec, err: err}
	}()
	<-started
	s.emitter.Emit(event.TaskFocus, map[string]any{"task": "0366"})
	s.emitter.Emit(event.SessionError, map[string]any{
		"kind": "server_error", "msg": "experiment endpoint returned 503", "action": "retry after the provider reset", "retryable": true,
	})
	s.setStatus(event.StatusError)

	var got result
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("realRunSession did not return")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.rec.focus != "0366" || got.rec.errKind != "server_error" || !got.rec.errRetryable ||
		!strings.Contains(got.rec.report, "experiment endpoint returned 503") || !strings.Contains(got.rec.report, "retry after the provider reset") {
		t.Fatalf("failure evidence = %+v", got.rec)
	}
	wl.sessions = append(wl.sessions, got.rec)
	continuation := wl.continuationContext()
	if !strings.Contains(continuation, "0366") || !strings.Contains(continuation, "experiment endpoint returned 503") {
		t.Fatalf("failure evidence missing from continuation: %q", continuation)
	}
}

func TestWorkLoopResourceEvidencePersists(t *testing.T) {
	ws := t.TempDir()
	old := &workLoop{
		loopID: "loop-resource-evidence", project: "demo", workspace: ws,
		state: "finished", startedAt: time.Now().UTC(),
		sessionTokens: 10_000, sessionCost: 1.5, loopTokens: 50_000, loopCost: 6, envelopeCaptured: true,
		sessions: []loopSessRec{{
			id: "attempt-1", focus: "0366", report: "latest diagnostic", tokens: 500,
			priceStatus: "unpriced", errKind: "server_error", errMessage: "503", errRetryable: true,
		}},
		cumTokens: 500, costStatus: "unpriced",
		unfinished: []WorkLoopDigestTask{{
			ID: "0366", Title: "diagnose", Status: "in_progress", Attempts: 1,
			LatestEvidence: "latest diagnostic", RemainingCriteria: "reproduce", NextStep: "inspect trace",
			PriceStatus: "unpriced",
		}},
	}
	old.persist()

	restarted := NewManager(workLoopPersistTestRegistry(), ws)
	got, err := restarted.GetWorkLoop("")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.SessionTokenLimit != 10_000 || got.SessionCostLimit != 1.5 ||
		got.LoopTokenLimit != 50_000 || got.LoopCostLimit != 6 || !got.CostLimitsPricedOnly {
		t.Fatalf("restored resource envelope = %+v", got)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Attempt != 1 || got.Sessions[0].Evidence != "latest diagnostic" ||
		got.Sessions[0].ErrorMessage != "503" || !got.Sessions[0].ErrorRetryable {
		t.Fatalf("restored attempts = %+v", got.Sessions)
	}
	if len(got.Unfinished) != 1 || got.Unfinished[0].Attempts != 1 || got.Unfinished[0].NextStep != "inspect trace" {
		t.Fatalf("restored unfinished digest = %+v", got.Unfinished)
	}
}
