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
//
// Each pending question gets a fresh single-use channel held under the mutex, so
// an answer can never be buffered across questions or silently dropped.
type interaction struct {
	level   string
	emitter *event.Emitter

	mu          sync.Mutex
	waiting     chan string // non-nil only while a question is pending
	assumptions []string
}

func newInteraction(level string, emitter *event.Emitter) *interaction {
	return &interaction{level: level, emitter: emitter}
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

	ch := make(chan string, 1)
	in.mu.Lock()
	in.waiting = ch
	in.mu.Unlock()
	// Stop pointing at this channel when we leave, whatever the outcome.
	defer func() {
		in.mu.Lock()
		if in.waiting == ch {
			in.waiting = nil
		}
		in.mu.Unlock()
	}()

	in.emitter.Emit(event.QuestionAsked, map[string]any{"question": question})
	select {
	case ans := <-ch:
		in.emitter.Emit(event.QuestionAnswered, map[string]any{"answer": ans})
		return ans, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Answer delivers a user answer to the pending question, returning true if one
// was pending and accepted. It claims the pending channel under the lock so a
// duplicate or racing answer can't double-deliver.
func (in *interaction) Answer(text string) bool {
	in.mu.Lock()
	ch := in.waiting
	in.waiting = nil
	in.mu.Unlock()
	if ch == nil {
		return false
	}
	ch <- text // buffered(1), single sender, single use: never blocks
	return true
}

// Assumptions returns the questions auto-answered in autonomous mode.
func (in *interaction) Assumptions() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.assumptions...)
}
