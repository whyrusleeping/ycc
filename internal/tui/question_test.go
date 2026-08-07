package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// TestWizardFreeTextAfterPickerFocusesInput guards the mixed-batch focus
// regression: when a picker question precedes a free-text question in one
// multi-question ask_user batch, advancing past the picker (which blurs the
// textarea) must re-focus the textarea so the next free-text answer is typable.
func TestWizardFreeTextAfterPickerFocusesInput(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	// Drive a multi-question batch: question 1 is a picker, question 2 is free text.
	m.appendEvent(&v1.Event{
		Seq: 1, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"db?","options":["postgres","sqlite"]},{"question":"name?"}]}`,
	})
	if !m.wizActive {
		t.Fatal("wizard should be active after a batch question_asked")
	}
	if !m.picking || m.wizIdx != 0 {
		t.Fatalf("first question should be a picker at idx 0 (picking=%v idx=%d)", m.picking, m.wizIdx)
	}

	// Answer the first (picker) question by selecting the highlighted option.
	// Update is called directly (not via drive) because advancing to the free-text
	// question returns the textarea's cursor-blink cmd, which would block if run
	// synchronously (see typeText).
	updated, _ = m.Update(keyMsg("enter"))
	m = updated.(model)

	if m.wizIdx != 1 {
		t.Fatalf("after answering Q1, wizIdx=%d, want 1", m.wizIdx)
	}
	if m.picking {
		t.Fatal("Q2 is free text; picking should be false")
	}
	if !m.input.Focused() {
		t.Fatal("textarea must be focused for the free-text question following a picker")
	}
}

// While the wizard is collecting answers for a multi-question batch, the inline
// event-log body for that question_asked event must NOT dump every question
// (which competed with / obscured the one-at-a-time wizard). It should show a
// concise summary pointing down at the wizard. Once the wizard is dismissed, the
// same event reverts to its full enumerated form.
func TestWizardCondensesInlineQuestionDump(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	ev := &v1.Event{
		Seq: 7, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"which database?","options":["postgres","sqlite"]},{"question":"service name?"}]}`,
	}
	m.appendEvent(ev)
	if !m.wizActive {
		t.Fatal("wizard should be active after a batch question_asked")
	}

	// Active: condensed summary, no inline enumeration of the second question.
	body := m.bodyFor(ev)
	if !strings.Contains(body, "questions — answer below") {
		t.Fatalf("active wizard body should show condensed summary, got:\n%s", body)
	}
	if strings.Contains(body, "service name?") {
		t.Fatalf("active wizard body should not enumerate questions inline, got:\n%s", body)
	}

	// Dismiss the wizard; the same event should re-render its full enumerated form.
	m.clearWizard()
	m.bodyCache = map[int]string{}
	body = stripANSI(m.bodyFor(ev))
	if !strings.Contains(body, "which database?") || !strings.Contains(body, "service name?") {
		t.Fatalf("after clearWizard the body should enumerate all questions, got:\n%s", body)
	}
}

// The wizard surfaces the active question's options via the picker and an
// obvious free-text escape, and the footer help spells out the interaction.
func TestWizardPickerAndFooterAffordances(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	m.appendEvent(&v1.Event{
		Seq: 3, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"db?","options":["postgres","sqlite"]},{"question":"name?"}]}`,
	})
	if !m.wizActive || !m.picking {
		t.Fatalf("expected an active picker wizard (active=%v picking=%v)", m.wizActive, m.picking)
	}

	picker := m.pickerView()
	if !strings.Contains(picker, "postgres") || !strings.Contains(picker, "sqlite") {
		t.Fatalf("picker should list the active question's options, got:\n%s", picker)
	}
	if !strings.Contains(picker, "other… (type your own)") {
		t.Fatalf("picker should offer an obvious free-text escape, got:\n%s", picker)
	}

	view := m.sessionView()
	if !strings.Contains(view, "choose") || !strings.Contains(view, "other…") {
		t.Fatalf("wizard footer should explain choosing + free-text, got:\n%s", view)
	}
}

// A number key selects the corresponding option directly (spec §18.3): the
// pending question clears and an answer command is issued without touching the
// highlighted cursor first.
func TestPickerNumberKeySelectsOption(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)

	// Press "2" → selects the 2nd option (index 1). The answer cmd is not run
	// here (the fake has no AnswerQuestion), only the state transition is checked.
	updated, cmd := m.Update(keyMsg("2"))
	m = updated.(model)
	if m.picking {
		t.Fatal("number key should have dismissed the picker")
	}
	if m.pending != "" || m.pickerOpts != nil {
		t.Fatalf("selecting an option should clear pending/pickerOpts, got pending=%q opts=%v", m.pending, m.pickerOpts)
	}
	if cmd == nil {
		t.Fatal("selecting an option should return an answer command")
	}
}

// TestPickerAnswerRefocusesInput guards the dead-input regression: answering a
// single options question via the picker blurs the textarea when the question
// arrives; committing the answer must hand focus back, or the input box drops
// every keystroke once the agent finishes (e.g. after onboarding).
func TestPickerAnswerRefocusesInput(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	if m.input.Focused() {
		t.Fatal("precondition: the picker should have blurred the textarea")
	}

	updated, _ := m.Update(keyMsg("enter"))
	m = updated.(model)
	if m.picking {
		t.Fatal("enter should have dismissed the picker")
	}
	if !m.input.Focused() {
		t.Fatal("textarea must be focused again after answering via the picker")
	}
}

// TestWizardFinalPickerAnswerRefocusesInput is the wizard variant: when the LAST
// question of a multi-question batch is a picker, submitting the batch must
// re-focus the textarea (the mixed picker→free-text case is covered by
// TestWizardFreeTextAfterPickerFocusesInput; this guards the picker-last case).
func TestWizardFinalPickerAnswerRefocusesInput(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	// Free-text question first, picker question last.
	m.appendEvent(&v1.Event{
		Seq: 1, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"name?"},{"question":"db?","options":["postgres","sqlite"]}]}`,
	})
	if !m.wizActive || m.picking {
		t.Fatalf("Q1 should be free text (active=%v picking=%v)", m.wizActive, m.picking)
	}

	// Answer Q1 as free text, landing on the final picker question.
	m.input.SetValue("svc")
	updated, _ = m.Update(keyMsg("enter"))
	m = updated.(model)
	if !m.picking || m.wizIdx != 1 {
		t.Fatalf("Q2 should be the active picker (picking=%v idx=%d)", m.picking, m.wizIdx)
	}
	if m.input.Focused() {
		t.Fatal("precondition: the picker question should have blurred the textarea")
	}

	// Answer the final picker question: the batch submits and the input box
	// replaces the picker — it must be typable. (wizActive stays set until the
	// daemon confirms with question_answered.)
	updated, _ = m.Update(keyMsg("enter"))
	m = updated.(model)
	if m.picking {
		t.Fatal("picker should collapse once the batch submits")
	}
	if !m.input.Focused() {
		t.Fatal("textarea must be focused after the final picker answer submits the batch")
	}
}

// TestQuestionAnsweredEventRefocusesInput: the daemon's question_answered
// confirmation is the safety net — even if local state missed the re-focus,
// the event must leave the textarea typable.
func TestQuestionAnsweredEventRefocusesInput(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	if m.input.Focused() {
		t.Fatal("precondition: the picker should have blurred the textarea")
	}

	m.appendEvent(&v1.Event{Seq: 2, Type: "question_answered", Actor: "user"})
	if m.picking || m.pending != "" {
		t.Fatalf("question_answered should clear the picker (picking=%v pending=%q)", m.picking, m.pending)
	}
	if !m.input.Focused() {
		t.Fatal("question_answered must re-focus the textarea")
	}
}

// The wizard (multi-question batch) variant of the stale-question reopen.
func TestReopenClearsStaleWizard(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	m.appendEvent(&v1.Event{
		Seq: 1, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"db?","options":["postgres","sqlite"]},{"question":"name?"}]}`,
	})
	if !m.wizActive || !m.picking {
		t.Fatalf("precondition: wizard picker should be active (active=%v picking=%v)", m.wizActive, m.picking)
	}

	m.appendEvent(&v1.Event{Seq: 2, Type: "session_reopened", Actor: "system"})
	if m.wizActive || m.picking || m.pending != "" {
		t.Fatalf("session_reopened should drop the stale wizard (active=%v picking=%v pending=%q)",
			m.wizActive, m.picking, m.pending)
	}
	if !m.input.Focused() {
		t.Fatal("session_reopened must re-focus the textarea")
	}
}

// In a multi-question wizard a number key selects the active question's option
// and advances to the next question.
func TestWizardNumberKeyAdvances(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.appendEvent(&v1.Event{
		Seq: 1, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"db?","options":["postgres","sqlite"]},{"question":"name?","options":["a","b"]}]}`,
	})
	if !m.wizActive || !m.picking || m.wizIdx != 0 {
		t.Fatalf("expected wizard picker at idx 0 (active=%v picking=%v idx=%d)", m.wizActive, m.picking, m.wizIdx)
	}

	updated, _ = m.Update(keyMsg("1"))
	m = updated.(model)
	if m.wizIdx != 1 {
		t.Fatalf("number key should advance the wizard: wizIdx=%d, want 1", m.wizIdx)
	}
	if got := m.wizAnswers[0]; !got.done || got.idx != 0 {
		t.Fatalf("Q1 answer should record option 0: %+v", got)
	}
}

// A digit past the number of options is ignored: the picker stays open and the
// cursor doesn't move.
func TestPickerNumberBeyondOptionsNoop(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f) // 3 options
	updated, cmd := m.Update(keyMsg("7"))
	m = updated.(model)
	if !m.picking {
		t.Fatal("a digit beyond the option count must not dismiss the picker")
	}
	if m.pickerCursor != 0 {
		t.Fatalf("stray digit moved the cursor to %d", m.pickerCursor)
	}
	if cmd != nil {
		t.Fatal("stray digit should not return a command")
	}
}

// pgup/pgdown scroll the transcript while a picker is active instead of being
// swallowed, so the question's context can be re-read.
func TestPickerScrollsTranscript(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	// Overflow the viewport so there is something to scroll.
	for i := 2; i < 60; i++ {
		m.appendEvent(&v1.Event{Seq: int64(i), Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"line"}`})
	}
	m.rebuild()
	m.vp.GotoBottom()
	m.follow = m.vp.AtBottom()
	if !m.follow {
		t.Fatal("setup: viewport should start at bottom (following)")
	}

	updated, _ := m.Update(keyMsg("pgup"))
	m = updated.(model)
	if m.follow {
		t.Fatal("pgup while picking should scroll up (follow=false), not be swallowed")
	}
	if !m.picking {
		t.Fatal("scrolling must not dismiss the picker")
	}
}

// A mouse wheel scroll reaches the viewport even while the picker is active.
func TestPickerMouseWheelScrolls(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	for i := 2; i < 60; i++ {
		m.appendEvent(&v1.Event{Seq: int64(i), Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"line"}`})
	}
	m.rebuild()
	m.vp.GotoBottom()
	m.follow = m.vp.AtBottom()

	before := m.vp.YOffset()
	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = updated.(model)
	if m.vp.YOffset() >= before {
		t.Fatalf("mouse wheel should scroll the viewport up: before=%d after=%d", before, m.vp.YOffset())
	}
	if !m.picking {
		t.Fatal("mouse wheel must not dismiss the picker")
	}
}

// The picker footer advertises number selection (spec §18.3).
func TestPickerFooterMentionsNumbers(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	if view := m.sessionView(); !strings.Contains(view, "1–9") {
		t.Fatalf("picker footer should advertise number selection, got:\n%s", view)
	}
}

// A pending question longer than the terminal width must word-wrap in the
// picker footer (task 0149) rather than being clipped/ellipsised: the full text
// is present across wrapped lines and no rendered line overflows the width.
func TestPickerWrapsLongQuestion(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	long := strings.TrimSpace(strings.Repeat("wrapme ", 30))
	m.appendEvent(&v1.Event{
		Seq: 1, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"question":"` + long + `","options":["yes","no"]}`,
	})
	if !m.picking || m.wizActive {
		t.Fatalf("expected a single-question picker (picking=%v wizActive=%v)", m.picking, m.wizActive)
	}

	assertPickerWraps := func(width int) {
		picker := m.pickerView()
		for _, ln := range strings.Split(picker, "\n") {
			if w := lipgloss.Width(ln); w > m.w {
				t.Fatalf("width %d: picker line width %d exceeds %d: %q", width, w, m.w, ln)
			}
		}
		joined := strings.ReplaceAll(stripANSI(picker), "\n", " ")
		if got := strings.Count(joined, "wrapme"); got != 30 {
			t.Fatalf("width %d: found %d wrapme tokens (clipped?), want 30:\n%s", width, got, picker)
		}
		// The layout accounting must agree with the rendered footer height so the
		// help line / viewport math stays correct after wrapping.
		if h, want := m.footerStackHeight(), lipgloss.Height(picker); h != want {
			t.Fatalf("width %d: footerStackHeight()=%d, want lipgloss.Height(pickerView())=%d", width, h, want)
		}
	}

	assertPickerWraps(80)

	// Reflow on resize: a narrower terminal must re-wrap and still fit.
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 48, Height: 24})
	m = updated.(model)
	assertPickerWraps(48)
}

// The multi-question wizard footer word-wraps a long question prompt (task 0149)
// instead of truncating it, and every rendered line fits the terminal width.
func TestWizardWrapsLongQuestion(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	m = updated.(model)

	long := strings.TrimSpace(strings.Repeat("wrapme ", 25))
	m.appendEvent(&v1.Event{
		Seq: 3, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"` + long + `","options":["a","b"]},{"question":"short?"}]}`,
	})
	if !m.wizActive {
		t.Fatalf("expected an active wizard (wizActive=%v)", m.wizActive)
	}

	wiz := m.wizardView()
	for _, ln := range strings.Split(wiz, "\n") {
		if w := lipgloss.Width(ln); w > m.w {
			t.Fatalf("wizard line width %d exceeds %d: %q", w, m.w, ln)
		}
	}
	joined := strings.ReplaceAll(stripANSI(wiz), "\n", " ")
	if got := strings.Count(joined, "wrapme"); got != 25 {
		t.Fatalf("found %d wrapme tokens (truncated?), want 25:\n%s", got, wiz)
	}
}

// TestQuestionPickerFitsOnScreen guards the ask_user layout regression: when a
// question_asked event carries options (single question or a multi-question
// batch), the rendered session view must still fit the terminal height with the
// picker options and help footer visible. Previously relayout only accounted
// for the input box, so the wizard overview / option picker stacked BELOW a
// full-height viewport and were clipped off the bottom of the screen — the user
// never saw the options and the question faded off the bottom.
func TestQuestionPickerFitsOnScreen(t *testing.T) {
	cases := map[string]string{
		"single": `{"question":"db?","options":["postgres","sqlite","mysql"]}`,
		"batch":  `{"questions":[{"question":"db?","options":["postgres","sqlite"]},{"question":"name?"}]}`,
	}
	for name, dataJSON := range cases {
		t.Run(name, func(t *testing.T) {
			m := newSessionTextareaModel(t)
			m.client = newFakeClient()
			m.ctx = context.Background()
			m.sessionID = "s1"
			m.events = make(chan *v1.Event, 4)
			m.follow = true

			// Fill the log with enough turns that the viewport content alone
			// exceeds the 24-row terminal — the situation where an unshrunk
			// viewport pushes the picker off-screen.
			var seq int64
			for seq = 1; seq <= 40; seq++ {
				nm, _ := m.Update(evMsg{&v1.Event{
					Seq: seq, Type: "model_turn", Actor: "coordinator",
					DataJson: fmt.Sprintf(`{"text":"working on step %d"}`, seq),
				}})
				m = nm.(model)
			}
			nm, _ := m.Update(evMsg{&v1.Event{
				Seq: seq, Type: "question_asked", Actor: "coordinator", DataJson: dataJSON,
			}})
			m = nm.(model)
			if !m.picking {
				t.Fatal("expected the option picker to be active after question_asked with options")
			}

			view := m.render()
			lines := strings.Split(view, "\n")
			if len(lines) > 24 {
				t.Fatalf("session view is %d rows tall; must fit the 24-row terminal", len(lines))
			}
			if !strings.Contains(view, "postgres") {
				t.Fatalf("picker options not visible in the rendered view:\n%s", view)
			}
			if !strings.Contains(view, "other…") {
				t.Fatalf("picker 'other…' escape not visible in the rendered view:\n%s", view)
			}
		})
	}
}

// One ask_user round-trip emits four events (tool_call, question_asked,
// question_answered, tool_result); the transcript must render them as ONE
// block: the plumbing rows fold away and the question_asked body carries the
// question plus the folded answer.
func TestAskUserExchangeFoldsToSingleBlock(t *testing.T) {
	m := &model{
		expanded: map[int]bool{}, bodyCache: map[int]string{},
		evs: []*v1.Event{
			{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"t1","name":"ask_user","args":"{\"question\":\"which db?\"}"}`},
			{Seq: 2, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"which db?","options":["postgres","sqlite"]}`},
			{Seq: 3, Type: "question_answered", Actor: "coordinator", DataJson: `{"answer":"postgres"}`},
			{Seq: 4, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"t1","result":"postgres"}`},
		},
	}
	if !m.hiddenRow(0) {
		t.Fatal("ask_user tool_call should be hidden once its question_asked exists")
	}
	if m.hiddenRow(1) {
		t.Fatal("question_asked row must stay visible (it is the canonical Q&A block)")
	}
	if !m.hiddenRow(2) {
		t.Fatal("question_answered should fold into the question_asked row")
	}
	if !m.hiddenRow(3) {
		t.Fatal("ask_user tool_result (a duplicate of the answer) should be hidden")
	}
	body := stripANSI(m.bodyFor(m.evs[1]))
	if !strings.Contains(body, "which db?") {
		t.Fatalf("question body should contain the question, got:\n%s", body)
	}
	if !strings.Contains(body, "→ postgres") {
		t.Fatalf("question body should fold in the answer, got:\n%s", body)
	}
}

// While a question is still unanswered, the question row stays visible with no
// answer line, and the (blocked, still-running) ask_user tool_call is already
// hidden — otherwise it would sit there showing a stuck in-flight glyph.
func TestAskUserPendingQuestionVisibleWithoutAnswer(t *testing.T) {
	m := &model{
		expanded: map[int]bool{}, bodyCache: map[int]string{},
		evs: []*v1.Event{
			{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"t1","name":"ask_user","args":"{\"question\":\"which db?\"}"}`},
			{Seq: 2, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"which db?"}`},
		},
	}
	if !m.hiddenRow(0) {
		t.Fatal("pending ask_user tool_call should be hidden")
	}
	if m.hiddenRow(1) {
		t.Fatal("pending question_asked must be visible")
	}
	body := stripANSI(m.bodyFor(m.evs[1]))
	if !strings.Contains(body, "which db?") {
		t.Fatalf("pending question body should show the question, got:\n%s", body)
	}
	if strings.Contains(body, "→") {
		t.Fatalf("pending question body must not show an answer arrow, got:\n%s", body)
	}
}

// An ask_user call that never asked (validation error: no question_asked event)
// must NOT be treated as plumbing — the call and its errored result render via
// the normal adjacent-fold tool card so the failure stays visible. An ask that
// was cancelled mid-question keeps its errored result visible too.
func TestAskUserErrorStaysVisible(t *testing.T) {
	bad := &model{evs: []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"t1","name":"ask_user","args":"{}"}`},
		{Seq: 2, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"t1","result":"ask_user: provide a 'question'","error":"true"}`},
	}}
	if bad.isAskUserPlumbing(0) || bad.isAskUserPlumbing(1) {
		t.Fatal("an ask_user call with no question_asked is not plumbing")
	}
	// The adjacent pair still folds into one visible tool card (isMergedResult),
	// which is the normal errored-tool rendering.
	if !bad.hiddenRow(1) || bad.hiddenRow(0) {
		t.Fatal("errored ask_user should render as a normal merged tool card")
	}

	cancelled := &model{evs: []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"t1","name":"ask_user","args":"{\"question\":\"q?\"}"}`},
		{Seq: 2, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"q?"}`},
		{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"t1","result":"ask_user: context canceled","error":"true"}`},
	}}
	if !cancelled.hiddenRow(0) {
		t.Fatal("cancelled ask_user call should still be hidden (its question row shows the ask)")
	}
	if cancelled.hiddenRow(2) {
		t.Fatal("an errored ask_user result must stay visible so the failure is not swallowed")
	}
}

// Autonomous-mode auto-answers must render as one compact dim line, not the
// full canned "no human is available…" paragraph the agent receives.
func TestAutoAnsweredQuestionRendersCompact(t *testing.T) {
	canned := "You are in unattended execution and no human is available, so this question cannot be answered."
	m := &model{
		expanded: map[int]bool{}, bodyCache: map[int]string{},
		evs: []*v1.Event{
			{Seq: 1, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"should I?","auto":true}`},
			{Seq: 2, Type: "question_answered", Actor: "coordinator", DataJson: fmt.Sprintf(`{"answer":%q,"auto":true}`, canned)},
		},
	}
	if !m.hiddenRow(1) {
		t.Fatal("auto question_answered should fold into the question row")
	}
	body := stripANSI(m.bodyFor(m.evs[0]))
	if !strings.Contains(body, "auto-answered (unattended execution)") {
		t.Fatalf("auto answer should render the compact marker, got:\n%s", body)
	}
	if strings.Contains(body, "no human is available") {
		t.Fatalf("auto answer must not dump the canned paragraph, got:\n%s", body)
	}
}

// A multi-question batch folds each answer beneath its own question in one
// block; the suggested options drop away once answered (only the chosen answer
// matters), and the tool_result's "Q1/A1" dump stays hidden.
func TestBatchQuestionBodyInterleavesAnswers(t *testing.T) {
	m := &model{
		expanded: map[int]bool{}, bodyCache: map[int]string{},
		evs: []*v1.Event{
			{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"t1","name":"ask_user","args":"{}"}`},
			{Seq: 2, Type: "question_asked", Actor: "coordinator", DataJson: `{"questions":[{"question":"which database?","options":["postgres","sqlite"]},{"question":"service name?"}]}`},
			{Seq: 3, Type: "question_answered", Actor: "coordinator", DataJson: `{"answers":["postgres","ycc"]}`},
			{Seq: 4, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"t1","result":"Q1: which database?\nA1: postgres\n\nQ2: service name?\nA2: ycc"}`},
		},
	}
	if !m.hiddenRow(0) || !m.hiddenRow(2) || !m.hiddenRow(3) {
		t.Fatal("batch ask_user plumbing and answer rows should be hidden")
	}
	body := stripANSI(m.bodyFor(m.evs[1]))
	for _, want := range []string{"1. which database?", "→ postgres", "2. service name?", "→ ycc"} {
		if !strings.Contains(body, want) {
			t.Fatalf("batch body missing %q, got:\n%s", want, body)
		}
	}
	if strings.Contains(body, "sqlite") {
		t.Fatalf("answered batch body should drop the unchosen options, got:\n%s", body)
	}

	// Unanswered: options stay visible, no answer arrows yet.
	pending := &model{
		expanded: map[int]bool{}, bodyCache: map[int]string{},
		evs: m.evs[:2],
	}
	body = stripANSI(pending.bodyFor(pending.evs[1]))
	if !strings.Contains(body, "sqlite") {
		t.Fatalf("unanswered batch body should keep the options, got:\n%s", body)
	}
	if strings.Contains(body, "→") {
		t.Fatalf("unanswered batch body must not show answer arrows, got:\n%s", body)
	}
}

// While the single-question footer picker is echoing the pending prompt, the
// question row's body collapses to a pointer (mirroring the wizard) so the
// question never renders twice on screen at once; choosing "other…" (free
// text) restores the full question since the plain textarea shows no prompt.
func TestPendingPickerCondensesQuestionBody(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	ev := &v1.Event{
		Seq: 5, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"question":"which database?","options":["postgres","sqlite"]}`,
	}
	m.appendEvent(ev)
	if !m.picking || m.pendingSeq != 5 {
		t.Fatalf("picker should be active for seq 5 (picking=%v pendingSeq=%d)", m.picking, m.pendingSeq)
	}
	body := stripANSI(m.bodyFor(ev))
	if !strings.Contains(body, "answer below") {
		t.Fatalf("pending picker body should be a pointer, got:\n%s", body)
	}
	if strings.Contains(body, "which database?") {
		t.Fatalf("pending picker body should not repeat the prompt, got:\n%s", body)
	}

	// "other…" drops to the free-text textarea: the prompt must come back.
	m.pickerCursor = len(m.pickerOpts) // the "other…" row
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.picking {
		t.Fatal("enter on other… should leave picker mode")
	}
	body = stripANSI(m.bodyFor(ev))
	if !strings.Contains(body, "which database?") {
		t.Fatalf("free-text mode should restore the full question body, got:\n%s", body)
	}
}
