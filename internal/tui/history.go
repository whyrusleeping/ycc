// This file owns session history browsing and transcript modals.
package tui

import (
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// fetchHistory loads the persisted session history for the previous-sessions
// screen (spec §18.6), scoped to the current project.
func (m model) fetchHistory() tea.Msg {
	resp, err := m.client.ListSessionHistory(m.ctx, connect.NewRequest(&v1.ListSessionHistoryRequest{Project: m.project}))
	if err != nil {
		return historyMsg{err: err}
	}
	return historyMsg{sessions: resp.Msg.Sessions}
}

// updateHistory handles the session browser (spec §18.6): navigate the list of
// persisted + live sessions, Enter drills into a read-only replayed transcript,
// `o` reopens the selected session via ResumeSession, `r` refreshes, Esc/q backs
// out (transcript → list, list → menu).
func (m model) updateHistory(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Transcript drill-in: a read-only replayed view that scrolls via the viewport.
	if m.historyTranscript {
		// Mouse: wheel scrolls, and a left-button drag selects + copies the
		// region to the clipboard on release (select.go), matching the live
		// session view. A plain click is a no-op here (rows expand via enter).
		switch mm := msg.(type) {
		case tea.MouseWheelMsg:
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		case tea.MouseClickMsg:
			if mm.Button == tea.MouseLeft {
				m.selMouseDown(mm.X, mm.Y)
			}
			return m, nil
		case tea.MouseMotionMsg:
			m.selMouseMotion(mm.X, mm.Y)
			return m, nil
		case tea.MouseReleaseMsg:
			if text, dragged := m.selMouseUp(); dragged && text != "" {
				return m, tea.Batch(tea.SetClipboard(text), m.noteFlash("copied ✓"))
			}
			return m, nil
		}
		key, ok := msg.(tea.KeyMsg)
		if !ok {
			return m, nil
		}
		// While the search bar owns input (task 0116), keystrokes edit the query
		// and incrementally re-jump the selection. Unconditional here (no input
		// textarea to protect).
		if m.searching {
			switch key.String() {
			case "ctrl+c":
				return m.confirmQuit()
			case "esc":
				m.clearSearch()
				return m, nil
			case "enter":
				// Confirm: keep the query active for n/N.
				m.searching = false
				return m, nil
			case "backspace":
				if r := []rune(m.searchQuery); len(r) > 0 {
					m.searchQuery = string(r[:len(r)-1])
				}
				m.runSearch()
				return m, nil
			default:
				if t := key.Key().Text; t != "" {
					m.searchQuery += t
					m.runSearch()
				}
				return m, nil
			}
		}
		switch key.String() {
		case "ctrl+c":
			return m.confirmQuit()
		case "esc":
			// A transcript search intercepts the first esc: clear it and stay in
			// the transcript. A second esc backs out to the list.
			if m.searchQuery != "" {
				m.clearSearch()
				return m, nil
			}
			fallthrough
		case "q", "backspace", "left":
			// Back to the list: drop the transient transcript event state.
			m.historyTranscript = false
			m.historyTransID = ""
			m.evs = nil
			m.expanded = map[int]bool{}
			m.invalidateRender()
			m.deliveredSeqs = map[int64]bool{}
			m.eventStart = nil
			m.selected = -1
			m.clearSearch()
			if m.ready {
				m.rebuild()
			}
			return m, nil
		case "/":
			// Enter transcript search (unconditional in the read-only transcript).
			m.searching = true
			m.searchQuery = ""
			return m, nil
		case "n":
			m.searchStep(1, m.selected+1)
			return m, nil
		case "N":
			m.searchStep(-1, m.selected-1)
			return m, nil
		case "{":
			m.jumpToEvent(-1, "question_asked")
			return m, nil
		case "}":
			m.jumpToEvent(1, "question_asked")
			return m, nil
		case "(":
			m.jumpToEvent(-1, "review_submitted")
			return m, nil
		case ")":
			m.jumpToEvent(1, "review_submitted")
			return m, nil
		case "<":
			m.jumpToEvent(-1, "commit_made")
			return m, nil
		case ">":
			m.jumpToEvent(1, "commit_made")
			return m, nil
		case "[":
			m.jumpToEvent(-1, "session_error")
			return m, nil
		case "]":
			m.jumpToEvent(1, "session_error")
			return m, nil
		case "enter":
			// Enter on a selected commit_made row drills into that commit's diff
			// overlay (task 0140); otherwise it reopens the session (like `o`).
			if m.selected >= 0 && m.selected < len(m.evs) && m.evs[m.selected].Type == "commit_made" {
				ev := m.evs[m.selected]
				if cmd := m.openCommitDiff(dataField(ev, "sha"), dataField(ev, "message")); cmd != nil {
					return m, cmd
				}
			}
			fallthrough
		case "o":
			// Reopen the session whose transcript we're viewing (resume = replay).
			m.historyTranscript = false
			m.clearSearch()
			m.historyMsgTxt = "reopening " + short(m.historyTransID) + "…"
			return m, m.reopenSession(m.historyTransID)
		}
		// Everything else (↑/↓, pgup/pgdn, wheel) scrolls the transcript viewport.
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.state = stateMenu
		m.historyWaitingOnly = false
		return m, m.refreshMenu()
	case "r":
		m.historyMsgTxt = "loading…"
		return m, m.fetchHistory
	case "up":
		m.historyCursor = navUp(m.historyCursor)
		return m, nil
	case "down":
		m.historyCursor = navDown(m.historyCursor, len(m.history))
		return m, nil
	case "enter":
		// Drill into a read-only replayed transcript of the selected session.
		if len(m.history) == 0 {
			return m, nil
		}
		sel := m.history[m.historyCursor]
		m.historyMsgTxt = "loading transcript…"
		return m, m.fetchTranscript(sel.SessionId)
	case "o":
		// Reopen the selected session directly from the list (resume = replay).
		if len(m.history) == 0 {
			return m, nil
		}
		sel := m.history[m.historyCursor]
		m.historyMsgTxt = "reopening " + short(sel.SessionId) + "…"
		return m, m.reopenSession(sel.SessionId)
	}
	return m, nil
}

// openHistModal opens the session browser as a read-only modal over the current
// live session (task 0112). It reuses the shared history list/cursor but keeps
// the live session's event pipeline untouched. Callers should return
// m.fetchHistory to populate the list.
func (m *model) openHistModal() {
	m.histModal = true
	m.histModalTranscript = false
	m.histModalID = ""
	m.historyCursor = 0
	m.history = nil
	m.historyWaitingOnly = false
	m.historyMsgTxt = "loading…"
}

// updateHistoryModal handles the session browser when it is open as a modal over
// a live session (task 0112). It mirrors updateHistory's navigation but is
// strictly read-only: there is no `o`/enter reopen (reopening over a live session
// is a footgun). Transcripts scroll a separate viewport and support line-based
// `/` search (n/N, esc) plus {}()<>[] jump-to-event keys (task 0119), so the live
// session behind the modal is never disturbed.
func (m model) updateHistoryModal(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Transcript drill-in: a read-only replayed view that scrolls its own viewport.
	// It supports the same `/` search (n/N, esc) and {}()<>[] jump-to-event keys as
	// the live transcript, but line-based over the rendered content so the live
	// session behind the modal (m.evs/m.vp/search state) is never touched (0119).
	if m.histModalTranscript {
		key, ok := msg.(tea.KeyMsg)
		if !ok {
			return m, nil
		}
		// While the search bar owns input, keystrokes edit the query and
		// incrementally re-jump the current line.
		if m.histModalSearching {
			switch key.String() {
			case "ctrl+c":
				return m.confirmQuit()
			case "esc":
				m.histModalSearching = false
				m.histModalQuery = ""
				m.histModalCurLine = -1
				m.applyHistModalContent()
				return m, nil
			case "enter":
				// Confirm: keep the query active for n/N.
				m.histModalSearching = false
				return m, nil
			case "backspace":
				if r := []rune(m.histModalQuery); len(r) > 0 {
					m.histModalQuery = string(r[:len(r)-1])
				}
				m.histRunSearch()
				return m, nil
			default:
				if t := key.Key().Text; t != "" {
					m.histModalQuery += t
					m.histRunSearch()
				}
				return m, nil
			}
		}
		switch key.String() {
		case "ctrl+c":
			return m.confirmQuit()
		case "esc":
			// A transcript search intercepts the first esc: clear it and stay in the
			// transcript. A second esc backs out to the list.
			if m.histModalQuery != "" {
				m.histModalQuery = ""
				m.histModalCurLine = -1
				m.applyHistModalContent()
				return m, nil
			}
			fallthrough
		case "q", "backspace", "left":
			// Back to the list; drop the transient transcript + nav state.
			m.histModalTranscript = false
			m.histModalID = ""
			m.resetHistModalNav()
			return m, nil
		case "/":
			// Enter transcript search (unconditional in the read-only transcript).
			m.histModalSearching = true
			m.histModalQuery = ""
			return m, nil
		case "n":
			m.histSearchStep(1, m.histModalCurLine+1)
			return m, nil
		case "N":
			m.histSearchStep(-1, m.histModalCurLine-1)
			return m, nil
		case "{":
			m.histJump(-1, "question_asked")
			return m, nil
		case "}":
			m.histJump(1, "question_asked")
			return m, nil
		case "(":
			m.histJump(-1, "review_submitted")
			return m, nil
		case ")":
			m.histJump(1, "review_submitted")
			return m, nil
		case "<":
			m.histJump(-1, "commit_made")
			return m, nil
		case ">":
			m.histJump(1, "commit_made")
			return m, nil
		case "[":
			m.histJump(-1, "session_error")
			return m, nil
		case "]":
			m.histJump(1, "session_error")
			return m, nil
		case "enter":
			// Drill into a commit's diff when the current line is a commit_made
			// event block (task 0140). Otherwise fall through to viewport handling.
			for _, el := range m.histModalEventLines {
				if el.line == m.histModalCurLine && el.typ == "commit_made" && el.idx >= 0 && el.idx < len(m.histModalEvents) {
					ev := m.histModalEvents[el.idx]
					if cmd := m.openCommitDiff(dataField(ev, "sha"), dataField(ev, "message")); cmd != nil {
						return m, cmd
					}
				}
			}
		}
		// Everything else (↑/↓, pgup/pgdn, wheel) scrolls the transcript viewport.
		// No `o`/enter reopen: browsing from a live session is read-only.
		var cmd tea.Cmd
		m.histModalVP, cmd = m.histModalVP.Update(msg)
		return m, cmd
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		// Close the modal and return to the live session behind it.
		m.histModal = false
		m.resetHistModalNav()
		return m, nil
	case "r":
		m.historyMsgTxt = "loading…"
		return m, m.fetchHistory
	case "up":
		m.historyCursor = navUp(m.historyCursor)
		return m, nil
	case "down":
		m.historyCursor = navDown(m.historyCursor, len(m.history))
		return m, nil
	case "enter":
		// Drill into a read-only replayed transcript of the selected session.
		if len(m.history) == 0 {
			return m, nil
		}
		sel := m.history[m.historyCursor]
		m.historyMsgTxt = "loading transcript…"
		return m, m.fetchTranscript(sel.SessionId)
	}
	return m, nil
}

// refreshHistModalVP (re)sizes the modal session-browser transcript viewport and
// loads a stateless render of the replayed events into it (task 0112). It never
// touches the live session's m.vp/m.evs. It also captures the rendered lines and
// per-event start-line metadata used by the modal's line-based search/jump
// navigation (task 0119).
func (m *model) refreshHistModalVP(events []*v1.Event) {
	if !m.ready {
		return
	}
	h := m.h - 2 // one row for the title bar, one for the footer
	if h < 3 {
		h = 3
	}
	if m.histModalVP.Height() == 0 && m.histModalVP.Width() == 0 {
		m.histModalVP = viewport.New(viewport.WithWidth(m.w), viewport.WithHeight(h))
	} else {
		m.histModalVP.SetWidth(m.w)
		m.histModalVP.SetHeight(h)
	}
	m.histModalEvents = events
	content, lines, eventLines := m.renderTranscript(events)
	m.histModalLines = lines
	m.histModalEventLines = eventLines
	m.histModalVP.SetContent(content)
	m.histModalVP.GotoTop()
}

// histEventLine records, per VISIBLE event block in a modal transcript render,
// the content line its block starts on plus its event Type — the metadata that
// lets the {}()<>[] jump keys land on the right event without the live event
// pipeline (task 0119).
type histEventLine struct {
	line int    // start content line of the event block
	typ  string // event Type
	idx  int    // index into the rendered events slice (for the commit-diff drill-in)
}

// renderTranscriptContent renders a replayed event log to a string using the same
// pipeline as rebuild()/the live session view, WITHOUT mutating the live model
// (task 0112). It works on a scratch copy of the model whose event state and
// caches are freshly allocated, so renderBlock's cache mutations never leak into
// the live session's shared maps.
func (m model) renderTranscriptContent(events []*v1.Event) string {
	content, _, _ := m.renderTranscript(events)
	return content
}

// renderTranscript renders a replayed event log statelessly (like
// renderTranscriptContent) and additionally returns the rendered content lines
// and per-event start-line metadata used by the modal transcript's line-based
// search + jump navigation (task 0119). It never mutates the live model.
func (m model) renderTranscript(events []*v1.Event) (content string, lines []string, eventLines []histEventLine) {
	scratch := m
	scratch.evs = events
	scratch.expanded = map[int]bool{}
	scratch.bodyCache = map[int]string{}
	scratch.blockCache = map[int]string{}
	scratch.hiddenCache = map[int]bool{}
	scratch.deliveredSeqs = deliveredSeqSet(events)
	scratch.eventStart = nil
	scratch.selected = -1
	scratch.follow = false
	// The live session's pending-question UI state (footer picker / wizard) must
	// not leak into a replayed transcript: there is no picker below it, so its
	// question rows always render in full, never as an "answer below ↓" pointer.
	scratch.picking = false
	scratch.pendingSeq = 0
	scratch.wizActive = false
	var b strings.Builder
	line := 0
	for i, ev := range events {
		if scratch.hiddenRow(i) {
			continue
		}
		block := scratch.renderBlock(i, ev)
		eventLines = append(eventLines, histEventLine{line: line, typ: ev.Type, idx: i})
		b.WriteString(block)
		b.WriteByte('\n')
		line += strings.Count(block, "\n") + 1
	}
	content = b.String()
	if content != "" {
		lines = strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	}
	return content, lines, eventLines
}

// resetHistModalNav clears all modal-transcript search/jump state. Called when a
// transcript loads, when backing out to the list, and when closing the modal.
func (m *model) resetHistModalNav() {
	m.histModalSearching = false
	m.histModalQuery = ""
	m.histModalCurLine = -1
	m.histModalEvents = nil
	m.histModalLines = nil
	m.histModalEventLines = nil
}

// histLineMatches reports whether content line i (ansi-stripped, lower-cased)
// contains q, which must already be lower-cased.
func (m *model) histLineMatches(i int, q string) bool {
	if q == "" || i < 0 || i >= len(m.histModalLines) {
		return false
	}
	return strings.Contains(strings.ToLower(ansi.Strip(m.histModalLines[i])), q)
}

// histSearchStep moves histModalCurLine to the nearest content line matching the
// active query, scanning in direction dir (+1 forward / -1 backward) from `from`
// and wrapping around all lines. No-op when there is no query or no match. Drives
// both incremental typing (dir +1 from the current line, inclusive) and n/N.
func (m *model) histSearchStep(dir, from int) {
	q := strings.ToLower(m.histModalQuery)
	n := len(m.histModalLines)
	if q == "" || n == 0 {
		return
	}
	if from < 0 {
		from = 0
	}
	for k := 0; k < n; k++ {
		i := ((from+dir*k)%n + n) % n
		if m.histLineMatches(i, q) {
			m.histModalCurLine = i
			m.applyHistModalContent()
			return
		}
	}
}

// histRunSearch re-jumps the current line to the first match at or after the
// current line after each incremental edit of the query.
func (m *model) histRunSearch() {
	from := m.histModalCurLine
	if from < 0 {
		from = 0
	}
	m.histSearchStep(1, from)
}

// histSearchCount returns the total number of matching content lines and the
// 1-based ordinal of the match at or before the current line (0 when the current
// line isn't on a match). Feeds the footer counter ⌕ "q" k/N.
func (m *model) histSearchCount() (total, cur int) {
	q := strings.ToLower(m.histModalQuery)
	if q == "" {
		return 0, 0
	}
	for i := range m.histModalLines {
		if m.histLineMatches(i, q) {
			total++
			if i <= m.histModalCurLine {
				cur = total
			}
		}
	}
	return total, cur
}

// histJump moves histModalCurLine to the nearest event block start line in
// direction dir (+1 forward / -1 backward) whose Type is one of types. Unlike
// search it does NOT wrap: a no-op when there is no such event past the current
// line. Drives the {}()<>[] jump keys.
func (m *model) histJump(dir int, types ...string) {
	if len(m.histModalEventLines) == 0 {
		return
	}
	cur := m.histModalCurLine
	if cur < 0 {
		// No cursor yet: start just before the first / just past the last line so
		// the first forward/backward match is found (mirrors jumpToEvent).
		if dir > 0 {
			cur = -1
		} else {
			cur = len(m.histModalLines)
		}
	}
	if dir > 0 {
		for _, el := range m.histModalEventLines {
			if el.line <= cur {
				continue
			}
			if typeMatches(el.typ, types) {
				m.histModalCurLine = el.line
				m.applyHistModalContent()
				return
			}
		}
		return
	}
	for i := len(m.histModalEventLines) - 1; i >= 0; i-- {
		el := m.histModalEventLines[i]
		if el.line >= cur {
			continue
		}
		if typeMatches(el.typ, types) {
			m.histModalCurLine = el.line
			m.applyHistModalContent()
			return
		}
	}
}

// typeMatches reports whether t is one of types.
func typeMatches(t string, types []string) bool {
	for _, want := range types {
		if t == want {
			return true
		}
	}
	return false
}

// applyHistModalContent re-sets the modal viewport content, highlighting the
// current line (histModalCurLine) with a reverse style and scrolling it roughly
// centered into view. With no current line it renders the plain content.
func (m *model) applyHistModalContent() {
	if len(m.histModalLines) == 0 {
		return
	}
	cur := m.histModalCurLine
	if cur < 0 || cur >= len(m.histModalLines) {
		m.histModalVP.SetContent(strings.Join(m.histModalLines, "\n"))
		return
	}
	lines := make([]string, len(m.histModalLines))
	copy(lines, m.histModalLines)
	// Strip the matched line's own ansi so the reverse highlight reads cleanly.
	lines[cur] = histHighlightStyle.Render(ansi.Strip(lines[cur]))
	m.histModalVP.SetContent(strings.Join(lines, "\n"))
	// Center the current line in the viewport, clamped to valid offsets.
	off := cur - m.histModalVP.Height()/2
	if max := len(m.histModalLines) - m.histModalVP.Height(); off > max {
		off = max
	}
	if off < 0 {
		off = 0
	}
	m.histModalVP.SetYOffset(off)
}

// historyView renders the session browser (spec §18.6): a navigable list of
// persisted + live sessions, most-recent first, that can be inspected (read-only
// transcript) or reopened. When a transcript is open it renders that instead.
func (m model) historyView() string {
	if m.historyTranscript {
		return m.transcriptView()
	}
	emptyMsg := m.historyMsgTxt
	if emptyMsg == "" {
		emptyMsg = "no previous sessions"
	}
	b := browser{
		title:  " ycc — sessions ",
		cursor: m.historyCursor,
		hint:   "↑/↓ choose · enter transcript · o reopen · r refresh · esc/q back",
		empty:  emptyMsg,
	}
	if m.historyWaitingOnly {
		b.title = " ycc — sessions waiting for you "
		if emptyMsg == "no previous sessions" {
			b.empty = "(no sessions waiting)"
		}
	}
	b.rows = m.historyRows()
	return m.browserCard(b)
}

// historyRows builds the session-browser list rows shared by the full-state
// session browser (historyView) and the read-only modal variant (histModalView),
// keeping the row format identical between them (task 0112).
func (m model) historyRows() []browserRow {
	// Clamp the title column so a row stays on a single physical line.
	tw := 48
	if m.w > 0 && m.w-4 < tw {
		tw = m.w - 4
	}
	if tw < 1 {
		tw = 1
	}
	var rows []browserRow
	for _, s := range m.history {
		// Prefer a derived title; fall back to the short id. Sessions that worked
		// backlog tasks are prefixed with those task ids so the list shows at a
		// glance which task each session was on.
		title := strings.TrimSpace(s.Title)
		if title == "" {
			title = short(s.SessionId)
		}
		if len(s.FocusTasks) > 0 {
			title = "[" + strings.Join(s.FocusTasks, ",") + "] " + title
		}
		meta := s.Mode + " · " + s.Status
		if s.Live {
			meta += " · live"
		}
		if when := historyWhen(s); when != "" {
			meta += " · " + when
		}
		rows = append(rows, browserRow{
			text:   fmt.Sprintf("%-*s", tw, trunc(title, tw)),
			suffix: "  " + dimStyle.Render(meta),
		})
	}
	return rows
}

// histModalView renders the read-only session browser modal shown over a live
// session (task 0112). When a transcript is drilled into it shows that instead.
// Unlike historyView it advertises no `o reopen` — browsing from a live session
// is strictly read-only.
func (m model) histModalView() string {
	if m.histModalTranscript {
		title := short(m.histModalID)
		if m.historyCursor < len(m.history) {
			if t := strings.TrimSpace(m.history[m.historyCursor].Title); t != "" {
				title = t
			}
		}
		top := m.titleBar(" ycc — transcript · " + title + " ")
		body := ""
		if m.ready {
			body = m.histModalVP.View()
		}
		var help string
		switch {
		case m.histModalSearching:
			help = m.histModalSearchBar()
		case m.histModalQuery != "":
			total, cur := m.histSearchCount()
			help = m.footerBar(fmt.Sprintf(" ⌕ %q %d/%d · n/N next/prev · esc clear · esc/q back", m.histModalQuery, cur, total))
		default:
			help = m.footerBar(" ↑↓/pgup/pgdn scroll · / search · {}()<>[] jump · <> commit · enter diff · esc/q back · read-only")
		}
		return top + "\n" + body + "\n" + help
	}
	emptyMsg := m.historyMsgTxt
	if emptyMsg == "" {
		emptyMsg = "no previous sessions"
	}
	b := browser{
		title:  " ycc — sessions ",
		cursor: m.historyCursor,
		hint:   "↑/↓ choose · enter transcript · r refresh · esc/q back",
		empty:  emptyMsg,
	}
	b.rows = m.historyRows()
	return m.browserCard(b)
}

// transcriptView renders the read-only replayed transcript of a session (spec
// §18.6): the same scrollable event viewport as the live session view, but with
// no input box and read-only.
func (m model) transcriptView() string {
	title := short(m.historyTransID)
	if m.historyCursor < len(m.history) {
		if t := strings.TrimSpace(m.history[m.historyCursor].Title); t != "" {
			title = t
		}
	}
	top := m.titleBar(" ycc — transcript · " + title + " ")
	body := ""
	if m.ready {
		body = m.overlaySelection(m.vp.View())
	}
	var help string
	switch {
	case m.searching:
		help = m.searchBar()
	case m.searchQuery != "":
		total, cur := m.searchCount()
		help = m.footerBar(fmt.Sprintf(" ⌕ %q %d/%d · n/N next/prev · esc clear · o reopen · esc/q back", m.searchQuery, cur, total))
	default:
		help = m.footerBar(" ↑↓/pgup/pgdn scroll · / search · {}()<>[] jump · <> commit · enter diff/reopen · o reopen · esc/q back")
	}
	return top + "\n" + body + "\n" + help
}

// historyWhen renders a session's last-activity (or start) timestamp compactly
// for the previous-sessions list, returning "" when neither is available.
func historyWhen(s *v1.SessionSummary) string {
	ts := s.LastActivity
	if ts == "" {
		ts = s.StartedAt
	}
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("2006-01-02 15:04")
}

// histModalSearchBar renders the modal session-browser transcript's search-entry
// line while `/` search is being typed (task 0119) — the line-based analogue of
// searchBar, counting matching content lines rather than events.
func (m model) histModalSearchBar() string {
	total, cur := m.histSearchCount()
	counter := dimStyle.Render("no matches")
	if total > 0 {
		counter = fmt.Sprintf("%d/%d", cur, total)
	}
	return m.footerBar(" ⌕ " + m.histModalQuery + "▌ · " + counter + dimStyle.Render(" · enter keep · esc cancel"))
}
