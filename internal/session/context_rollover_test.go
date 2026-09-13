package session

import (
	"context"
	"errors"
	"fmt"
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

type overflowAfterToolTurner struct {
	mu       sync.Mutex
	calls    int
	msgBytes []int
}

func (t *overflowAfterToolTurner) TurnCtx(_ context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	n := 0
	for _, msg := range opts.Messages {
		n += len(msg.Content)
		for _, call := range msg.ToolCalls {
			n += len(call.Function.Arguments)
		}
	}
	t.msgBytes = append(t.msgBytes, n)
	switch t.calls {
	case 1:
		return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{
			Role: "assistant", ToolCalls: []gollama.ToolCall{{ID: "mutate_once", Type: "function", Function: gollama.ToolCallFunction{Name: "mutate", Arguments: `{}`}}},
		}}}}, nil
	case 2:
		return nil, errors.New("context_length_exceeded")
	default:
		return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{Role: "assistant", Content: "recovered"}}}}, nil
	}
}

func (t *overflowAfterToolTurner) snapshot() (int, []int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls, append([]int(nil), t.msgBytes...)
}

func TestUnattendedOverflowRollsOverOnceWithoutRepeatingMutation(t *testing.T) {
	s := newStopSession(t)
	s.inter = newInteraction(true, s.emitter)
	s.Mode = "chat"
	s.prompt = "complete the authorized task"
	s.retryCh = make(chan struct{})
	s.rolloverCh = make(chan *rolloverRequest, 1)
	s.messageCh = make(chan engine.UserMessage, 4)

	turner := &overflowAfterToolTurner{}
	mutations := 0
	reg := tools.New()
	reg.Add(&gollama.Tool{Name: "mutate", Params: tools.Obj(map[string]any{}), Call: func(context.Context, any) (*gollama.ToolResult, error) {
		mutations++
		return &gollama.ToolResult{Content: "mutation applied; verification artifact /tmp/result.log"}, nil
	}})
	loop := &engine.Loop{
		Client: turner, Model: "test", ModelName: "test", Tools: reg, Emitter: s.emitter,
		Steer: s, Retry: engine.RetryPolicy{MaxAttempts: 1}, ContextLengthHandled: true,
	}
	loop.SetHistory([]gollama.Message{{Role: "user", Content: strings.Repeat("long coordinator history ", 20_000)}})
	s.loop = loop

	go s.run()
	waitStatus(t, s, event.StatusIdle)
	if mutations != 1 {
		t.Fatalf("mutating tool ran %d times, want exactly once", mutations)
	}
	calls, sizes := turner.snapshot()
	if calls != 3 {
		t.Fatalf("backend calls = %d, want tool turn + overflow + recovered turn", calls)
	}
	if len(sizes) != 3 || sizes[2] >= sizes[1] {
		t.Fatalf("recovery did not send genuinely smaller history: %v", sizes)
	}
	events := s.log.Snapshot()
	if countType(events, event.ContextViewChanged) != 1 {
		t.Fatalf("context transition count = %d, want 1", countType(events, event.ContextViewChanged))
	}
	if countType(events, event.SessionError) != 0 {
		t.Fatal("recovered overflow was incorrectly parked as a session error")
	}
	s.Stop()
}

type alwaysOverflowTurner struct {
	mu    sync.Mutex
	sizes []int
}

func (t *alwaysOverflowTurner) TurnCtx(_ context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	n := 0
	for _, msg := range opts.Messages {
		n += len(msg.Content)
	}
	t.mu.Lock()
	t.sizes = append(t.sizes, n)
	t.mu.Unlock()
	return nil, errors.New("maximum context length exceeded")
}

type concurrentOverflowTurner struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (t *concurrentOverflowTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	t.mu.Lock()
	t.calls++
	call := t.calls
	t.mu.Unlock()
	if call == 1 {
		close(t.entered)
		<-t.release
		return nil, errors.New("context_length_exceeded")
	}
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{Role: "assistant", Content: "recovered with correction"}}}}, nil
}

func TestAutomaticOverflowConcurrentInputHasExactDurableBoundary(t *testing.T) {
	s := newStopSession(t)
	s.inter = newInteraction(true, s.emitter)
	s.Mode = "chat"
	s.prompt = "authorized task"
	s.retryCh = make(chan struct{})
	s.rolloverCh = make(chan *rolloverRequest, 1)
	s.messageCh = make(chan engine.UserMessage, 4)
	turner := &concurrentOverflowTurner{entered: make(chan struct{}), release: make(chan struct{})}
	s.loop = &engine.Loop{Client: turner, Model: "test", ModelName: "test", Tools: tools.New(), Emitter: s.emitter, Steer: s, Retry: engine.RetryPolicy{MaxAttempts: 1}, ContextLengthHandled: true}
	s.loop.SetHistory([]gollama.Message{{Role: "user", Content: strings.Repeat("oversized ", 20_000)}})

	go s.run()
	<-turner.entered
	if err := s.SendInput("late unattended restriction"); err != nil {
		t.Fatal(err)
	}
	close(turner.release)
	waitStatus(t, s, event.StatusIdle)
	events := s.log.Snapshot()
	transition, delivery := 0, 0
	for _, ev := range events {
		if ev.Type == event.ContextViewChanged {
			transition = ev.Seq
		}
		if ev.Type == event.UserInputDelivered && str(ev.Data, "text") == "late unattended restriction" {
			delivery = ev.Seq
		}
	}
	if transition == 0 || delivery <= transition {
		t.Fatalf("automatic recovery input boundary: transition=%d delivery=%d", transition, delivery)
	}
	if replayed, live := engine.ReplayHistory(events), s.loop.History(); !reflect.DeepEqual(replayed, live) {
		t.Fatalf("automatic recovery reopen differs from live\nreplay=%#v\nlive=%#v", replayed, live)
	}
	s.Stop()
}

func TestAutomaticOverflowRecoveryIsBoundedAndNeverResendsSameHistory(t *testing.T) {
	s := newStopSession(t)
	s.inter = newInteraction(true, s.emitter)
	s.Mode = "chat"
	s.prompt = "continue"
	s.retryCh = make(chan struct{})
	s.rolloverCh = make(chan *rolloverRequest, 1)
	s.messageCh = make(chan engine.UserMessage, 4)
	turner := &alwaysOverflowTurner{}
	s.loop = &engine.Loop{
		Client: turner, Model: "test", ModelName: "test", Tools: tools.New(), Emitter: s.emitter,
		Steer: s, Retry: engine.RetryPolicy{MaxAttempts: 1}, ContextLengthHandled: true,
	}
	s.loop.SetHistory([]gollama.Message{{Role: "user", Content: strings.Repeat("oversized ", 30_000)}})

	go s.run()
	waitStatus(t, s, event.StatusError)
	turner.mu.Lock()
	sizes := append([]int(nil), turner.sizes...)
	turner.mu.Unlock()
	if len(sizes) != 2 {
		t.Fatalf("backend calls = %d, want one failed request plus one bounded recovery: %v", len(sizes), sizes)
	}
	if sizes[1] >= sizes[0] {
		t.Fatalf("recovery blindly resent oversized history: %v", sizes)
	}
	events := s.log.Snapshot()
	if countType(events, event.ContextViewChanged) != 1 {
		t.Fatal("bounded overflow recovery should select exactly one fresh view")
	}
	var terminal event.Event
	for _, ev := range events {
		if ev.Type == event.SessionError {
			terminal = ev
		}
	}
	if terminal.Data["action"] != "switch_model" || !strings.Contains(str(terminal.Data, "msg"), "larger context window") {
		t.Fatalf("second-overflow recovery action = %#v, want actionable model switch", terminal.Data)
	}
	if err := s.Rollover(context.Background()); err == nil || !strings.Contains(err.Error(), "larger context window") {
		t.Fatalf("explicit rollover after compact view = %v, want safe model-switch guidance", err)
	}
	if err := s.Resume(); err == nil || !strings.Contains(err.Error(), "instead of retrying the unchanged request") {
		t.Fatalf("Resume after compact overflow = %v, want model-switch gate", err)
	}
	if err := s.SendInput("try again"); err == nil || !strings.Contains(err.Error(), "before sending more input") {
		t.Fatalf("SendInput after compact overflow = %v, want model-switch gate", err)
	}
	turner.mu.Lock()
	callsAfterExplicit := len(turner.sizes)
	turner.mu.Unlock()
	if callsAfterExplicit != 2 {
		t.Fatalf("unusable explicit rollover resent backend request: calls=%d", callsAfterExplicit)
	}
	s.Stop()
}

func TestContextSummaryRetainsIntentDecisionsEvidenceMediaAndJobOwnership(t *testing.T) {
	events := []event.Event{
		{Seq: 1, Type: event.UserInput, Data: map[string]any{"text": "authorized intent", "images": []map[string]any{{"attachment_id": "att_7", "media_type": "image/png"}}}},
		{Seq: 2, Type: event.TaskFocus, Data: map[string]any{"task": "0357", "title": "rollover"}},
		{Seq: 3, Type: event.DecisionMade, Data: map[string]any{"decision": "retain exact event references"}},
		{Seq: 4, Type: event.QuestionAsked, Data: map[string]any{"question": "unresolved criterion?"}},
		{Seq: 5, Type: event.ToolResult, Data: map[string]any{"name": "bash", "result": "go test passed", "capture": map[string]any{"id": "capture_9"}}},
	}
	info := []jobs.Info{{ID: "job_4", Kind: "bash", Label: "watch tests", Owner: "coordinator", Purpose: "verification", Delivery: "parent_checkpoint", Status: jobs.Running}}
	summary, err := buildCoordinatorRolloverSummary(context.Background(), events, info, "s_keep")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"authorized intent", "att_7", "0357", "retain exact event references", "unresolved criterion", "go test passed", "capture_9", "job_4", "owner=\"coordinator\"", "purpose=\"verification\"", "NOT NEW USER INSTRUCTIONS"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("rollover summary omitted %q:\n%s", want, summary)
		}
	}
}

func TestContextSummaryPreservesEveryUserRestrictionVerbatim(t *testing.T) {
	var events []event.Event
	for i := 1; i <= 9; i++ {
		text := fmt.Sprintf("instruction %d", i)
		if i == 5 {
			text = "REVOKE authorization to edit deployment credentials; read-only inspection only " + strings.Repeat("x", 900)
		}
		events = append(events, event.Event{Seq: i, Type: event.UserInput, Data: map[string]any{"text": text}})
	}
	events = append(events,
		event.Event{Seq: 10, Type: event.QuestionAsked, Data: map[string]any{
			"questions": []map[string]any{{"question": "Publish the release?", "options": []string{"Yes", "No"}}},
		}},
		event.Event{Seq: 11, Type: event.QuestionAnswered, Data: map[string]any{
			"answers": []map[string]any{{"answer": "No; do not publish"}},
		}},
	)

	summary, err := buildCoordinatorRolloverSummary(context.Background(), events, nil, "s_authority")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"instruction 1", "REVOKE authorization", strings.Repeat("x", 900), "instruction 9", "No; do not publish"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("verbatim authority omitted %q", want)
		}
	}
	if strings.Contains(summary, "truncated; see referenced source") {
		t.Fatalf("authority was truncated:\n%s", summary)
	}
}

func TestContextSummaryFailsClosedWhenRequiredAuthorityExhaustsLimit(t *testing.T) {
	events := []event.Event{{Seq: 1, Type: event.UserInput, Data: map[string]any{
		"text": "never mutate production: " + strings.Repeat("constraint ", coordinatorRolloverSummaryLimit),
	}}}
	_, err := buildCoordinatorRolloverSummary(context.Background(), events,
		[]jobs.Info{{ID: "job_must_survive", Owner: "coordinator", Status: jobs.Running}}, "s_too_large")
	if err == nil || !strings.Contains(err.Error(), "required verbatim user authority") {
		t.Fatalf("irreducible authority summary error = %v", err)
	}
}

func TestContextSummaryBoundsEvidenceWithoutConsumingLiveOwnership(t *testing.T) {
	var events []event.Event
	for i := 1; i <= 100; i++ {
		events = append(events, event.Event{Seq: i, Type: event.ToolResult, Data: map[string]any{
			"name": "bash", "result": fmt.Sprintf("evidence-%03d %s", i, strings.Repeat("z", 800)),
		}})
	}
	summary, err := buildCoordinatorRolloverSummary(context.Background(), events,
		[]jobs.Info{{ID: "job_after_exhaustion", Owner: "coordinator", Kind: "bash", Status: jobs.Running}}, "s_sections")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"source events are omitted", "job_after_exhaustion", "owner=\"coordinator\"", "evidence-100"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("bounded section omitted %q:\n%s", want, summary)
		}
	}
}

func TestFailedContextSummaryLeavesSelectedViewUnchanged(t *testing.T) {
	s := newStopSession(t)
	s.status = event.StatusIdle
	s.rolloverCh = make(chan *rolloverRequest, 1)
	s.loop = &engine.Loop{Model: "test", Tools: tools.New(), Emitter: s.emitter}
	original := []gollama.Message{{Role: "user", Content: strings.Repeat("history", 10_000)}}
	s.loop.SetHistory(original)
	s.contextSummary = func(context.Context, []event.Event, []jobs.Info, string) (string, error) {
		return "", errors.New("summary backend failed")
	}

	// Simulate the idle run-owner branch; Rollover itself must never mutate the
	// selected history from this RPC goroutine.
	go func() {
		req := <-s.rolloverCh
		err := s.performRolloverRequest(req, "explicit")
		_, err = s.releaseRolloverInputs(err, false)
		req.complete(err)
	}()
	err := s.Rollover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "summary backend failed") {
		t.Fatalf("Rollover error = %v", err)
	}
	if got := s.loop.History(); !reflect.DeepEqual(got, original) {
		t.Fatalf("failed summary changed selected history: %#v", got)
	}
	if countType(s.log.Snapshot(), event.ContextViewChanged) != 0 {
		t.Fatal("failed summary recorded a context transition")
	}
	s.Stop()
}

type captureFinalTurner struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
	seen    []gollama.Message
}

func (t *captureFinalTurner) TurnCtx(_ context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	t.mu.Lock()
	t.calls++
	t.seen = append([]gollama.Message(nil), opts.Messages...)
	entered, release := t.entered, t.release
	t.mu.Unlock()
	if entered != nil {
		select {
		case <-entered:
		default:
			close(entered)
		}
	}
	if release != nil {
		<-release
	}
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{Role: "assistant", Content: "authoritative final report"}}}}, nil
}

func TestContextViewAppendFailureLeavesInMemorySelectionUnchanged(t *testing.T) {
	s := newStopSession(t)
	recorder := &sessionFailAtRecorder{failAt: event.ContextViewChanged}
	s.emitter = event.NewEmitter(recorder, "coordinator")
	s.loop = &engine.Loop{Model: "test", Tools: tools.New(), Emitter: s.emitter}
	original := []gollama.Message{{Role: "user", Content: strings.Repeat("old durable view ", 4_000)}}
	s.loop.SetHistory(original)

	err := s.performContextRollover(context.Background(), "explicit")
	if err == nil || !strings.Contains(err.Error(), "persist coordinator context rollover") {
		t.Fatalf("append failure = %v", err)
	}
	if got := s.loop.History(); !reflect.DeepEqual(got, original) {
		t.Fatalf("failed append changed selected history: %#v", got)
	}
	if len(recorder.events) != 0 {
		t.Fatalf("failed context transition reported durable: %#v", recorder.events)
	}
	s.cancel()
	_ = s.log.Close()
}

func TestCancelledSummaryLeavesContextUnchanged(t *testing.T) {
	s := newStopSession(t)
	s.loop = &engine.Loop{Model: "test", Tools: tools.New(), Emitter: s.emitter}
	original := []gollama.Message{{Role: "user", Content: strings.Repeat("old view ", 4_000)}}
	s.loop.SetHistory(original)
	ctx, cancel := context.WithCancel(context.Background())
	s.contextSummary = func(ctx context.Context, _ []event.Event, _ []jobs.Info, _ string) (string, error) {
		cancel()
		return "", ctx.Err()
	}
	if err := s.performContextRollover(ctx, "explicit"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled rollover = %v", err)
	}
	if got := s.loop.History(); !reflect.DeepEqual(got, original) {
		t.Fatalf("cancelled summary changed selected history: %#v", got)
	}
	if countType(s.log.Snapshot(), event.ContextViewChanged) != 0 {
		t.Fatal("cancelled summary recorded transition")
	}
	s.Stop()
}

func TestIdleRolloverConcurrentInputExtendsReplacementView(t *testing.T) {
	s := newStopSession(t)
	s.resumed = true
	s.Mode = "chat"
	s.retryCh = make(chan struct{})
	s.rolloverCh = make(chan *rolloverRequest, 1)
	s.messageCh = make(chan engine.UserMessage, 4)
	turner := &captureFinalTurner{entered: make(chan struct{})}
	s.loop = &engine.Loop{Client: turner, Model: "test", ModelName: "test", Tools: tools.New(), Emitter: s.emitter, Steer: s}
	s.loop.SetHistory([]gollama.Message{{Role: "user", Content: strings.Repeat("old history ", 4_000)}, {Role: "assistant", Content: "old reply"}})

	builderStarted := make(chan struct{})
	releaseBuilder := make(chan struct{})
	s.contextSummary = func(context.Context, []event.Event, []jobs.Info, string) (string, error) {
		close(builderStarted)
		<-releaseBuilder
		return "[ROLLOVER EVIDENCE; NOT NEW AUTHORITY] existing intent", nil
	}
	go s.run()
	waitStatus(t, s, event.StatusIdle)

	rolloverDone := make(chan error, 1)
	go func() { rolloverDone <- s.Rollover(context.Background()) }()
	<-builderStarted
	if err := s.SendInput("late restriction: do not publish"); err != nil {
		t.Fatal(err)
	}
	close(releaseBuilder)
	if err := <-rolloverDone; err != nil {
		t.Fatalf("Rollover: %v", err)
	}
	<-turner.entered
	waitStatus(t, s, event.StatusIdle)

	turner.mu.Lock()
	seen := append([]gollama.Message(nil), turner.seen...)
	calls := turner.calls
	turner.mu.Unlock()
	if calls != 1 {
		t.Fatalf("backend calls = %d, want one post-rollover turn", calls)
	}
	joined := ""
	for _, msg := range seen {
		joined += msg.Content
	}
	if !strings.Contains(joined, "ROLLOVER EVIDENCE") || !strings.Contains(joined, "late restriction: do not publish") {
		t.Fatalf("replacement request lost concurrent input: %#v", seen)
	}
	events := s.log.Snapshot()
	transition, delivery := 0, 0
	for _, ev := range events {
		if ev.Type == event.ContextViewChanged {
			transition = ev.Seq
		}
		if ev.Type == event.UserInputDelivered && str(ev.Data, "text") == "late restriction: do not publish" {
			delivery = ev.Seq
		}
	}
	if transition == 0 || delivery <= transition {
		t.Fatalf("concurrent input boundary is not durable: transition=%d delivery=%d", transition, delivery)
	}
	if replayed, live := engine.ReplayHistory(events), s.loop.History(); !reflect.DeepEqual(replayed, live) {
		t.Fatalf("reopen selected view differs from live\nreplay=%#v\nlive=%#v", replayed, live)
	}
	s.Stop()
}

type multiToolRolloverTurner struct {
	mu    sync.Mutex
	calls int
}

func (t *multiToolRolloverTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	if t.calls == 1 {
		return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{Role: "assistant", ToolCalls: []gollama.ToolCall{
			{ID: "one", Type: "function", Function: gollama.ToolCallFunction{Name: "first", Arguments: `{}`}},
			{ID: "two", Type: "function", Function: gollama.ToolCallFunction{Name: "second", Arguments: `{}`}},
		}}}}}, nil
	}
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{Message: gollama.Message{Role: "assistant", Content: "done after rollover"}}}}, nil
}

func TestExplicitRolloverWaitsForCompleteMultiToolBatch(t *testing.T) {
	s := newStopSession(t)
	s.Mode = "chat"
	s.prompt = "authorized task"
	s.retryCh = make(chan struct{})
	s.rolloverCh = make(chan *rolloverRequest, 1)
	s.messageCh = make(chan engine.UserMessage, 4)
	turner := &multiToolRolloverTurner{}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondRan := make(chan struct{})
	reg := tools.New()
	reg.Add(&gollama.Tool{Name: "first", Params: tools.Obj(map[string]any{}), Call: func(context.Context, any) (*gollama.ToolResult, error) {
		close(firstStarted)
		<-releaseFirst
		return &gollama.ToolResult{Content: "first mutation complete"}, nil
	}})
	reg.Add(&gollama.Tool{Name: "second", Params: tools.Obj(map[string]any{}), Call: func(context.Context, any) (*gollama.ToolResult, error) {
		close(secondRan)
		return &gollama.ToolResult{Content: "second mutation complete"}, nil
	}})
	s.loop = &engine.Loop{Client: turner, Model: "test", ModelName: "test", Tools: reg, Emitter: s.emitter, Steer: s}
	s.loop.SetHistory([]gollama.Message{{Role: "user", Content: strings.Repeat("long history ", 4_000)}})
	go s.run()
	<-firstStarted

	rolloverDone := make(chan error, 1)
	go func() { rolloverDone <- s.Rollover(context.Background()) }()
	close(releaseFirst)
	select {
	case <-secondRan:
	case <-time.After(time.Second):
		t.Fatal("second tool did not run before rollover")
	}
	if err := <-rolloverDone; err != nil {
		t.Fatalf("Rollover: %v", err)
	}
	waitStatus(t, s, event.StatusIdle)
	events := s.log.Snapshot()
	transition := 0
	resultsBefore := 0
	for _, ev := range events {
		if ev.Type == event.ContextViewChanged {
			transition = ev.Seq
		}
		if ev.Type == event.ToolResult && transition == 0 {
			resultsBefore++
		}
	}
	if transition == 0 || resultsBefore != 2 {
		t.Fatalf("rollover crossed tool batch: transition=%d results_before=%d", transition, resultsBefore)
	}
	s.Stop()
}

func TestRolloverRacingCompletedResultDoesNotCreateAnotherTurn(t *testing.T) {
	s := newStopSession(t)
	s.Mode = "chat"
	s.prompt = "finish once"
	s.retryCh = make(chan struct{})
	s.rolloverCh = make(chan *rolloverRequest, 1)
	s.messageCh = make(chan engine.UserMessage, 4)
	turner := &captureFinalTurner{entered: make(chan struct{}), release: make(chan struct{})}
	s.loop = &engine.Loop{Client: turner, Model: "test", ModelName: "test", Tools: tools.New(), Emitter: s.emitter, Steer: s}
	s.loop.SetHistory([]gollama.Message{{Role: "user", Content: strings.Repeat("history ", 4_000)}})
	go s.run()
	<-turner.entered

	rolloverDone := make(chan error, 1)
	go func() { rolloverDone <- s.Rollover(context.Background()) }()
	// Ensure the request is queued while the only model turn is still in flight.
	deadline := time.Now().Add(time.Second)
	for len(s.rolloverCh) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(turner.release)
	if err := <-rolloverDone; err == nil || !strings.Contains(err.Error(), "completed the turn") {
		t.Fatalf("racing rollover = %v, want authoritative-result rejection", err)
	}
	waitStatus(t, s, event.StatusIdle)
	turner.mu.Lock()
	calls := turner.calls
	turner.mu.Unlock()
	if calls != 1 || countType(s.log.Snapshot(), event.ContextViewChanged) != 0 {
		t.Fatalf("racing rollover caused another turn/view: calls=%d transitions=%d", calls, countType(s.log.Snapshot(), event.ContextViewChanged))
	}
	var report string
	for _, ev := range s.log.Snapshot() {
		if ev.Type == event.SessionIdle {
			report = str(ev.Data, "report")
		}
	}
	if report != "authoritative final report" {
		t.Fatalf("authoritative report lost: %q", report)
	}
	s.Stop()
}

func TestExplicitRolloverRejectsPendingQuestion(t *testing.T) {
	s := newStopSession(t)
	s.status = event.StatusIdle
	s.rolloverCh = make(chan *rolloverRequest, 1)
	s.loop = &engine.Loop{Model: "test", Tools: tools.New(), Emitter: s.emitter}

	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = s.inter.Ask(s.ctx, "still need approval?", nil)
	}()
	<-started
	deadline := time.Now().Add(time.Second)
	for !s.inter.pending() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := s.Rollover(context.Background()); err == nil || !strings.Contains(err.Error(), "pending question") {
		t.Fatalf("Rollover with pending question = %v", err)
	}
	s.cancel()
	s.log.Close()
}

func TestExplicitRolloverRejectsPausedAndPendingPauseWithoutResuming(t *testing.T) {
	for _, tc := range []struct {
		name     string
		paused   bool
		pauseReq bool
	}{
		{name: "paused", paused: true},
		{name: "pause_requested", pauseReq: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStopSession(t)
			s.status = event.StatusPaused
			s.rolloverCh = make(chan *rolloverRequest, 1)
			s.steerMu.Lock()
			s.paused, s.pauseReq = tc.paused, tc.pauseReq
			s.steerMu.Unlock()

			err := s.Rollover(context.Background())
			if err == nil || !strings.Contains(err.Error(), "resume the paused session") {
				t.Fatalf("Rollover = %v, want paused guidance", err)
			}
			s.steerMu.Lock()
			if s.resumeReq || s.paused != tc.paused || s.pauseReq != tc.pauseReq || s.rolloverPending {
				t.Fatalf("failed rollover changed pause state: paused=%t pauseReq=%t resumeReq=%t rollover=%t",
					s.paused, s.pauseReq, s.resumeReq, s.rolloverPending)
			}
			s.steerMu.Unlock()
			if len(s.rolloverCh) != 0 {
				t.Fatal("paused rollover was queued")
			}
			s.Stop()
		})
	}
}

func TestPauseRacingAcceptedRolloverFailsWithoutBlockingRollover(t *testing.T) {
	s := newStopSession(t)
	s.resumed = true
	s.Mode = "chat"
	s.retryCh = make(chan struct{})
	s.rolloverCh = make(chan *rolloverRequest, 1)
	s.messageCh = make(chan engine.UserMessage, 4)
	s.loop = &engine.Loop{Client: &captureFinalTurner{}, Model: "test", ModelName: "test", Tools: tools.New(), Emitter: s.emitter, Steer: s}
	s.loop.SetHistory([]gollama.Message{{Role: "user", Content: strings.Repeat("old ", 4_000)}})
	started, release := make(chan struct{}), make(chan struct{})
	s.contextSummary = func(context.Context, []event.Event, []jobs.Info, string) (string, error) {
		close(started)
		<-release
		return "[compact durable evidence]", nil
	}
	go s.run()
	waitStatus(t, s, event.StatusIdle)
	done := make(chan error, 1)
	go func() { done <- s.Rollover(context.Background()) }()
	<-started
	if err := s.Interrupt(); err == nil || !strings.Contains(err.Error(), "rollover is pending") {
		t.Fatalf("Interrupt racing rollover = %v", err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Rollover: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted rollover hung behind racing pause")
	}
	s.Stop()
}

func TestRolloverFailsClosedForLiveAndReplayedMedia(t *testing.T) {
	t.Run("live native image", func(t *testing.T) {
		s := newStopSession(t)
		s.loop = &engine.Loop{Model: "test", Tools: tools.New(), Emitter: s.emitter}
		s.loop.SetHistory([]gollama.Message{{Role: "user", MultiContent: []gollama.ContentBlock{{Type: "image", ImageBase64: "aW1hZ2U=", ImageMediaType: "image/png"}}}})
		err := s.performContextRollover(context.Background(), "explicit")
		if err == nil || !strings.Contains(err.Error(), "re-attach the media") {
			t.Fatalf("live-media rollover = %v", err)
		}
		if countType(s.log.Snapshot(), event.ContextViewChanged) != 0 {
			t.Fatal("live media selected a lossy compact view")
		}
		s.Stop()
	})

	t.Run("reopened attachment loss", func(t *testing.T) {
		s := newStopSession(t)
		s.emitter.EmitAs("user", event.UserInput, map[string]any{
			"text":   "what does this show?",
			"images": []map[string]any{{"attachment_id": "att_1", "media_type": "image/png"}},
		})
		events := s.log.Snapshot()
		s.loop = &engine.Loop{Model: "test", Tools: tools.New(), Emitter: s.emitter}
		s.loop.SetHistory(engine.ReplayHistory(events))
		if !strings.Contains(s.loop.History()[0].Content, "bytes are unavailable") {
			t.Fatalf("test did not reconstruct attachment-loss evidence: %#v", s.loop.History())
		}
		err := s.performContextRollover(context.Background(), "explicit")
		if err == nil || !strings.Contains(err.Error(), "re-attach the media") {
			t.Fatalf("replayed-media rollover = %v", err)
		}
		if countType(s.log.Snapshot(), event.ContextViewChanged) != 0 {
			t.Fatal("replayed media selected a lossy compact view")
		}
		s.Stop()
	})
}

func TestContextSummaryPairsHumanAuthorityAndLabelsAutomaticAnswers(t *testing.T) {
	// Put both pairs before enough later evidence to evict them from any bounded
	// latest-evidence section. Human authority must still retain the complete pair.
	events := []event.Event{
		{Seq: 1, Type: event.QuestionAsked, Data: map[string]any{"question": "Deploy?", "options": []string{"Yes", "No"}}},
		{Seq: 2, Type: event.QuestionAnswered, Data: map[string]any{"answer": "No", "confirmed": false}},
		{Seq: 3, Type: event.QuestionAsked, Data: map[string]any{"question": "Pick automatically", "auto": true}},
		{Seq: 4, Type: event.QuestionAnswered, Data: map[string]any{"answer": unattendedAutoAnswer, "auto": true}},
	}
	for i := 5; i <= 74; i++ {
		events = append(events, event.Event{Seq: i, Type: event.ToolResult, Data: map[string]any{"result": strings.Repeat("noise", 300)}})
	}
	summary, err := buildCoordinatorRolloverSummary(context.Background(), events, nil, "s_questions")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"human question #1 / answer #2", `"question":"Deploy?"`, `"options":["Yes","No"]`, `"answer":"No"`} {
		if !strings.Contains(summary, want) {
			t.Fatalf("human authority pair omitted %q:\n%s", want, summary)
		}
	}
	if !strings.Contains(summary, "SYSTEM-GENERATED UNATTENDED ASSUMPTIONS") || !strings.Contains(summary, "system assumption from question #3 / answer #4") {
		t.Fatalf("automatic answer was not separately labeled:\n%s", summary)
	}
	if strings.Contains(strings.Split(summary, "SYSTEM-GENERATED UNATTENDED ASSUMPTIONS")[0], unattendedAutoAnswer) {
		t.Fatal("automatic assumption was promoted to user authority")
	}

	_, err = buildCoordinatorRolloverSummary(context.Background(), []event.Event{{Seq: 1, Type: event.QuestionAnswered, Data: map[string]any{"answer": "Yes"}}}, nil, "s_missing_question")
	if err == nil || !strings.Contains(err.Error(), "no complete paired question") {
		t.Fatalf("unpaired human answer = %v, want fail closed", err)
	}
}

func TestFailedOrCancelledIdleRolloverPreservesConcurrentInputForReopen(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "summary_failure"
		if cancelled {
			name = "request_cancelled"
		}
		t.Run(name, func(t *testing.T) {
			s := newStopSession(t)
			s.resumed = true
			s.Mode = "chat"
			s.retryCh = make(chan struct{})
			s.rolloverCh = make(chan *rolloverRequest, 1)
			s.messageCh = make(chan engine.UserMessage, 4)
			s.emitter.EmitAs("user", event.UserInput, map[string]any{"text": strings.Repeat("old durable intent ", 100)})
			s.emitter.Emit(event.ModelTurn, map[string]any{"text": "old answer"})
			turner := &captureFinalTurner{entered: make(chan struct{})}
			s.loop = &engine.Loop{Client: turner, Model: "test", ModelName: "test", Tools: tools.New(), Emitter: s.emitter, Steer: s}
			s.loop.SetHistory(engine.ReplayHistory(s.log.Snapshot()))

			started, release := make(chan struct{}), make(chan struct{})
			s.contextSummary = func(ctx context.Context, _ []event.Event, _ []jobs.Info, _ string) (string, error) {
				close(started)
				if cancelled {
					<-ctx.Done()
					return "", ctx.Err()
				}
				<-release
				return "", errors.New("summary failed")
			}
			go s.run()
			waitStatus(t, s, event.StatusIdle)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- s.Rollover(ctx) }()
			<-started
			if err := s.SendInput("late restriction survives"); err != nil {
				t.Fatal(err)
			}
			if cancelled {
				cancel()
			} else {
				close(release)
			}
			if err := <-done; err == nil {
				t.Fatal("failed/cancelled rollover unexpectedly succeeded")
			}
			select {
			case <-turner.entered:
			case <-time.After(time.Second):
				t.Fatal("concurrent input remained stranded after rollover failure")
			}
			waitStatus(t, s, event.StatusIdle)
			events := s.log.Snapshot()
			if countType(events, event.ContextViewChanged) != 0 {
				t.Fatal("failed/cancelled rollover selected a new view")
			}
			if replayed, live := engine.ReplayHistory(events), s.loop.History(); !reflect.DeepEqual(replayed, live) {
				t.Fatalf("failure-path reopen differs from live\nreplay=%#v\nlive=%#v", replayed, live)
			}
			s.Stop()
		})
	}
}
