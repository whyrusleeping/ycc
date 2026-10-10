package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// stepTurner runs one step per backend call; calls past the script succeed.
type stepTurner struct {
	mu    sync.Mutex
	steps []func() (*gollama.ResponseMessageGenerate, error)
	calls int
}

func (t *stepTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	t.mu.Lock()
	t.calls++
	n := t.calls
	t.mu.Unlock()
	if n <= len(t.steps) {
		return t.steps[n-1]()
	}
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{Role: "assistant", Content: "done"}}}}, nil
}

func (t *stepTurner) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

func overflowStep() (*gollama.ResponseMessageGenerate, error) {
	return nil, errors.New("context_length_exceeded")
}

func blockingOverflowStep(entered, release chan struct{}) func() (*gollama.ResponseMessageGenerate, error) {
	return func() (*gollama.ResponseMessageGenerate, error) {
		close(entered)
		<-release
		return nil, errors.New("context_length_exceeded")
	}
}

func newOverflowRunSession(t *testing.T, turner *stepTurner) *Session {
	t.Helper()
	s := newStopSession(t)
	s.inter = newInteraction(true, s.emitter)
	s.Mode = "chat"
	// The opening prompt is the oversized view, so live history and replay of
	// the durable log start identically.
	s.prompt = "authorized task " + strings.Repeat("oversized ", 20_000)
	s.retryCh = make(chan struct{})
	s.switchRetryCh = make(chan struct{}, 1)
	s.rolloverCh = make(chan *rolloverRequest, 1)
	s.loop = &engine.Loop{Client: turner, Model: "test", ModelName: "test", Tools: tools.New(), Emitter: s.emitter,
		Steer: s, Retry: engine.RetryPolicy{MaxAttempts: 1}, ContextLengthHandled: true}
	s.loop.SetHistory([]gollama.Message{{Role: "user", Content: s.prompt}})
	s.contextSummary = func(context.Context, []event.Event, []jobs.Info, string) (string, error) {
		return "[COMPACT DURABLE EVIDENCE] authorized task", nil
	}
	return s
}

func assertParkedBehindGate(t *testing.T, s *Session, turner *stepTurner, wantCalls int, input string) {
	t.Helper()
	waitStatus(t, s, event.StatusError)
	// Give a wrongly-continuing owner time to issue another request.
	time.Sleep(50 * time.Millisecond)
	if got := turner.count(); got != wantCalls {
		t.Fatalf("backend calls = %d, want %d (oversized view resent after the gate)", got, wantCalls)
	}
	s.steerMu.Lock()
	running := s.running
	s.steerMu.Unlock()
	if running {
		t.Fatal("parked owner left running=true; a coordinator switch could not wake it")
	}
	events := s.log.Snapshot()
	delivered, gate := 0, 0
	for _, ev := range events {
		if ev.Type == event.UserInputDelivered && str(ev.Data, "text") == input {
			delivered = ev.Seq
		}
		if ev.Type == event.SessionError && str(ev.Data, "action") == "switch_model" {
			gate = ev.Seq
		}
	}
	if delivered == 0 || gate == 0 {
		t.Fatalf("accepted input not durably retained (delivered=%d) or gate missing (%d)", delivered, gate)
	}
	if replayed, live := engine.ReplayHistory(events), s.loop.History(); !reflect.DeepEqual(replayed, live) {
		t.Fatalf("parked live view (%d messages) differs from reopen (%d messages)", len(live), len(replayed))
	}
	if err := s.SendInput("more"); err == nil {
		t.Fatal("SendInput accepted while the model-switch gate is active")
	}
}

func TestFailedAutomaticSummaryRetainsConcurrentInputWithoutResending(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	turner := &stepTurner{steps: []func() (*gollama.ResponseMessageGenerate, error){blockingOverflowStep(entered, release)}}
	s := newOverflowRunSession(t, turner)
	summaryStarted, summaryRelease := make(chan struct{}), make(chan struct{})
	s.contextSummary = func(context.Context, []event.Event, []jobs.Info, string) (string, error) {
		close(summaryStarted)
		<-summaryRelease
		return "", errors.New("summary backend failed")
	}
	go s.run()
	<-entered
	close(release)
	<-summaryStarted
	if err := s.SendInput("restriction during summary"); err != nil {
		t.Fatal(err)
	}
	close(summaryRelease)
	assertParkedBehindGate(t, s, turner, 1, "restriction during summary")
	s.Stop()
}

func TestTerminalCompactOverflowRetainsConcurrentInputWithoutResending(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	turner := &stepTurner{steps: []func() (*gollama.ResponseMessageGenerate, error){
		overflowStep, blockingOverflowStep(entered, release),
	}}
	s := newOverflowRunSession(t, turner)
	go s.run()
	<-entered
	if err := s.SendInput("restriction during compact request"); err != nil {
		t.Fatal(err)
	}
	close(release)
	assertParkedBehindGate(t, s, turner, 2, "restriction during compact request")
	if countType(s.log.Snapshot(), event.ContextViewChanged) != 1 {
		t.Fatal("terminal overflow should follow exactly one compact view")
	}
	s.Stop()
}

func TestPauseDuringOverflowIsHonouredThenRecovers(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	turner := &stepTurner{steps: []func() (*gollama.ResponseMessageGenerate, error){blockingOverflowStep(entered, release)}}
	s := newOverflowRunSession(t, turner)
	go s.run()
	<-entered
	if err := s.Interrupt(); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitStatus(t, s, event.StatusPaused)
	events := s.log.Snapshot()
	if countType(events, event.ContextViewChanged) != 0 || countType(events, event.SessionError) != 0 {
		t.Fatal("pause during overflow replaced context or forced a model-switch error")
	}
	if err := s.Resume(); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, event.StatusIdle)
	events = s.log.Snapshot()
	if countType(events, event.ContextViewChanged) != 1 || countType(events, event.SessionError) != 0 {
		t.Fatalf("resumed overflow did not recover once: transitions=%d errors=%d",
			countType(events, event.ContextViewChanged), countType(events, event.SessionError))
	}
	if got := turner.count(); got != 2 {
		t.Fatalf("backend calls = %d, want overflow + recovered turn", got)
	}
	s.Stop()
}

// Reopening a session whose compact view already overflowed must not reissue
// that request; an actual coordinator switch is what wakes it.
func TestReopenHonoursDurableModelSwitchGate(t *testing.T) {
	turner := &stepTurner{}
	s := newStopSession(t)
	s.inter = newInteraction(true, s.emitter)
	s.resumed = true
	s.Mode = "chat"
	s.retryCh = make(chan struct{})
	s.switchRetryCh = make(chan struct{}, 1)
	s.rolloverCh = make(chan *rolloverRequest, 1)
	s.reg = testRegistry()
	s.coordinator = "a"
	s.emitter.Emit(event.SessionStarted, map[string]any{"coordinator": "a"})
	s.emitter.EmitAs("user", event.UserInput, map[string]any{"text": "authorized task"})
	s.emitter.Emit(event.ContextViewChanged, map[string]any{"summary": "[compact durable evidence] authorized task", "reason": "context_error_recovery"})
	s.emitter.Emit(event.SessionError, map[string]any{"msg": "still too large", "kind": string(engine.KindContextLength), "action": "switch_model"})
	s.loop = &engine.Loop{Client: turner, Model: "test", ModelName: "a", Tools: tools.New(), Emitter: s.emitter,
		Steer: s, Retry: engine.RetryPolicy{MaxAttempts: 1}, ContextLengthHandled: true}
	s.loop.SetHistory(engine.ReplayHistory(s.log.Snapshot()))
	if !s.loop.PendingResponse() {
		t.Fatal("test setup: reopened compact view should owe a response")
	}

	go s.run()
	waitStatus(t, s, event.StatusError)
	time.Sleep(50 * time.Millisecond)
	if got := turner.count(); got != 0 {
		t.Fatalf("reopen resent the compact oversized request %d time(s)", got)
	}
	if err := s.Resume(); err == nil {
		t.Fatal("Resume retried the unchanged request behind the gate")
	}
	if err := s.SendInput("more"); err == nil {
		t.Fatal("SendInput accepted behind the gate")
	}

	if err := s.SetRoleConfig("b", "", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for countType(s.log.Snapshot(), event.Resumed) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("coordinator switch did not wake the gated owner")
		}
		time.Sleep(time.Millisecond)
	}
	s.Stop()
}

// hookRecorder appends to the durable log, then runs after on the emitting goroutine.
type hookRecorder struct {
	log   *event.Log
	after func(event.Event)
}

func (r *hookRecorder) Record(actor string, typ event.Type, data map[string]any) event.Event {
	ev := r.log.Record(actor, typ, data)
	r.after(ev)
	return ev
}

// A coordinator switch that lands after the gate is durable but before the owner
// parks must still retry automatically rather than being dropped.
func TestCoordinatorSwitchBeforeOwnerParksStillRetries(t *testing.T) {
	turner := &stepTurner{steps: []func() (*gollama.ResponseMessageGenerate, error){overflowStep, overflowStep}}
	s := newOverflowRunSession(t, turner)
	s.reg = testRegistry()
	s.coordinator = "a"
	published, release := make(chan struct{}), make(chan struct{})
	em := event.NewEmitter(&hookRecorder{log: s.log, after: func(ev event.Event) {
		if ev.Type == event.SessionError && str(ev.Data, "action") == "switch_model" {
			close(published)
			<-release
		}
	}}, "coordinator")
	s.emitter = em
	s.loop.Emitter = em
	go s.run()
	<-published
	if err := s.SetRoleConfig("b", "", nil); err != nil {
		close(release)
		s.Stop()
		t.Fatal(err)
	}
	close(release)
	defer s.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for countType(s.log.Snapshot(), event.Resumed) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("coordinator switch before the owner parked was dropped; no automatic retry")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRolloverMediaDetectionIsStructured(t *testing.T) {
	text := []gollama.Message{{Role: "user", Content: "plain"}}
	cases := []struct {
		name    string
		history []gollama.Message
		events  []event.Event
		want    bool
	}{
		{name: "plain", history: text, want: false},
		{name: "prose mentioning attachments is not media", history: []gollama.Message{{Role: "user", Content: "attachments= picture attachment bytes are unavailable"}}, want: false},
		{name: "anthropic tool image", history: []gollama.Message{{Role: "tool", Images: []string{"aW1n"}}}, want: true},
		{name: "anthropic tool document", history: []gollama.Message{{Role: "tool", Documents: []gollama.Document{{}}}}, want: true},
		{name: "follow-up image block", history: []gollama.Message{{Role: "user", MultiContent: []gollama.ContentBlock{{Type: "image"}}}}, want: true},
		{name: "durable tool image (json decoded)", history: text, events: []event.Event{
			{Seq: 1, Type: event.ToolResult, Actor: "coordinator", Data: map[string]any{"images": float64(1), "docs": float64(0)}},
		}, want: true},
		{name: "durable tool document", history: text, events: []event.Event{
			{Seq: 1, Type: event.ToolResult, Actor: "coordinator", Data: map[string]any{"images": 0, "docs": 2}},
		}, want: true},
		{name: "subagent tool image is not coordinator media", history: text, events: []event.Event{
			{Seq: 1, Type: event.ToolResult, Actor: "implementer", Data: map[string]any{"images": 1}},
		}, want: false},
		{name: "tool image before the selected view", history: text, events: []event.Event{
			{Seq: 1, Type: event.ToolResult, Actor: "coordinator", Data: map[string]any{"images": 1}},
			{Seq: 2, Type: event.ContextViewChanged, Data: map[string]any{"summary": "s"}},
		}, want: false},
		{name: "user picture anywhere in authority", history: text, events: []event.Event{
			{Seq: 1, Type: event.UserInput, Data: map[string]any{"text": "see", "images": []any{map[string]any{"attachment_id": "att"}}}},
			{Seq: 2, Type: event.ContextViewChanged, Data: map[string]any{"summary": "s"}},
		}, want: true},
	}
	for _, tc := range cases {
		if got := selectedViewContainsMedia(tc.history, tc.events); got != tc.want {
			t.Errorf("%s: selectedViewContainsMedia = %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestContextSummaryKeepsBudgetWrapUpAndJobReports(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Type: event.UserInput, Data: map[string]any{"text": "task"}},
		{Seq: 2, Type: event.BudgetExceeded, Actor: "user", Data: map[string]any{"action": "halt", "text": "Session budget reached — wrap up now"}},
		{Seq: 3, Type: event.JobNotified, Actor: "user", Data: map[string]any{"id": "job_9", "text": "job_9 finished: tests passed"}},
	}
	for i := 4; i < 80; i++ {
		events = append(events, event.Event{Seq: i, Type: event.DecisionMade, Data: map[string]any{"decision": strings.Repeat("d", 300)}})
	}
	summary, err := buildCoordinatorRolloverSummary(context.Background(), events, nil, "s_budget")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SESSION BUDGET STATE", "wrap up now", "job_9 finished: tests passed"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary omitted %q:\n%s", want, summary)
		}
	}
}
