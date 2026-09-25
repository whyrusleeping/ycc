package session

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// captureTurner records the messages of each request and answers with plain text.
type captureTurner struct {
	mu   sync.Mutex
	reqs [][]gollama.Message
}

func (c *captureTurner) TurnCtx(_ context.Context, o gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, append([]gollama.Message(nil), o.Messages...))
	c.mu.Unlock()
	return &gollama.ResponseMessageGenerate{Choices: []gollama.GenChoice{{
		Message: gollama.Message{Role: "assistant", Content: "thanks, continuing"},
	}}}, nil
}

func (c *captureTurner) requests() [][]gollama.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]gollama.Message(nil), c.reqs...)
}

// askUserTail is a log that ended while the coordinator waited in a
// one-question batch ask_user (the shape that stranded the vals session).
func askUserTail() []event.Event {
	return []event.Event{
		{Seq: 1, Type: event.SessionStarted, Actor: "coordinator", Data: map[string]any{"mode": "work"}},
		{Seq: 2, Type: event.UserInput, Actor: "user", Data: map[string]any{"text": "go"}},
		{Seq: 3, Type: event.ModelTurn, Actor: "coordinator", Data: map[string]any{"text": "", "tool_calls": 1}},
		{Seq: 4, Type: event.ToolCall, Actor: "coordinator", Data: map[string]any{"name": "ask_user", "id": "toolu_ask", "args": `{"questions":[{"question":"how?"}]}`}},
		{Seq: 5, Type: event.QuestionAsked, Actor: "coordinator", Data: map[string]any{
			"questions": []any{map[string]any{"question": "how?", "options": []any{"restore", "leave", "stop"}}},
		}},
	}
}

func TestFindResumableQuestion(t *testing.T) {
	base := askUserTail()
	if q := findResumableQuestion(base); q == nil || !q.batch || q.callID != "toolu_ask" ||
		len(q.questions) != 1 || len(q.questions[0].Options) != 3 {
		t.Fatalf("batch tail: got %+v", q)
	}

	single := append(append([]event.Event(nil), base[:4]...), event.Event{Seq: 5, Type: event.QuestionAsked, Actor: "coordinator",
		Data: map[string]any{"question": "proceed?", "options": []string{"yes", "no"}}})
	if q := findResumableQuestion(single); q == nil || q.batch || q.questions[0].Prompt != "proceed?" {
		t.Fatalf("single tail: got %+v", q)
	}

	// Non-coordinator/lifecycle events after the question don't disturb it.
	withReopen := append(append([]event.Event(nil), base...),
		event.Event{Seq: 6, Type: event.SessionReopened, Actor: "coordinator"},
		event.Event{Seq: 7, Type: event.ModelTurn, Actor: "implementer"})
	if findResumableQuestion(withReopen) == nil {
		t.Fatal("reopen marker / subagent turn should not clear the question")
	}

	for name, tail := range map[string]event.Event{
		"answered":    {Type: event.QuestionAnswered, Actor: "coordinator", Data: map[string]any{"answers": []any{"x"}}},
		"tool result": {Type: event.ToolResult, Actor: "coordinator", Data: map[string]any{"id": "toolu_ask", "result": "x"}},
		"model turn":  {Type: event.ModelTurn, Actor: "coordinator", Data: map[string]any{"text": "moving on"}},
		"user input":  {Type: event.UserInput, Actor: "user", Data: map[string]any{"text": "hi"}},
	} {
		evs := append(append([]event.Event(nil), base...), tail)
		if q := findResumableQuestion(evs); q != nil {
			t.Errorf("%s after question: want nil, got %+v", name, q)
		}
	}

	// A Confirm gate raised by another tool is not restored.
	confirm := append([]event.Event(nil), base...)
	confirm[3] = event.Event{Seq: 4, Type: event.ToolCall, Actor: "coordinator", Data: map[string]any{"name": "start_work", "id": "c9"}}
	if q := findResumableQuestion(confirm); q != nil {
		t.Fatalf("confirm gate restored: %+v", q)
	}

	// Auto-answered (unattended) questions are not restored.
	auto := append([]event.Event(nil), base...)
	auto[4] = event.Event{Seq: 5, Type: event.QuestionAsked, Actor: "coordinator", Data: map[string]any{"question": "q", "auto": true}}
	if q := findResumableQuestion(auto); q != nil {
		t.Fatalf("auto question restored: %+v", q)
	}
}

// A reopened session whose log ended in ask_user re-arms the question: it is
// pending immediately (before run starts), the answer is recorded as
// question_answered + the ask_user tool_result, and the model's next request
// sees the real answer instead of the replay placeholder. A later replay of the
// continued log pairs the recorded result, too.
func TestReopenRestoresPendingQuestion(t *testing.T) {
	log, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range askUserTail() {
		log.Record(ev.Actor, ev.Type, ev.Data)
	}
	events := log.Snapshot()

	em := event.NewEmitter(log, "coordinator")
	s := newStopSession(t)
	s.log = log
	s.emitter = em
	s.inter = newInteraction(false, em)
	s.Mode = "chat"
	s.resumed = true
	turner := &captureTurner{}
	loop := &engine.Loop{Client: turner, Model: "test", Tools: tools.New(), Emitter: em, Steer: s}
	loop.SetHistory(engine.ReplayHistory(events))
	s.loop = loop

	if !s.restoreQuestion(findResumableQuestion(events), loop.History()) {
		t.Fatal("restoreQuestion did not arm the trailing ask_user")
	}
	if !s.PendingQuestion() {
		t.Fatal("restored question must be pending before run starts")
	}
	go s.run()
	defer s.Stop()

	// iOS answers a one-question batch through AnswerQuestion (AnswerOption).
	if err := s.AnswerOption(2, ""); err != nil {
		t.Fatalf("AnswerOption: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !hasType(s.log.Snapshot(), event.SessionIdle) {
		if time.Now().After(deadline) {
			t.Fatal("session never went idle after the restored answer")
		}
		time.Sleep(time.Millisecond)
	}

	reqs := turner.requests()
	if len(reqs) != 1 {
		t.Fatalf("model requests = %d, want 1", len(reqs))
	}
	var toolMsg *gollama.Message
	for i := range reqs[0] {
		if reqs[0][i].Role == "tool" {
			toolMsg = &reqs[0][i]
		}
	}
	want := "Q1: how?\nA1: stop"
	if toolMsg == nil || toolMsg.Content != want {
		t.Fatalf("tool message sent to model = %+v, want content %q", toolMsg, want)
	}

	snap := s.log.Snapshot()
	var reopened, answered, result *event.Event
	for i := range snap {
		switch snap[i].Type {
		case event.SessionReopened:
			reopened = &snap[i]
		case event.QuestionAnswered:
			answered = &snap[i]
		case event.ToolResult:
			result = &snap[i]
		}
	}
	if reopened == nil || reopened.Data["question_restored"] != true {
		t.Fatalf("session_reopened should mark question_restored: %+v", reopened)
	}
	if answered == nil || result == nil || str(result.Data, "id") != "toolu_ask" || str(result.Data, "result") != want {
		t.Fatalf("answer not recorded: answered=%+v result=%+v", answered, result)
	}
	if answered.Seq > result.Seq {
		t.Fatal("question_answered must precede the tool_result")
	}
	for _, m := range engine.ReplayHistory(snap) {
		if m.Role == "tool" && m.Content != want {
			t.Fatalf("replay of continued log has tool content %q, want %q", m.Content, want)
		}
	}
}

// Stopping a session waiting on a restored question unblocks cleanly and
// records no answer.
func TestRestoredQuestionStop(t *testing.T) {
	log, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range askUserTail() {
		log.Record(ev.Actor, ev.Type, ev.Data)
	}
	events := log.Snapshot()
	em := event.NewEmitter(log, "coordinator")
	s := newStopSession(t)
	s.log, s.emitter, s.inter, s.Mode, s.resumed = log, em, newInteraction(false, em), "chat", true
	turner := &captureTurner{}
	loop := &engine.Loop{Client: turner, Model: "test", Tools: tools.New(), Emitter: em, Steer: s}
	loop.SetHistory(engine.ReplayHistory(events))
	s.loop = loop
	if !s.restoreQuestion(findResumableQuestion(events), loop.History()) {
		t.Fatal("not restored")
	}
	done := make(chan struct{})
	go func() { s.run(); close(done) }()
	for !hasType(s.log.Snapshot(), event.SessionReopened) {
		time.Sleep(time.Millisecond)
	}
	s.Stop()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after Stop")
	}
	if hasType(s.log.Snapshot(), event.QuestionAnswered) || len(turner.requests()) != 0 {
		t.Fatal("stop must not answer the question or run the model")
	}
}

// ReopenForAnswer reopens a persisted session only when its log still ends on
// an unanswered ask_user; otherwise it leaves the transcript closed.
func TestReopenForAnswer(t *testing.T) {
	ws := t.TempDir()
	if _, err := git.Open(ws); err != nil {
		t.Fatal(err)
	}
	absWS, _ := filepath.Abs(ws)
	tail := askUserTail()
	tail[0].Data = map[string]any{"mode": "work", "workspace": absWS}
	writeSession(t, ws, "s_waiting", tail)
	writeSession(t, ws, "s_done", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "work", "workspace": absWS}},
		{Seq: 2, TS: ts(2), Actor: "user", Type: event.UserInput, Data: map[string]any{"text": "go"}},
		{Seq: 3, TS: ts(3), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{"text": "done"}},
	})
	m := NewManager(testRegistry(), ws)

	if _, err := m.ReopenForAnswer("s_done"); !errors.Is(err, ErrNoPendingQuestion) {
		t.Fatalf("finished session: err = %v, want ErrNoPendingQuestion", err)
	}
	if _, ok := m.Get("s_done"); ok {
		t.Fatal("answering must not reopen a session with no pending question")
	}
	if _, err := m.ReopenForAnswer("s_missing"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("missing session: err = %v, want ErrUnknownSession", err)
	}

	s, err := m.ReopenForAnswer("s_waiting")
	if err != nil {
		t.Fatalf("ReopenForAnswer: %v", err)
	}
	defer s.Stop()
	if !s.PendingQuestion() {
		t.Fatal("reopened session should be waiting on the restored question")
	}
	if got, ok := m.Get("s_waiting"); !ok || got != s {
		t.Fatal("reopened session not registered live")
	}
}
