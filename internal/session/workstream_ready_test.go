package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

func TestDeriveWorkstreamReadiness(t *testing.T) {
	ws := workstream.Workstream{ID: "ws_1"}
	tests := []struct {
		name       string
		status     event.Status
		blocked    bool
		commits    int
		task       string
		taskStatus docs.Status
		taskErr    error
		want       workstream.Status
		reason     string
	}{
		{name: "ready", status: event.StatusIdle, commits: 1, want: workstream.StatusReady},
		{name: "error", status: event.StatusError, commits: 1, want: workstream.StatusNeedsAttention, reason: "session ended in error"},
		{name: "blocked", status: event.StatusIdle, blocked: true, commits: 1, want: workstream.StatusNeedsAttention, reason: "session ended blocked"},
		{name: "no commits", status: event.StatusIdle, want: workstream.StatusNeedsAttention, reason: "no commits since base"},
		{name: "task review", status: event.StatusIdle, commits: 1, task: "0251", taskStatus: docs.StatusInReview, want: workstream.StatusReady},
		{name: "task blocked", status: event.StatusIdle, commits: 1, task: "0251", taskStatus: docs.StatusBlocked, want: workstream.StatusNeedsAttention, reason: "task 0251 is blocked"},
		{name: "task unreadable", status: event.StatusIdle, commits: 1, task: "0251", taskErr: errors.New("missing"), want: workstream.StatusNeedsAttention, reason: "task 0251 could not be read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws.TaskID = tt.task
			got, reason := deriveWorkstreamReadiness(ws, tt.status, tt.blocked, tt.commits, tt.taskStatus, tt.taskErr)
			if got != tt.want || !strings.Contains(reason, tt.reason) {
				t.Fatalf("derive = %s, %q; want %s containing %q", got, reason, tt.want, tt.reason)
			}
		})
	}
}

func TestEvaluateWorkstreamReadinessEmitsEvents(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "ready.txt", "ready\n", "ready work")
	m.evaluateWorkstreamReadiness(ws.ID, event.StatusIdle, false)
	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusReady {
		t.Fatalf("status = %s, want ready", got.Status)
	}
	if _, ok := snapshotHasEvent(s, event.WorkstreamReady); !ok {
		t.Fatal("workstream_ready event not emitted")
	}

	_, _ = m.workstreams.Transition(ws.ID, workstream.StatusActive, "", workstream.StatusReady)
	m.evaluateWorkstreamReadiness(ws.ID, event.StatusError, false)
	got, _ = m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusNeedsAttention || got.StatusReason != "session ended in error" {
		t.Fatalf("errored status = %+v", got)
	}
	if _, ok := snapshotHasEvent(s, event.WorkstreamNeedsAttention); !ok {
		t.Fatal("workstream_needs_attention event not emitted")
	}
	if err := m.DiscardWorkstream(ws.ID); err != nil {
		t.Fatalf("cleanup discard: %v", err)
	}
}

func TestReconcileWorkstreamReadinessAfterRestart(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	ws, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	commitInto(t, ws.WorktreePath, "restart.txt", "durable\n", "restart work")
	if err := m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		got, _ := m.workstreams.Get(ws.ID)
		return got.Status == workstream.StatusReady
	})
	// Simulate the pre-readiness registry state left by an older daemon while the
	// durable terminal log and branch commit survive.
	if err := m.workstreams.SetStatus(ws.ID, workstream.StatusActive); err != nil {
		t.Fatal(err)
	}
	if err := m.ReconcileWorkstreams(); err != nil {
		t.Fatal(err)
	}
	got, _ := m.workstreams.Get(ws.ID)
	if got.Status != workstream.StatusReady {
		t.Fatalf("reconciled status = %s, want ready", got.Status)
	}
	if err := m.DiscardWorkstream(ws.ID); err != nil {
		t.Fatalf("cleanup discard: %v", err)
	}
}

func TestWorkstreamBlockedThenResolvedIsReady(t *testing.T) {
	events := []event.Event{
		{Type: event.SubagentFinished, Data: map[string]any{"role": "implementer", "blocked": true}},
		// An unrelated reviewer outcome must not change the implementer latch.
		{Type: event.SubagentFinished, Data: map[string]any{"role": "reviewer"}},
		// A successful revision resolves the earlier implementer block.
		{Type: event.SubagentFinished, Data: map[string]any{"role": "implementer"}},
	}
	// Exercise the helper shared by both durable reconciliation and the live
	// watcher before asserting the complete durable-log fold.
	blocked := updateWorkstreamBlocked(false, events[0])
	if !blocked {
		t.Fatal("blocked implementer finish was not latched")
	}
	blocked = updateWorkstreamBlocked(blocked, events[1])
	if !blocked {
		t.Fatal("reviewer finish incorrectly cleared implementer block")
	}
	blocked = updateWorkstreamBlocked(blocked, events[2])
	if blocked || workstreamRunBlocked(events) {
		t.Fatal("resolved implementer block remained latched")
	}
	status, reason := deriveWorkstreamReadiness(workstream.Workstream{}, event.StatusIdle, blocked, 1, "", nil)
	if status != workstream.StatusReady || reason != "" {
		t.Fatalf("resolved completion = %s, %q; want ready", status, reason)
	}
}

func TestWorkstreamTerminalStatusPreservesError(t *testing.T) {
	events := []event.Event{{Type: event.SessionError}, {Type: event.SessionStopped}}
	if got := workstreamTerminalStatus(events); got != event.StatusError {
		t.Fatalf("status = %s, want error", got)
	}
}
