package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/whyrusleeping/gollama"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// A human ask_user question must survive the live process that asked it. When a
// session is closed or the daemon restarts while the coordinator is blocked in
// ask_user, the log ends with tool_call(ask_user) + question_asked and no answer.
// Reopening such a session restores that exact question as a live pending gate —
// without re-emitting question_asked, since the durable log already holds it —
// instead of handing the model a "(no result recorded)" placeholder and letting
// it guess or re-ask. The user's answer is then recorded exactly like a live one
// (question_answered + the ask_user tool_result) and the model continues from it.

// ErrNoPendingQuestion reports that a session has no question awaiting an answer
// (live or restorable from its persisted log).
var ErrNoPendingQuestion = errors.New("no pending question")

// resumableQuestion is a trailing unanswered human ask_user question found in a
// persisted log.
type resumableQuestion struct {
	callID    string // raw recorded ask_user tool_call id
	batch     bool   // asked via the `questions` list form
	questions []orchestrator.Question

	// Filled in by Reopen once the replayed history's synthetic placeholder for
	// callID is located and the interaction gate is armed.
	historyCallID string
	wait          func(context.Context) ([]string, error)
}

// findResumableQuestion returns the question the log's coordinator conversation
// is still blocked on, or nil. It is deliberately strict: the last coordinator
// tool call must be ask_user with no recorded result, followed by its (non-auto)
// question_asked, with no later answer, model turn, tool activity, or user
// message. Confirm gates (question_asked raised by another tool) are not
// restored — their tool re-validates on its own terms.
func findResumableQuestion(events []event.Event) *resumableQuestion {
	var (
		q            *resumableQuestion
		lastCallID   string
		lastCallName string
		lastResolved bool
	)
	for _, ev := range events {
		switch ev.Type {
		case event.ToolCall:
			if ev.Actor != "coordinator" {
				continue
			}
			lastCallID, lastCallName, lastResolved = str(ev.Data, "id"), str(ev.Data, "name"), false
			q = nil
		case event.ToolResult:
			if ev.Actor != "coordinator" {
				continue
			}
			if id := str(ev.Data, "id"); id == "" || id == lastCallID {
				lastResolved = true
			}
			q = nil
		case event.QuestionAsked:
			if ev.Actor != "coordinator" {
				continue
			}
			q = nil
			if b, _ := ev.Data["auto"].(bool); b || lastCallName != "ask_user" || lastResolved || lastCallID == "" {
				continue
			}
			q = parseAskedQuestion(ev.Data)
			if q != nil {
				q.callID = lastCallID
			}
		case event.QuestionAnswered, event.ModelTurn, event.ContextViewChanged:
			if ev.Actor == "coordinator" {
				q = nil
			}
		case event.UserInput, event.UserInputDelivered, event.JobNotified, event.BudgetExceeded:
			q = nil
		}
	}
	return q
}

// parseAskedQuestion rebuilds the asked question(s) from a question_asked payload
// (single {"question","options"} or batch {"questions":[...]}).
func parseAskedQuestion(data map[string]any) *resumableQuestion {
	if raw, ok := data["questions"].([]any); ok {
		var qs []orchestrator.Question
		for _, item := range raw {
			qm, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if p := str(qm, "question"); p != "" {
				qs = append(qs, orchestrator.Question{Prompt: p, Options: strList(qm["options"])})
			}
		}
		if len(qs) == 0 {
			return nil
		}
		return &resumableQuestion{batch: true, questions: qs}
	}
	p := str(data, "question")
	if p == "" {
		return nil
	}
	return &resumableQuestion{questions: []orchestrator.Question{{Prompt: p, Options: strList(data["options"])}}}
}

func strList(v any) []string {
	switch l := v.(type) {
	case []string:
		return append([]string(nil), l...)
	case []any:
		var out []string
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// placeholderCallID finds the replayed history's synthetic result for the
// restored ask_user call on the trailing assistant turn and returns its
// (canonicalized) tool-call id, or "" when the history does not have the
// expected shape — in which case the question is not restored and reopen keeps
// its ordinary behavior.
func placeholderCallID(history []gollama.Message, rawID string) string {
	ai := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" {
			ai = i
			break
		}
	}
	if ai < 0 {
		return ""
	}
	id := ""
	for _, c := range history[ai].ToolCalls {
		if c.Function.Name != "ask_user" {
			continue
		}
		if c.ID == rawID {
			id = c.ID
			break
		}
		id = c.ID // canonicalized id: fall back to the turn's last ask_user call
	}
	if id == "" {
		return ""
	}
	for _, m := range history[ai+1:] {
		if m.Role == "tool" && m.ToolCallID == id && m.Content == engine.DanglingToolResult {
			return id
		}
	}
	return ""
}

// restoreQuestion arms the restored question on the session's interaction gate.
// Called by Reopen before the session is registered, so an answer RPC racing
// the run goroutine's start can never see "no pending question".
func (s *Session) restoreQuestion(rq *resumableQuestion, history []gollama.Message) bool {
	if rq == nil || s.inter == nil || s.inter.unattended {
		return false
	}
	rq.historyCallID = placeholderCallID(history, rq.callID)
	if rq.historyCallID == "" {
		return false
	}
	rq.wait = s.inter.restore(rq.questions, rq.batch)
	s.resumeQuestion = rq
	return true
}

// awaitRestoredQuestion blocks the reopened run until the restored question is
// answered, records the ask_user tool result, and swaps the answer into the
// model history in place of the replay placeholder. It returns false when the
// session is cancelled or its log fails first.
func (s *Session) awaitRestoredQuestion(rq *resumableQuestion) bool {
	s.setStatus(event.StatusRunning)
	answers, err := rq.wait(s.ctx)
	if err != nil || s.logFailure() != nil {
		return false
	}
	result := ""
	if rq.batch {
		result = orchestrator.FormatAskManyAnswers(rq.questions, answers)
	} else if len(answers) > 0 {
		result = answers[0]
	}
	s.emitter.Emit(event.ToolResult, map[string]any{
		"name":     "ask_user",
		"result":   result,
		"error":    false,
		"id":       rq.callID,
		"restored": true,
	})
	if s.logFailure() != nil {
		return false
	}
	loop := s.currentLoop()
	h := loop.History()
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Role == "tool" && h[i].ToolCallID == rq.historyCallID {
			h[i].Content = result
			loop.SetHistory(h)
			break
		}
	}
	return true
}

// ReopenForAnswer reopens a persisted (not live) session so an answer RPC can
// reach the question its log is still blocked on — the case where the daemon
// restarted while the coordinator waited in ask_user. Answer RPCs carry no
// project, so the owning project is located by session id. A session whose log
// does not end in a restorable question is NOT reopened (ErrNoPendingQuestion):
// answering never resumes a finished transcript as a side effect.
func (m *Manager) ReopenForAnswer(id string) (*Session, error) {
	if s, ok := m.Get(id); ok {
		return s, nil
	}
	if id == "" || id == "." || filepath.Base(id) != id {
		return nil, fmt.Errorf("%w %q", ErrUnknownSession, id)
	}
	for _, p := range m.projects.List() {
		logPath := filepath.Join(p.Path, ".ycc", "sessions", id, "events.jsonl")
		if _, err := os.Stat(logPath); err != nil {
			continue
		}
		events, err := m.SessionTranscript(p.Name, id)
		if err != nil {
			return nil, err
		}
		if findResumableQuestion(events) == nil {
			return nil, fmt.Errorf("session %s: %w", id, ErrNoPendingQuestion)
		}
		s, err := m.Reopen(p.Name, id)
		if err != nil {
			return nil, err
		}
		if !s.PendingQuestion() {
			return nil, fmt.Errorf("session %s: %w", id, ErrNoPendingQuestion)
		}
		return s, nil
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownSession, id)
}
