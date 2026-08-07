// This file owns capture-session streaming, input, and rendering.
package tui

import (
	"encoding/json"
	"strings"

	"connectrpc.com/connect"

	tea "charm.land/bubbletea/v2"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/reflow/wrap"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// openCapture enters the quick-add backlog capture overlay (task 0016), resetting
// it to the "describe" stage with a focused, empty input.
func (m *model) openCapture() {
	m.capture = true
	m.captureStage = 0
	m.captureQuestion = ""
	m.captureDesc = ""
	m.captureMsg = ""
	m.captureBusy = false
	m.captureLog = nil
	m.captureInput.SetValue("")
	m.captureInput.Focus()
}

// updateCapture handles the modal quick-add overlay: describe an item, optionally
// answer one clarifying question, then see the created task id. The capture runs
// server-side (a separate off-stream agent), so the main session keeps streaming
// behind the overlay — opening or using it never pauses the running session.
func (m model) updateCapture(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var c tea.Cmd
		m.captureInput, c = m.captureInput.Update(msg)
		return m, c
	}
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc":
		m.capture = false
		return m, nil
	case "enter":
		if m.captureBusy {
			return m, nil
		}
		if m.captureStage == 2 {
			// Dismiss after a successful creation.
			m.capture = false
			return m, nil
		}
		val := strings.TrimSpace(m.captureInput.Value())
		if val == "" {
			return m, nil
		}
		if m.captureStage == 0 {
			m.captureDesc = val
			m.captureBusy = true
			m.captureMsg = ""
			// Echo the user's own message into the transcript so the
			// conversation history stays visible after Enter clears the input.
			m.captureLog = append(m.captureLog, userInputEvent(val))
			ch := make(chan *v1.Event, 64)
			m.captureEvents = ch
			m.captureInput.SetValue("")
			spin := m.spinnerCmd() // mutates m.spinning before returning m
			return m, tea.Batch(m.captureSubmit(ch, m.captureDesc, "", ""), spin)
		}
		// Stage 1: answering the agent's clarifying question.
		m.captureBusy = true
		m.captureMsg = ""
		m.captureLog = append(m.captureLog, userInputEvent(val))
		ch := make(chan *v1.Event, 64)
		m.captureEvents = ch
		ans := val
		m.captureInput.SetValue("")
		spin := m.spinnerCmd() // mutates m.spinning before returning m
		return m, tea.Batch(m.captureSubmit(ch, m.captureDesc, m.captureQuestion, ans), spin)
	default:
		var c tea.Cmd
		m.captureInput, c = m.captureInput.Update(msg)
		return m, c
	}
}

// userInputEvent builds a synthetic action-log event echoing the user's own
// submitted text, so the capture overlay transcript shows the conversation
// history (their message alongside the agent's events).
func userInputEvent(text string) *v1.Event {
	var dj string
	if b, err := json.Marshal(map[string]string{"text": text}); err == nil {
		dj = string(b)
	}
	return &v1.Event{Actor: "you", Type: "user_input", DataJson: dj}
}

// captureSubmit opens the streaming CaptureBacklogItem RPC, scoped to the current
// project, and pumps its action-log events into ch. It does not touch the session
// stream. The first event (or an open error) is delivered as the returned msg;
// subsequent events are pulled via waitCaptureEvent.
func (m model) captureSubmit(ch chan *v1.Event, desc, q, a string) tea.Cmd {
	return func() tea.Msg {
		stream, err := m.client.CaptureBacklogItem(m.ctx, connect.NewRequest(&v1.CaptureBacklogItemRequest{
			Project: m.project, Description: desc, PriorQuestion: q, PriorAnswer: a,
		}))
		if err != nil {
			return captureErrMsg{err}
		}
		go func() {
			for stream.Receive() {
				ch <- stream.Msg()
			}
			close(ch)
		}()
		return waitCaptureEvent(ch)()
	}
}

// waitCaptureEvent blocks for the next capture-agent event on ch, mapping a
// closed channel to captureStreamClosedMsg.
func waitCaptureEvent(ch chan *v1.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return captureStreamClosedMsg{}
		}
		return captureEvMsg{ev}
	}
}

// captureView renders the quick-add backlog capture overlay as a bordered modal card.
func (m model) captureView() string {
	var b strings.Builder
	w := m.w - 4
	if w < 1 {
		w = 20
	}
	switch m.captureStage {
	case 0:
		b.WriteString("Describe a new backlog item:\n\n")
		b.WriteString(framedInput(m.captureInput, 0) + "\n")
	case 1:
		// Reuse the shared interactive question UI badge the main agents use.
		b.WriteString(questionPrompt(m.captureQuestion, w) + "\n\n")
		b.WriteString("Your answer:\n\n")
		b.WriteString(framedInput(m.captureInput, 0) + "\n")
	case 2:
		b.WriteString(selStyle.Render(m.captureMsg) + "\n")
	}
	// Stream the capture agent's action log live (task 0049): show the last few
	// events so the user sees progress instead of a blank wait.
	if len(m.captureLog) > 0 {
		b.WriteString("\n")
		const maxLines = 10
		start := 0
		if len(m.captureLog) > maxLines {
			start = len(m.captureLog) - maxLines
		}
		for _, ev := range m.captureLog[start:] {
			// Echo the user's own messages in full (wrapped), without the
			// truncation detailLine applies, so the conversation reads cleanly.
			if ev.Actor == "you" || ev.Type == "user_input" {
				text := dataField(ev, "text")
				if strings.TrimSpace(text) == "" {
					continue
				}
				b.WriteString(wrap.String(wordwrap.String("› "+text, w), w) + "\n")
				continue
			}
			line := detailLine(ev)
			if line == "" {
				continue
			}
			composed := dimStyle.Render(ev.Actor) + " " + line
			b.WriteString(wrap.String(wordwrap.String(composed, w), w) + "\n")
		}
	}
	if m.captureBusy {
		// Animate the same activity spinner (task 0062) while the capture RPC streams.
		spin := dimStyle.Render("…")
		if len(m.spin.Spinner.Frames) > 0 {
			spin = m.spin.View()
		}
		b.WriteString("\n" + spin + " " + dimStyle.Render("capturing…"))
	} else if strings.HasPrefix(m.captureMsg, "error:") {
		b.WriteString("\n" + selStyle.Render(m.captureMsg))
	}
	b.WriteString("\n\n" + dimStyle.Render("(the running session keeps going — capture is off-stream)"))
	hint := "enter submit · esc cancel"
	if m.captureStage == 2 {
		hint = "enter/esc close"
	}
	return m.modalCard(" capture backlog item ", strings.TrimRight(b.String(), "\n"), hint)
}
