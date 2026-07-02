package session

import (
	"context"
	"strings"
	"sync"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
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
	options     []string    // suggested answers for the pending question, if any
	assumptions []string

	// batch question state (ask_user with multiple questions). Only one of the
	// single-question (waiting) or batch (batchWaiting) gates is pending at a time.
	batchWaiting   chan []string           // non-nil only while a batch is pending
	batchQuestions []orchestrator.Question // the pending batch, for option resolution
}

// answer is one user reply within a batch: idx selects an option from the
// matching question's Options when >= 0 and in range, otherwise text is free text.
type answer struct {
	idx  int
	text string
}

func newInteraction(level string, emitter *event.Emitter) *interaction {
	return &interaction{level: level, emitter: emitter}
}

// SetLevel updates the interaction level. It takes effect at the next Ask gate;
// a question already blocked is unaffected (spec §11, §18.2).
func (in *interaction) SetLevel(level string) {
	in.mu.Lock()
	in.level = level
	in.mu.Unlock()
}

// Level returns the current interaction level.
func (in *interaction) Level() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.level
}

// autonomousAutoAnswer is the canned reply every ask_user call receives in
// autonomous mode (spec §11): no human is available, so the agent is told to
// proceed on its own judgement — or, when a wrong guess would be hard to
// reverse, to mark the affected backlog task "blocked" instead of guessing.
// One constant serves both the single-question and batch paths so the two
// can't drift apart.
const autonomousAutoAnswer = "You are in autonomous mode and no human is available, so this question " +
	"cannot be answered. If you can responsibly proceed, do so on your best judgement and note the " +
	"assumption in your final report. If you genuinely cannot — the answer is needed and a wrong guess " +
	"is hard to reverse — do not guess: mark the affected backlog task \"blocked\" (update_task) with a " +
	"brief note of this question, then move on to other work or finish."

// Ask implements orchestrator.Asker.
func (in *interaction) Ask(ctx context.Context, question string, options []string) (string, error) {
	in.mu.Lock()
	level := in.level
	in.mu.Unlock()
	if level == "autonomous" {
		in.emitter.Emit(event.QuestionAsked, askData(question, options, true))
		in.mu.Lock()
		in.assumptions = append(in.assumptions, question)
		in.mu.Unlock()
		in.emitter.Emit(event.QuestionAnswered, map[string]any{"answer": autonomousAutoAnswer, "auto": true})
		return autonomousAutoAnswer, nil
	}

	ch := make(chan string, 1)
	in.mu.Lock()
	in.waiting = ch
	in.options = options
	in.mu.Unlock()
	// Stop pointing at this channel when we leave, whatever the outcome.
	defer func() {
		in.mu.Lock()
		if in.waiting == ch {
			in.waiting = nil
			in.options = nil
		}
		in.mu.Unlock()
	}()

	in.emitter.Emit(event.QuestionAsked, askData(question, options, false))
	select {
	case ans := <-ch:
		in.emitter.Emit(event.QuestionAnswered, map[string]any{"answer": ans})
		return ans, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// AskMany implements orchestrator.Asker for a batch of questions, each with its
// own optional option set. The returned slice is parallel to questions.
func (in *interaction) AskMany(ctx context.Context, questions []orchestrator.Question) ([]string, error) {
	in.mu.Lock()
	level := in.level
	in.mu.Unlock()

	if level == "autonomous" {
		in.emitter.Emit(event.QuestionAsked, askManyData(questions, true))
		answers := make([]string, len(questions))
		in.mu.Lock()
		for i, q := range questions {
			in.assumptions = append(in.assumptions, q.Prompt)
			answers[i] = autonomousAutoAnswer
		}
		in.mu.Unlock()
		in.emitter.Emit(event.QuestionAnswered, map[string]any{"answers": answers, "auto": true})
		return answers, nil
	}

	ch := make(chan []string, 1)
	in.mu.Lock()
	in.batchWaiting = ch
	in.batchQuestions = questions
	in.mu.Unlock()
	defer func() {
		in.mu.Lock()
		if in.batchWaiting == ch {
			in.batchWaiting = nil
			in.batchQuestions = nil
		}
		in.mu.Unlock()
	}()

	in.emitter.Emit(event.QuestionAsked, askManyData(questions, false))
	select {
	case answers := <-ch:
		in.emitter.Emit(event.QuestionAnswered, map[string]any{"answers": answers})
		return answers, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Confirm asks a yes/no question for a high-impact, hard-to-reverse action. Unlike
// Ask, it does NOT auto-answer in autonomous mode: starting the work pipeline is
// hard to reverse, so it always seeks a real human answer. When no human is
// available (the session is cancelled before answering), it returns (false, nil)
// so the action is declined rather than silently taken (spec §9, §11).
func (in *interaction) Confirm(ctx context.Context, question string) (bool, error) {
	const (
		yes = "Yes"
		no  = "No"
	)
	opts := []string{yes, no}

	ch := make(chan string, 1)
	in.mu.Lock()
	in.waiting = ch
	in.options = opts
	in.mu.Unlock()
	defer func() {
		in.mu.Lock()
		if in.waiting == ch {
			in.waiting = nil
			in.options = nil
		}
		in.mu.Unlock()
	}()

	in.emitter.Emit(event.QuestionAsked, askData(question, opts, false))
	select {
	case ans := <-ch:
		ok := isAffirmative(ans)
		in.emitter.Emit(event.QuestionAnswered, map[string]any{"answer": ans, "confirmed": ok})
		return ok, nil
	case <-ctx.Done():
		// Session cancelled / no human available: decline rather than proceed.
		return false, nil
	}
}

// isAffirmative reports whether a confirmation answer means "yes".
func isAffirmative(ans string) bool {
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "yes", "y", "ok", "okay", "approve", "approved", "confirm", "confirmed", "go", "proceed", "sure":
		return true
	default:
		return false
	}
}

// askData builds the question_asked payload, including options when offered.
func askData(question string, options []string, auto bool) map[string]any {
	d := map[string]any{"question": question}
	if auto {
		d["auto"] = true
	}
	if len(options) > 0 {
		d["options"] = options
	}
	return d
}

// askManyData builds the question_asked payload for a batch of questions. Each
// entry carries its prompt and (when offered) its own options list.
func askManyData(questions []orchestrator.Question, auto bool) map[string]any {
	qs := make([]map[string]any, 0, len(questions))
	for _, q := range questions {
		qm := map[string]any{"question": q.Prompt}
		if len(q.Options) > 0 {
			qm["options"] = q.Options
		}
		qs = append(qs, qm)
	}
	d := map[string]any{"questions": qs}
	if auto {
		d["auto"] = true
	}
	return d
}

// batchFreeTextMarker is placed in every batch answer slot after the first when
// a plain free-form Answer (SendInput) resolves a pending multi-question ask_user:
// the user's whole reply lands in A1 and the remaining slots point back to it, so
// the model sees the message once, unambiguously, and knows the other questions
// weren't answered individually.
const batchFreeTextMarker = "(the user replied with a single free-form message; see the answer to Q1)"

// Answer delivers a user answer to the pending question, returning true if one
// was pending and accepted. It claims the pending channel under the lock so a
// duplicate or racing answer can't double-deliver.
//
// If a batch (multi-question) ask_user is pending instead of a single question,
// the free-form text is delivered as the answer to the first question and the
// remaining questions get batchFreeTextMarker pointing back to it. This keeps
// scripted / non-TUI clients (e.g. `ycc send`) from having their reply silently
// buffered into inputCh and lost while the loop is blocked inside AskMany.
func (in *interaction) Answer(text string) bool {
	in.mu.Lock()
	ch := in.waiting
	in.waiting = nil
	in.options = nil
	if ch == nil {
		// No single question pending; fall back to a pending batch, if any.
		bch := in.batchWaiting
		qs := in.batchQuestions
		if bch == nil {
			in.mu.Unlock()
			return false
		}
		in.batchWaiting = nil
		in.batchQuestions = nil
		in.mu.Unlock()
		out := make([]string, len(qs))
		for i := range out {
			if i == 0 {
				out[i] = text
			} else {
				out[i] = batchFreeTextMarker
			}
		}
		bch <- out // buffered(1), single sender, single use: never blocks
		return true
	}
	in.mu.Unlock()
	ch <- text // buffered(1), single sender, single use: never blocks
	return true
}

// AnswerOption resolves a chosen option for the pending question. If idx is a
// valid index into the pending options, that option's text is delivered;
// otherwise text is delivered as free text. Returns true if a question was
// pending and answered.
func (in *interaction) AnswerOption(idx int, text string) bool {
	in.mu.Lock()
	ch := in.waiting
	opts := in.options
	if ch == nil {
		in.mu.Unlock()
		return false
	}
	in.waiting = nil
	in.options = nil
	in.mu.Unlock()
	if idx >= 0 && idx < len(opts) {
		text = opts[idx]
	}
	ch <- text
	return true
}

// AnswerAll delivers a batch of user answers to the pending batch question,
// resolving each answer's text by option index against the corresponding
// question's Options (idx in range → that option text; else free text). The
// answers are padded/truncated to the number of pending questions. Returns true
// if a batch was pending and accepted.
func (in *interaction) AnswerAll(ans []answer) bool {
	in.mu.Lock()
	ch := in.batchWaiting
	qs := in.batchQuestions
	if ch == nil {
		in.mu.Unlock()
		return false
	}
	in.batchWaiting = nil
	in.batchQuestions = nil
	in.mu.Unlock()

	out := make([]string, len(qs))
	for i := range qs {
		text := ""
		if i < len(ans) {
			a := ans[i]
			if a.idx >= 0 && a.idx < len(qs[i].Options) {
				text = qs[i].Options[a.idx]
			} else {
				text = a.text
			}
		}
		out[i] = text
	}
	ch <- out
	return true
}

// pending reports whether a question (single or batch) is currently awaiting a
// user answer. Used by the idle reaper so a session blocked on ask_user is never
// reaped as "idle".
func (in *interaction) pending() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.waiting != nil || in.batchWaiting != nil
}

// Assumptions returns the questions auto-answered in autonomous mode.
func (in *interaction) Assumptions() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.assumptions...)
}
