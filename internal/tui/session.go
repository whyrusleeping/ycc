// This file owns live session streaming, input, notifications, and session rendering.
package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// fetchTranscript loads a session's full replayed event log for the read-only
// transcript drill-in (spec §18.6) via the GetSessionTranscript RPC.
func (m model) fetchTranscript(id string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.GetSessionTranscript(m.ctx, connect.NewRequest(&v1.GetSessionTranscriptRequest{
			Project: m.project, SessionId: id,
		}))
		if err != nil {
			return transcriptMsg{id: id, err: err}
		}
		return transcriptMsg{id: id, events: resp.Msg.Events}
	}
}

// reopenSession re-opens a persisted session on its existing event log via
// ResumeSession ("resume = replay", spec §4.5/§18.6) and enters the session view.
func (m model) reopenSession(id string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.ResumeSession(m.ctx, connect.NewRequest(&v1.ResumeSessionRequest{
			Project: m.project, SessionId: id,
		}))
		if err != nil {
			return errMsg{err}
		}
		return startedMsg{id: resp.Msg.SessionId, mode: resp.Msg.Mode}
	}
}

func (m model) subscribe() tea.Cmd {
	return func() tea.Msg {
		stream, err := m.client.Subscribe(m.ctx, connect.NewRequest(&v1.SubscribeRequest{SessionId: m.sessionID}))
		if err != nil {
			return errMsg{err}
		}
		go func() {
			for stream.Receive() {
				m.events <- stream.Msg()
			}
			close(m.events)
		}()
		return waitEvent(m.events)()
	}
}

func waitEvent(ch chan *v1.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamClosedMsg{}
		}
		return evMsg{ev}
	}
}

func (m model) sendInput(text string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.SendInput(m.ctx, connect.NewRequest(&v1.SendInputRequest{SessionId: m.sessionID, Text: text})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// interrupt gracefully pauses the running session at its next safe checkpoint
// (spec §18.7) so the user can steer or resume.
func (m model) interrupt() tea.Cmd {
	return func() tea.Msg {
		if m.sessionID == "" {
			return nil
		}
		if _, err := m.client.Interrupt(m.ctx, connect.NewRequest(&v1.InterruptRequest{SessionId: m.sessionID})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// resume continues a paused session unchanged (spec §18.7).
func (m model) resume() tea.Cmd {
	return func() tea.Msg {
		if m.sessionID == "" {
			return nil
		}
		if _, err := m.client.Resume(m.ctx, connect.NewRequest(&v1.ResumeRequest{SessionId: m.sessionID})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

func (m model) updateSession(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelUp || msg.Button == tea.MouseWheelDown {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			m.follow = m.vp.AtBottom()
			return m, cmd
		}
		return m, nil

	case tea.MouseClickMsg:
		// A left press is ambiguous until released: motion turns it into a
		// drag-selection (select.go), an immediate release stays a click whose
		// expand/collapse toggle is applied by the MouseReleaseMsg case below.
		if msg.Button == tea.MouseLeft {
			m.selMouseDown(msg.X, msg.Y)
		}
		return m, nil

	case tea.MouseMotionMsg:
		m.selMouseMotion(msg.X, msg.Y)
		return m, nil

	case tea.MouseReleaseMsg:
		// wasPress (a left press recorded by selMouseDown) rather than
		// msg.Button gates the click fallback: legacy X10 mouse encoding
		// reports every release as Button==MouseNone.
		wasPress := m.selDrag
		if text, dragged := m.selMouseUp(); dragged {
			// A drag-selection: copy it to the system clipboard (OSC 52) and
			// confirm with the same transient flash the row-yank uses.
			if text == "" {
				return m, nil
			}
			return m, tea.Batch(tea.SetClipboard(text), m.noteFlash("copied ✓"))
		} else if wasPress {
			// A plain click (no drag): select + expand/collapse the row under
			// the pointer, as before.
			row := msg.Y - headerHeight + m.vp.YOffset()
			if i := m.eventAt(row); i >= 0 {
				m.selected = i
				m.toggle(i)
				m.follow = false
			}
		}
		return m, nil

	case tea.KeyMsg:
		// While the transcript search bar owns input (task 0116), keystrokes edit
		// the query and incrementally re-jump the selection to the nearest match.
		// It is entered by `/` below (only when the input textarea is empty) and
		// blurs the input; esc/enter (handled at the top level / here) leave it.
		if m.searching {
			switch msg.String() {
			case "ctrl+c":
				return m.confirmQuit()
			case "enter":
				// Confirm: keep the query active for n/N, restore the input row.
				m.searching = false
				m.relayout()
				return m, m.input.Focus()
			case "backspace":
				if r := []rune(m.searchQuery); len(r) > 0 {
					m.searchQuery = string(r[:len(r)-1])
				}
				m.runSearch()
				return m, nil
			default:
				if t := msg.Key().Text; t != "" {
					m.searchQuery += t
					m.runSearch()
				}
				return m, nil
			}
		}
		// When a question with options is pending, the footer is a picker that
		// owns ↑/↓/enter until the user chooses "other…" to free-type.
		if m.picking {
			switch msg.String() {
			case "ctrl+c":
				return m.confirmQuit()
			case "up":
				if m.pickerCursor > 0 {
					m.pickerCursor--
				}
				return m, nil
			case "down":
				if m.pickerCursor < len(m.pickerOpts) {
					m.pickerCursor++
				}
				return m, nil
			case "enter":
				if m.pickerCursor >= len(m.pickerOpts) {
					// "other…": drop into the free-text textarea. The question row's
					// body was collapsed to an "answer below ↓" pointer while the
					// picker echoed the prompt; the plain textarea doesn't, so
					// restore the full question in the transcript.
					m.picking = false
					m.invalidateRender()
					m.relayout()
					m.rebuild()
					return m, m.input.Focus()
				}
				return m, m.choosePickerOption(m.pickerCursor)
			case "1", "2", "3", "4", "5", "6", "7", "8", "9":
				// Number keys select an option directly (spec §18.3); digits past
				// the option count are ignored so a stray press stays on the picker.
				if idx := int(msg.String()[0] - '1'); idx < len(m.pickerOpts) {
					return m, m.choosePickerOption(idx)
				}
				return m, nil
			case "pgup":
				// Keep the transcript scrollable so the question's context can be
				// re-read without dismissing the picker.
				m.vp.HalfPageUp()
				m.follow = m.vp.AtBottom()
				return m, nil
			case "pgdown":
				m.vp.HalfPageDown()
				m.follow = m.vp.AtBottom()
				return m, nil
			case "ctrl+n":
				// Quick-add a backlog item without answering yet (task 0016); the
				// picker re-renders once the capture overlay closes.
				m.openCapture()
				return m, nil
			case "ctrl+b":
				// Open the read-only backlog browser (spec §18.5) — often exactly
				// what's needed to answer "which task next?". m.picking is left set
				// so sessionView restores the picker on return.
				m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
				m.backlogShowDone = false
				m.backlogBlockedOnly = false
				return m, m.fetchBacklog
			case "ctrl+o":
				// Open the browse selector (backlog / sessions / cost) — parity with
				// the menu (task 0112). m.picking stays set so the picker restores.
				m.openBrowse()
				return m, nil
			case "ctrl+r":
				// Open the read-only session browser modal (task 0112).
				m.openHistModal()
				return m, m.fetchHistory
			case "?", "ctrl+h", "ctrl+_":
				// Open the keybinding help modal (task 0111). No free-text input is
				// focused in the picker, so "?"/ctrl+h open unconditionally here.
				m.openHelp()
				return m, nil
			}
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c":
			return m.confirmQuit()
		case "ctrl+n":
			// Quick-add a backlog item without pausing the session (task 0016).
			m.openCapture()
			return m, nil
		case "ctrl+b":
			// Open the read-only backlog browser (spec §18.5).
			m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
			m.backlogShowDone = false
			m.backlogBlockedOnly = false
			return m, m.fetchBacklog
		case "ctrl+o":
			// Open the browse selector (backlog / plans / sessions / cost) from a
			// session — parity with the menu (task 0112).
			m.openBrowse()
			return m, nil
		case "ctrl+r":
			// Open the read-only session browser modal over the session (task 0112).
			m.openHistModal()
			return m, m.fetchHistory
		case "?", "ctrl+h":
			// Open the keybinding help modal (task 0111). Gated on empty input so a
			// bare "?" still types and ctrl+h (== legacy BS byte 0x08, bound by the
			// textarea to delete-char-backward) keeps deleting mid-edit; fall through
			// to the textarea otherwise. ctrl+_ is the unconditional chord.
			if strings.TrimSpace(m.input.Value()) == "" {
				m.openHelp()
				return m, nil
			}
		case "ctrl+_":
			m.openHelp()
			return m, nil
		case "ctrl+i", "ctrl+x":
			// Gracefully interrupt the running agent to steer it (spec §18.7).
			// ctrl+i is the historical chord but is byte-identical to Tab (0x09)
			// and only distinguishable on terminals with the kitty keyboard
			// protocol; ctrl+x (0x18) is a distinct control byte delivered on
			// every terminal (and unused by the textarea keymap), so it is the
			// universal fallback.
			if !m.paused {
				return m, m.interrupt()
			}
			return m, nil
		case "shift+tab":
			if m.mode != "work" {
				return m, nil
			}
			if m.looping {
				m.status = "loop stopping: current task finishes, next not picked"
				return m, m.stopWorkLoop()
			}
			// Do not immediately start a daemon loop beside this attended session.
			// Arm it and start only after the current stream closes, avoiding two
			// coordinators competing for the same backlog.
			m.loopArmed = !m.loopArmed
			m.loopArmStop = false
			if m.loopArmed {
				m.status = "loop armed: starts when this session finishes"
			} else {
				m.status = "loop disarmed"
			}
			return m, nil
		case "up":
			m.moveSelection(-1)
			return m, nil
		case "down":
			m.moveSelection(1)
			return m, nil
		case "q":
			// Return to the main menu from a finished (idle / stream-closed) session
			// (task 0127). Gated on empty input so a bare "q" still types into the
			// textarea mid-compose; falls through otherwise. Only fires on a finished,
			// non-looping session (sessionFinished): the daemon owns loop sessions.
			if m.sessionFinished() && strings.TrimSpace(m.input.Value()) == "" {
				// Build the stop command FIRST so it captures the current m.sessionID.
				stop := m.stopSession()
				status := m.status
				// Clear transient session-footer state so the menu starts clean.
				m.pending, m.pendingSeq = "", 0
				m.pickerOpts, m.picking = nil, false
				m.clearWizard()
				m.clearSearch()
				m.selected = -1
				m.sessionID = ""
				m.status = ""
				m.state = stateMenu
				// StopSession only when the session went idle (it is still alive in the
				// daemon). A "stream closed" session is already gone — stopping it would
				// return NotFound and flash a needless error.
				if status == "idle" {
					return m, tea.Batch(stop, m.refreshMenu())
				}
				return m, m.refreshMenu()
			}
		case "y":
			// Copy the selected transcript row's text to the clipboard via OSC 52
			// (task 0141). Gated on empty input so a bare "y" still types into the
			// textarea mid-compose; falls through otherwise. commit_made → sha,
			// session_error → the error text, otherwise the row's body text.
			if m.selected >= 0 && m.selected < len(m.evs) && strings.TrimSpace(m.input.Value()) == "" {
				text := m.yankText(m.evs[m.selected])
				if text == "" {
					return m, nil
				}
				return m, tea.Batch(tea.SetClipboard(text), m.noteFlash("copied ✓"))
			}
		case "/":
			// Enter transcript search (task 0116). Gated on empty input so a bare
			// "/" still types into the textarea mid-compose; falls through otherwise.
			if strings.TrimSpace(m.input.Value()) == "" {
				m.searching = true
				m.searchQuery = ""
				m.input.Blur()
				m.relayout()
				return m, nil
			}
		case "n":
			// Cycle to the next search match. Only hijacks 'n' when a search is
			// active AND the input is empty; otherwise it types normally.
			if m.searchQuery != "" && strings.TrimSpace(m.input.Value()) == "" {
				m.searchStep(1, m.selected+1)
				return m, nil
			}
		case "N":
			if m.searchQuery != "" && strings.TrimSpace(m.input.Value()) == "" {
				m.searchStep(-1, m.selected-1)
				return m, nil
			}
		case "{":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(-1, "question_asked")
				return m, nil
			}
		case "}":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(1, "question_asked")
				return m, nil
			}
		case "(":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(-1, "review_submitted")
				return m, nil
			}
		case ")":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(1, "review_submitted")
				return m, nil
			}
		case "<":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(-1, "commit_made")
				return m, nil
			}
		case ">":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(1, "commit_made")
				return m, nil
			}
		case "[":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(-1, "session_error")
				return m, nil
			}
		case "]":
			if strings.TrimSpace(m.input.Value()) == "" {
				m.jumpToEvent(1, "session_error")
				return m, nil
			}
		case "pgup":
			m.vp.HalfPageUp()
			m.follow = m.vp.AtBottom()
			return m, nil
		case "pgdown":
			m.vp.HalfPageDown()
			m.follow = m.vp.AtBottom()
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				// While paused, an empty Enter resumes the agent unchanged (§18.7).
				if m.paused {
					return m, m.resume()
				}
				// Empty input: Enter expands/collapses the selected turn — unless the
				// selected row is a commit_made, in which case Enter drills into the
				// commit's diff overlay (task 0140).
				if m.selected >= 0 {
					if m.selected < len(m.evs) && m.evs[m.selected].Type == "commit_made" {
						ev := m.evs[m.selected]
						if cmd := m.openCommitDiff(dataField(ev, "sha"), dataField(ev, "message")); cmd != nil {
							return m, cmd
						}
					}
					m.toggle(m.selected)
				}
				return m, nil
			}
			m.input.SetValue("")
			m.relayout()
			// While paused, a non-empty Enter steers: send the correction AND
			// resume so the agent continues with it (spec §18.7).
			if m.paused {
				m.follow = true
				return m, tea.Sequence(m.sendInput(text), m.resume())
			}
			// If a question is pending, answer it as free text (option_index -1);
			// otherwise it's a prod handled by SendInput.
			if m.wizActive {
				m.follow = true
				cmd := m.recordWizAnswer(-1, text, false)
				if cmd == nil {
					return m, nil
				}
				return m, cmd
			}
			if m.pending != "" {
				m.pending = ""
				m.follow = true
				return m, m.answerQuestion(-1, text)
			}
			m.follow = true
			return m, m.sendInput(text)
		}
	}
	if m.picking {
		return m, nil
	}
	var icmd tea.Cmd
	m.input, icmd = m.input.Update(msg)
	m.relayout()
	return m, icmd
}

func (m model) sessionView() string {
	top := m.statusBar()
	body := ""
	if m.ready {
		body = m.overlaySelection(m.vp.View())
	}
	// While `/` search is being typed, a single-row search bar replaces the whole
	// footer stack (input/picker/wizard) and help line (task 0116). footerStackHeight
	// returns 0 so relayout leaves exactly one row for it below the viewport.
	if m.searching {
		return top + "\n" + body + "\n" + m.searchBar()
	}
	// A confirmed (non-entry) query leads the help line with a live match counter
	// and the n/N · esc-clear hint.
	searchHint := ""
	if m.searchQuery != "" {
		total, cur := m.searchCount()
		searchHint = fmt.Sprintf(" ⌕ %q %d/%d · n/N next/prev · esc clear ·", m.searchQuery, cur, total)
	}
	if m.wizActive {
		overview := m.wizardView()
		if m.picking {
			help := m.footer(" ↑↓/1–9 choose · enter select · ‹other…› to type · pgup/pgdn scroll · ctrl+b backlog · esc settings")
			return top + "\n" + body + "\n" + overview + "\n" + m.pickerView() + "\n" + help
		}
		help := m.footer(" type your answer + enter · esc settings")
		return top + "\n" + body + "\n" + overview + "\n" + m.inputRow() + "\n" + help
	}
	if m.picking {
		help := m.footer(" ↑↓/1–9 choose · enter select · pgup/pgdn scroll · ctrl+b backlog · esc settings")
		return top + "\n" + body + "\n" + m.pickerView() + "\n" + help
	}
	if m.paused {
		help := m.footer(" ⏸ paused — type a correction + enter to steer · enter to resume · esc settings")
		return top + "\n" + body + "\n" + m.inputRow() + "\n" + help
	}
	help := m.footer(searchHint + " ? help · enter send/expand · shift+enter newline · ↑↓ select · click expand · drag copy · pgup/pgdn scroll · " + m.interruptKeyHint() + " interrupt · esc settings · ctrl+b backlog · ctrl+o browse · ctrl+n new task")
	if m.mode == "work" {
		switch {
		case m.looping:
			help = m.footer(searchHint + " ? help · shift+tab halt loop · enter send/expand · ↑↓ select · pgup/pgdn scroll · " + m.interruptKeyHint() + " interrupt · esc settings")
		case m.loopArmed:
			help = m.footer(searchHint + " ? help · shift+tab disarm loop · enter send/expand · ↑↓ select · pgup/pgdn scroll · " + m.interruptKeyHint() + " interrupt · esc settings")
		default:
			help = m.footer(searchHint + " ? help · shift+tab arm loop · enter send/expand · ↑↓ select · pgup/pgdn scroll · " + m.interruptKeyHint() + " interrupt · esc settings")
		}
	}
	if m.sessionFinished() {
		// A finished (idle / stream-closed), non-looping session leads the footer with
		// a clean way back to the menu (task 0127). This takes precedence over the
		// work-mode loop-toggle hints above.
		help = m.footer(searchHint + " ✔ session finished — q return to menu · ? help · enter expand · ↑↓ select · pgup/pgdn scroll · esc settings")
	}
	return top + "\n" + body + "\n" + m.inputRow() + "\n" + help
}

// interruptKeyHint returns the interrupt chord to advertise in the footer. ctrl+i
// is byte-identical to Tab (0x09), so it only reaches the runtime on terminals
// that report kitty keyboard-protocol disambiguation; everywhere else we show
// ctrl+x, the universal fallback that is always delivered as a distinct byte.
func (m model) interruptKeyHint() string {
	if m.keyEnhanced {
		return "ctrl+i"
	}
	return "ctrl+x"
}

// footer renders a single-row help/status line, clamped to the terminal width so
// it can never wrap to a second physical row. Without this clamp a long help line
// wraps, overflowing the H-row frame and corrupting Bubble Tea's line accounting —
// which visually shows up as the input box overlapping the agent's last output
// line. A zero width (before the first WindowSizeMsg) is a no-op.
//
// It is the session view's footer; it delegates to footerBar so the clamp is
// byte-identical to the one shared by every other screen.
func (m model) footer(text string) string {
	return m.footerBar(text)
}

// spinnerCmd arms the activity spinner's tick loop when there is activity to
// indicate (the session is running or a quick-capture RPC is in flight) and it is
// not already ticking. It returns nil otherwise so we never stack duplicate tick
// commands. The pointer receiver lets it record that a tick is in flight.
func (m *model) spinnerCmd() tea.Cmd {
	if (m.status == "running" || m.captureBusy) && !m.spinning {
		m.spinning = true
		return m.spin.Tick
	}
	return nil
}

// notifyOut is where terminal notifications (BEL / OSC 9) are written. It is a
// package-level var so tests can capture the emitted bytes; in production it is
// the real stdout the TUI already renders to.
var notifyOut io.Writer = os.Stdout

// notifyTrigger reports whether a live event type warrants a bell / desktop
// notification when the user may be looking elsewhere (task 0108).
func notifyTrigger(t string) bool {
	switch t {
	case "question_asked", "session_idle", "session_error", "interrupted":
		return true
	}
	return false
}

// maybeNotify emits a terminal bell and/or OSC 9 desktop notification for a
// genuinely-new live event, gated by client prefs. It is called only from the
// live subscription path (evMsg) — never from transcript/replay loads — and it
// suppresses events whose timestamp predates the subscribe instant so the
// daemon's full-log replay on reopen stays silent (task 0108).
func (m *model) maybeNotify(ev *v1.Event) {
	if ev == nil || (!m.prefs.NotifyBell && !m.prefs.NotifyDesktop) {
		return
	}
	if !notifyTrigger(ev.Type) {
		return
	}
	// While auto-looping, session_idle just means the current task finished and
	// the loop will advance itself — a bell per task would be noise. Keep the
	// attention-worthy events (question/error/interrupt).
	if m.looping && ev.Type == "session_idle" {
		return
	}
	// Auto-answered questions (unattended execution) never need the user, so a bell
	// would be a false alarm.
	if ev.Type == "question_asked" && dataField(ev, "auto") == "true" {
		return
	}
	// Only notify for events newer than the subscribe instant; earlier ones are
	// the daemon replaying the persisted log on reopen.
	ts, err := time.Parse(time.RFC3339, ev.Ts)
	if err != nil {
		return
	}
	if !m.notifyAfter.IsZero() && ts.Before(m.notifyAfter) {
		return
	}

	var b []byte
	if m.prefs.NotifyBell {
		b = append(b, '\a')
	}
	if m.prefs.NotifyDesktop {
		b = append(b, notifyOSC9(ev)...)
	}
	if len(b) > 0 {
		// Single Write so the escape bytes can't interleave mid-frame with the
		// renderer's output to the same file.
		_, _ = notifyOut.Write(b)
	}
}

// notifyOSC9 builds an OSC 9 desktop-notification escape sequence for an event:
// ESC ] 9 ; <text> BEL.
func notifyOSC9(ev *v1.Event) []byte {
	return []byte("\x1b]9;" + notifyText(ev) + "\x07")
}

// notifyText picks the desktop-notification body for an event: the question
// text for question_asked (truncated, control chars stripped), else a short
// labelled status line.
func notifyText(ev *v1.Event) string {
	switch ev.Type {
	case "question_asked":
		q := sanitizeNotify(dataField(ev, "question"))
		if q == "" {
			// Batch (multi-question) asks carry their prompts under "questions"
			// rather than "question"; surface the first one.
			if qs := dataQuestions(ev); len(qs) > 0 {
				q = sanitizeNotify(qs[0].prompt)
			}
		}
		if q == "" {
			return "ycc: question waiting"
		}
		return "ycc: " + q
	case "session_idle":
		return "ycc: session idle"
	case "session_error":
		return "ycc: session error"
	case "interrupted":
		return "ycc: interrupted"
	}
	return "ycc"
}

// sanitizeNotify strips control characters (which would corrupt the escape
// sequence) and truncates to a sane length for a notification body.
func sanitizeNotify(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	const max = 120
	// Truncate on a rune boundary so a multibyte rune can't be split.
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}
