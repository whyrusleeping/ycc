package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// ctrl+n opens the quick-capture overlay while a question is pending; the picker
// state survives underneath.
func TestPickerCtrlNOpensCapture(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	updated, _ := m.Update(keyMsg("ctrl+n"))
	m = updated.(model)
	if !m.capture {
		t.Fatal("ctrl+n while picking should open the capture overlay")
	}
	if !m.picking {
		t.Fatal("opening capture must not drop the pending picker")
	}
}

// The quick-add capture overlay (task 0049) streams the capture agent's action
// log live: each captureEvMsg appends to captureLog and is rendered in
// captureView, and a terminal capture_result event drives the overlay to its
// created/answered/error state.
func TestCaptureStreamsActionLog(t *testing.T) {
	m := model{w: 80, capture: true, captureStage: 0, captureBusy: true}
	m.captureInput = newChatInput("describe a new backlog item…")
	m.captureEvents = make(chan *v1.Event, 8)

	// Feed two action-log events; each should append and rearm the waiter.
	turn := &v1.Event{Seq: 1, Actor: "capture", Type: "model_turn", DataJson: `{"text":"drafting the task"}`}
	tc := &v1.Event{Seq: 2, Actor: "capture", Type: "tool_call", DataJson: `{"name":"create_task","args":"{\"title\":\"x\"}"}`}

	nm, _ := m.Update(captureEvMsg{turn})
	m = nm.(model)
	nm, _ = m.Update(captureEvMsg{tc})
	m = nm.(model)

	if len(m.captureLog) != 2 {
		t.Fatalf("captureLog len = %d, want 2", len(m.captureLog))
	}
	if !m.captureBusy {
		t.Fatal("expected captureBusy to remain true while streaming")
	}
	view := m.captureView()
	if !strings.Contains(view, "drafting the task") {
		t.Fatalf("captureView missing model_turn detail:\n%s", view)
	}
	if !strings.Contains(view, "create_task") {
		t.Fatalf("captureView missing tool_call detail:\n%s", view)
	}

	// Terminal capture_result with a created task: stage 2, not busy, msg set.
	done := &v1.Event{Actor: "capture", Type: "capture_result", DataJson: `{"task_id":"0050","title":"Add x","question":""}`}
	nm, _ = m.Update(captureEvMsg{done})
	m = nm.(model)
	if m.captureBusy {
		t.Fatal("expected captureBusy=false after capture_result")
	}
	if m.captureStage != 2 {
		t.Fatalf("captureStage = %d, want 2", m.captureStage)
	}
	if !strings.Contains(m.captureMsg, "0050") {
		t.Fatalf("captureMsg = %q, want it to mention the created id", m.captureMsg)
	}
}

// TestCaptureEchoesUserMessage verifies that submitting a message in the
// quick-add capture overlay appends the user's own text to the transcript log
// (so the conversation history stays visible) and that captureView shows it.
func TestCaptureEchoesUserMessage(t *testing.T) {
	m := model{w: 80, capture: true, captureStage: 0, state: stateMenu}
	m.captureInput = newChatInput("describe a new backlog item…")
	m.captureInput.Focus()
	m.captureInput.SetValue("add a dark mode toggle")

	nm, _ := m.Update(keyMsg("enter"))
	m = nm.(model)

	if len(m.captureLog) == 0 {
		t.Fatalf("captureLog empty, want a user_input event")
	}
	last := m.captureLog[len(m.captureLog)-1]
	if last.Actor != "you" || last.Type != "user_input" {
		t.Fatalf("last event actor/type = %q/%q, want you/user_input", last.Actor, last.Type)
	}
	if dataField(last, "text") != "add a dark mode toggle" {
		t.Fatalf("echoed text = %q, want %q", dataField(last, "text"), "add a dark mode toggle")
	}
	view := m.captureView()
	if !strings.Contains(view, "add a dark mode toggle") {
		t.Fatalf("captureView missing echoed user message:\n%s", view)
	}
}

// TestCaptureLogWraps verifies that long capture-log lines wrap to the modal
// inner width instead of being truncated with an ellipsis or overflowing.
func TestCaptureLogWraps(t *testing.T) {
	m := model{w: 40, h: 24, capture: true, captureStage: 0}
	m.captureInput = newChatInput("describe a new backlog item…")
	m.captureEvents = make(chan *v1.Event, 8)

	long := strings.Repeat("wrapme ", 30)
	ev := &v1.Event{Seq: 1, Actor: "you", Type: "user_input", DataJson: `{"text":"` + strings.TrimSpace(long) + `"}`}
	nm, _ := m.Update(captureEvMsg{ev})
	m = nm.(model)

	view := m.captureView()
	for _, ln := range strings.Split(view, "\n") {
		if lipgloss.Width(ln) > m.w {
			t.Fatalf("rendered line width %d exceeds terminal width %d: %q", lipgloss.Width(ln), m.w, ln)
		}
	}
	// The full text should be present (wrapped across lines), not truncated:
	// every "wrapme" token survives.
	joined := strings.ReplaceAll(stripANSI(view), "\n", " ")
	if got := strings.Count(joined, "wrapme"); got != 30 {
		t.Fatalf("found %d wrapme tokens in wrapped log, want 30 (truncated?):\n%s", got, view)
	}
}

// TestCaptureQuestionUsesSharedBadge verifies the stage-1 clarifying question
// reuses the shared interactive-question UI badge (askStyle " ? ") that the
// main agents use, rather than a bespoke header.
func TestCaptureQuestionUsesSharedBadge(t *testing.T) {
	m := model{w: 80, capture: true, captureStage: 1, captureQuestion: "Which platform?"}
	m.captureInput = newChatInput("describe a new backlog item…")

	view := m.captureView()
	if !strings.Contains(view, askStyle.Render(" ? ")) {
		t.Fatalf("captureView missing shared question badge:\n%s", view)
	}
	if strings.Contains(view, "The capture agent asks:") {
		t.Fatalf("captureView still uses bespoke clarification header:\n%s", view)
	}
	if !strings.Contains(view, "Which platform?") {
		t.Fatalf("captureView missing the question text:\n%s", view)
	}
}
