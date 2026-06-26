package session

import (
	"context"
	"sync"

	"github.com/whyrusleeping/ycc/internal/event"
)

// interaction implements orchestrator.Asker, enforcing the session's interaction
// level (spec §11):
//   - autonomous: ask_user never blocks; the question is recorded as an assumption
//     and the agent is told to proceed on its own judgement.
//   - interactive / judgement: ask_user emits a question and blocks until the user
//     answers (via SendInput or AnswerQuestion) or the session is cancelled.
type interaction struct {
	level   string
	emitter *event.Emitter
	answer  chan string

	mu          sync.Mutex
	pending     bool
	assumptions []string
}

func newInteraction(level string, emitter *event.Emitter) *interaction {
	return &interaction{level: level, emitter: emitter, answer: make(chan string, 1)}
}

// Ask implements orchestrator.Asker.
func (in *interaction) Ask(ctx context.Context, question string, _ []string) (string, error) {
	if in.level == "autonomous" {
		const ans = "You are in autonomous mode and no human is available. Proceed using your best judgement."
		in.emitter.Emit(event.QuestionAsked, map[string]any{"question": question, "auto": true})
		in.mu.Lock()
		in.assumptions = append(in.assumptions, question)
		in.mu.Unlock()
		in.emitter.Emit(event.QuestionAnswered, map[string]any{"answer": ans, "auto": true})
		return ans, nil
	}

	in.mu.Lock()
	in.pending = true
	in.mu.Unlock()
	in.emitter.Emit(event.QuestionAsked, map[string]any{"question": question})

	select {
	case ans := <-in.answer:
		in.clearPending()
		in.emitter.Emit(event.QuestionAnswered, map[string]any{"answer": ans})
		return ans, nil
	case <-ctx.Done():
		in.clearPending()
		return "", ctx.Err()
	}
}

// Answer delivers a user answer to a pending question, returning true if one was
// pending and accepted.
func (in *interaction) Answer(text string) bool {
	in.mu.Lock()
	if !in.pending {
		in.mu.Unlock()
		return false
	}
	in.pending = false
	in.mu.Unlock()
	select {
	case in.answer <- text:
		return true
	default:
		return false
	}
}

// Assumptions returns the questions auto-answered in autonomous mode.
func (in *interaction) Assumptions() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.assumptions...)
}

func (in *interaction) clearPending() {
	in.mu.Lock()
	in.pending = false
	in.mu.Unlock()
}
