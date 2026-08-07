// This file owns question pickers and multi-question wizard state and rendering.
package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// answerQuestions submits a batch of answers for a multi-question ask_user call.
func (m model) answerQuestions(answers []*v1.QuestionAnswer) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.AnswerQuestions(m.ctx, connect.NewRequest(&v1.AnswerQuestionsRequest{
			SessionId: m.sessionID, Answers: answers,
		}))
		if err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// startWizard enters the questionnaire wizard for a multi-question ask_user call,
// resetting collected answers and presenting the first question.
func (m *model) startWizard(qs []wizQuestion, seq int64) {
	m.wizActive = true
	m.wizQuestions = qs
	m.wizAnswers = make([]wizAnswer, len(qs))
	for i := range m.wizAnswers {
		m.wizAnswers[i] = wizAnswer{idx: -1}
	}
	m.wizIdx = 0
	m.wizSeq = seq
	m.status = "waiting for your answer"
	// Invalidate the body cache so the active question_asked entry re-renders in
	// its condensed form (the wizard below is now the focal point, not the inline
	// log dump of every question).
	m.invalidateRender()
	m.loadWizQuestion()
}

// loadWizQuestion configures the per-question input (picker or free-text) for the
// current wizard question. For a free-text question it focuses the textarea and
// returns its blink command; the caller in Update propagates it. For a picker
// question it blurs the textarea and returns nil.
func (m *model) loadWizQuestion() tea.Cmd {
	if m.wizIdx < 0 || m.wizIdx >= len(m.wizQuestions) {
		return nil
	}
	q := m.wizQuestions[m.wizIdx]
	m.pending = q.prompt
	m.pickerOpts = append([]string(nil), q.options...)
	m.input.SetValue("")
	if len(m.pickerOpts) > 0 {
		m.picking = true
		m.pickerCursor = 0
		m.input.Blur()
		m.relayout() // picker rows replace the input box; resize the viewport
		return nil
	}
	// Free-text question: re-focus the textarea so the user can type, even when a
	// preceding picker question blurred it. Focus() sets the focused state
	// synchronously (what matters for typing) and returns the cosmetic blink cmd.
	m.picking = false
	m.relayout() // input box replaces the picker rows; resize the viewport
	return m.input.Focus()
}

// clearWizard exits the questionnaire wizard and resets its state.
func (m *model) clearWizard() {
	m.wizActive = false
	m.wizQuestions = nil
	m.wizAnswers = nil
	m.wizIdx = 0
	m.wizSeq = 0
	// Invalidate the body cache so the (now answered) entry re-renders its full
	// enumerated form once the wizard is dismissed.
	m.invalidateRender()
	m.relayout() // the wizard overview rows are gone; give them back to the viewport
}

// recordWizAnswer stores the answer for the current question and advances. When
// the last question is answered it returns the command that submits all answers;
// otherwise it loads the next question and returns nil.
func (m *model) recordWizAnswer(idx int, text string, viaPicker bool) tea.Cmd {
	if m.wizIdx >= 0 && m.wizIdx < len(m.wizAnswers) {
		m.wizAnswers[m.wizIdx] = wizAnswer{idx: idx, text: text, done: true, picking: viaPicker}
	}
	if m.wizIdx < len(m.wizQuestions)-1 {
		m.wizIdx++
		return m.loadWizQuestion()
	}
	// Last question answered: submit the whole batch.
	answers := make([]*v1.QuestionAnswer, len(m.wizAnswers))
	for i, a := range m.wizAnswers {
		answers[i] = &v1.QuestionAnswer{Text: a.text, OptionIndex: int32(a.idx)}
	}
	m.pending = ""
	m.picking = false
	m.pickerOpts = nil
	m.follow = true
	m.relayout() // picker rows collapse back to the input box while awaiting question_answered
	// Re-focus the textarea: when the final question was a picker it blurred the
	// input, and nothing else focuses it again — leaving the session's input box
	// dead once the agent finishes. Focus() flips the typable state synchronously;
	// the returned cmd is only the cosmetic cursor blink.
	fc := m.input.Focus()
	return tea.Batch(fc, m.answerQuestions(answers))
}

// answerQuestion sends a structured answer to a pending question: optIdx >= 0
// selects a suggested option (resolved to its text on the daemon), otherwise
// optIdx is -1 and text is taken as free text.
func (m model) answerQuestion(optIdx int, text string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.AnswerQuestion(m.ctx, connect.NewRequest(&v1.AnswerQuestionRequest{
			SessionId: m.sessionID, Text: text, OptionIndex: int32(optIdx),
		}))
		if err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// choosePickerOption commits the picker selection at idx (a valid index into
// m.pickerOpts). In a wizard it records the answer and advances; otherwise it
// clears the pending question and sends the answer. Shared by the enter and
// number-key paths.
func (m *model) choosePickerOption(idx int) tea.Cmd {
	m.picking = false
	if m.wizActive {
		return m.recordWizAnswer(idx, m.pickerOpts[idx], true)
	}
	m.pending = ""
	m.pickerOpts = nil
	m.follow = true
	m.relayout()
	// The picker blurred the textarea when the question arrived; give focus back
	// now that the picker collapses into the input box, or typing stays dead
	// after the agent moves on (the blurred textarea drops every key).
	fc := m.input.Focus()
	return tea.Batch(fc, m.answerQuestion(idx, ""))
}

// questionPrompt renders the shared interactive-question badge used by the main
// agents (the askStyle " ? " badge followed by the prompt), word-wrapped to
// width w. Continuation lines are hanging-indented to align under the first
// line's text (the badge occupies 5 visible columns). Used by the capture
// overlay modal and the session picker footer.
func questionPrompt(prompt string, w int) string {
	const badgeW = 5 // " " + " ? " + " "
	badge := " " + askStyle.Render(" ? ") + " "
	if w < badgeW+1 {
		w = badgeW + 15
	}
	lines := strings.Split(wrapTo(prompt, w-badgeW), "\n")
	indent := strings.Repeat(" ", badgeW)
	for i := range lines {
		if i == 0 {
			lines[i] = badge + lines[i]
		} else {
			lines[i] = indent + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// pickerView renders the navigable list of suggested answers plus an "other…"
// escape into the free-text textarea.
func (m model) pickerView() string {
	var b strings.Builder
	if m.pending != "" {
		b.WriteString(questionPrompt(m.pending, m.w) + "\n")
	}
	rows := append(append([]string(nil), m.pickerOpts...), "other… (type your own)")
	for i, opt := range rows {
		cursor := "  "
		label := opt
		// Clamp option text so a long suggestion can't wrap to a second physical
		// row (reserve the "  " + cursor "▸ " = 4 leading columns; trunc may add a
		// 1-col ellipsis, so reserve that too).
		if m.w > 0 {
			label = trunc(label, m.w-4-1)
		}
		if i == m.pickerCursor {
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render(label)
		}
		b.WriteString("  " + cursor + label + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// wizardView renders an overview of all questions in a multi-question ask_user
// call alongside each collected answer, marking the question currently being
// answered. The active question's picker/textarea is rendered below it.
func (m model) wizardView() string {
	var b strings.Builder
	b.WriteString(" " + askStyle.Render(" ? ") + " " +
		dimStyle.Render(fmt.Sprintf("question %d of %d", m.wizIdx+1, len(m.wizQuestions))) + "\n")
	for i, q := range m.wizQuestions {
		marker := "  "
		if i == m.wizIdx {
			marker = selStyle.Render("▸ ")
		}
		num := fmt.Sprintf("%d. ", i+1)
		// Word-wrap the question so a prompt longer than the terminal width folds
		// onto multiple lines; continuation lines hang-indent to align under the
		// prompt text (after the "  ▸ N. " prefix).
		prompt := q.prompt
		promptLines := []string{prompt}
		if m.w > 0 {
			promptLines = strings.Split(wrapTo(prompt, m.w-len(num)-4), "\n")
		}
		indent := strings.Repeat(" ", len(num)+4)
		for j, pl := range promptLines {
			if j == 0 {
				line := num + pl
				if i == m.wizIdx {
					line = selStyle.Render(line)
				}
				b.WriteString("  " + marker + line + "\n")
			} else {
				if i == m.wizIdx {
					pl = selStyle.Render(pl)
				}
				b.WriteString(indent + pl + "\n")
			}
		}
		// Show the collected answer (or a pending marker) under each question.
		var ansTxt string
		if a := m.wizAnswers[i]; a.done {
			if a.idx >= 0 && a.idx < len(q.options) {
				ansTxt = "→ " + q.options[a.idx]
			} else {
				ansTxt = "→ " + a.text
			}
		} else if i == m.wizIdx {
			ansTxt = "→ (answer below)"
		} else {
			ansTxt = "→ …"
		}
		if m.w > 0 {
			ansTxt = trunc(ansTxt, m.w-6)
		}
		b.WriteString("     " + dimStyle.Render(ansTxt) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// questionBody renders a question_asked event as the canonical block for the
// whole ask_user exchange (the tool plumbing rows and the question_answered
// row are hidden — see isAskUserPlumbing / isFoldedAnswer). While the footer
// picker/wizard is collecting the answer it collapses to a pointer (the footer
// already shows the prompt); once the paired question_answered event exists the
// answer folds in beneath the question; and an unattended auto-answer compacts
// to one dim line instead of the canned "no human available" paragraph.
func (m *model) questionBody(ev *v1.Event) string {
	ansEv := m.answerEventFor(ev)
	if qs := dataQuestions(ev); len(qs) > 0 {
		return m.batchQuestionBody(ev, qs, ansEv)
	}
	txt := firstField(ev, "question")
	if txt == "" {
		return ""
	}
	// While the single-question picker below echoes this prompt, point at it
	// instead of repeating it (mirrors the wizard's condensed form).
	if ansEv == nil && m.picking && ev.Seq == m.pendingSeq {
		return indentLines(dimStyle.Render("answer below ↓"), "  ")
	}
	body := strings.TrimRight(m.markdown(txt), "\n")
	if ansEv != nil {
		// The extra two-space indent aligns the answer under the question text,
		// which carries glamour's own left margin.
		if dataField(ansEv, "auto") == "true" {
			body += "\n" + autoAnswerLine("  ")
		} else {
			body += "\n" + answerLines(dataField(ansEv, "answer"), m.bodyWrapWidth(), "  ")
		}
	}
	return indentLines(body, "  ")
}

// batchQuestionBody renders a multi-question ask_user batch: each prompt with
// its suggested options while unanswered, or with its folded answer once the
// paired question_answered event exists. While the wizard is actively
// collecting this batch it collapses to a pointer at the wizard below.
func (m *model) batchQuestionBody(ev *v1.Event, qs []wizQuestion, ansEv *v1.Event) string {
	if m.wizActive && ev.Seq == m.wizSeq {
		noun := "questions"
		if len(qs) == 1 {
			noun = "question"
		}
		return indentLines(dimStyle.Render(fmt.Sprintf("%d %s — answer below ↓", len(qs), noun)), "  ")
	}
	auto := ansEv != nil && dataField(ansEv, "auto") == "true"
	var answers []string
	if ansEv != nil && !auto {
		answers = dataList(ansEv, "answers")
	}
	w := m.bodyWrapWidth()
	var b strings.Builder
	for i, q := range qs {
		b.WriteString(wrapTo(fmt.Sprintf("%d. %s", i+1, q.prompt), w) + "\n")
		switch {
		case ansEv == nil:
			// Unanswered: keep the suggested options visible. Once answered only
			// the chosen answer matters, so the options drop away.
			for _, o := range q.options {
				b.WriteString(indentLines(dimStyle.Render(wrapTo("- "+o, w-3)), "   ") + "\n")
			}
		case !auto:
			a := ""
			if i < len(answers) {
				a = answers[i]
			}
			b.WriteString(answerLines(a, w-3, "   ") + "\n")
		}
	}
	if auto {
		b.WriteString(autoAnswerLine(""))
	}
	// Four-space indent matches the left margin glamour gives markdown-rendered
	// bodies (2 from indentLines + 2 of its own), so batch and single-question
	// blocks line up.
	return indentLines(strings.TrimRight(b.String(), "\n"), "    ")
}

// wizQuestion is one parsed question in a multi-question ask_user call.
type wizQuestion struct {
	prompt  string
	options []string
}

// wizAnswer is the user's collected answer to one wizard question. idx >= 0
// selects an option (resolved to its text on the daemon); idx == -1 means the
// free-text field holds the answer.
type wizAnswer struct {
	idx     int
	text    string
	done    bool
	picking bool // chosen via the picker (vs. typed) — for the overview display
}

// dataQuestions parses the `questions` field of a question_asked event into a
// slice of wizQuestion. Returns nil when absent or empty (single-question form).
func dataQuestions(ev *v1.Event) []wizQuestion {
	if ev.DataJson == "" {
		return nil
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return nil
	}
	raw, ok := mp["questions"].([]any)
	if !ok {
		return nil
	}
	var out []wizQuestion
	for _, item := range raw {
		qm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		prompt, _ := qm["question"].(string)
		if strings.TrimSpace(prompt) == "" {
			continue
		}
		q := wizQuestion{prompt: prompt}
		if opts, ok := qm["options"].([]any); ok {
			for _, o := range opts {
				if s, ok := o.(string); ok && strings.TrimSpace(s) != "" {
					q.options = append(q.options, s)
				}
			}
		}
		out = append(out, q)
	}
	return out
}
