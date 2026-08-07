// This file owns live transcript selection, search, jumping, and yanking.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func (m *model) moveSelection(d int) {
	if len(m.evs) == 0 {
		return
	}
	if m.selected < 0 {
		m.selected = len(m.evs) - 1
	}
	m.selected += d
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= len(m.evs) {
		m.selected = len(m.evs) - 1
	}
	// Skip hidden rows (folded tool_results and empty model_turns): they share
	// another event's rendered row, so selection should land on the owning
	// visible row, never on them. Travel in the move direction past any hidden
	// row, then snap back if we ran off the end.
	dir := d
	if dir == 0 {
		dir = 1
	}
	for m.hiddenRow(m.selected) {
		next := m.selected + dir
		if next < 0 || next >= len(m.evs) {
			break
		}
		m.selected = next
	}
	for m.hiddenRow(m.selected) && m.selected > 0 {
		m.selected--
	}
	m.follow = m.selected == len(m.evs)-1
	m.rebuild()
	m.ensureVisible()
}

// searchableText returns the plain-text (ansi-stripped) rendering of event i as
// it appears on screen: its type/actor labels and detail-line headline plus,
// when the row is expanded, its rendered body. Hidden rows (folded tool_results,
// empty model_turns, finish-turn echoes, delivery markers) return "" so a folded row
// never matches on its own — its text participates via the owning visible row.
// Used for case-insensitive substring matching by the transcript search.
func (m *model) searchableText(i int) string {
	if i < 0 || i >= len(m.evs) || m.hiddenRow(i) {
		return ""
	}
	ev := m.evs[i]
	var b strings.Builder
	b.WriteString(ev.Type)
	b.WriteByte(' ')
	b.WriteString(ev.Actor)
	b.WriteByte(' ')
	b.WriteString(ansi.Strip(m.detailLineFor(ev)))
	if m.eventExpanded(int(ev.Seq), ev.Type) {
		b.WriteByte(' ')
		b.WriteString(ansi.Strip(m.bodyFor(ev)))
	}
	return b.String()
}

// yankText returns the plain text to copy to the clipboard for an event when the
// user presses `y` on the selected transcript row (task 0141). For events whose
// raw source pastes better than the glamour-rendered body (a commit sha, an error
// message, a model's text) it returns that raw value; otherwise it falls back to
// the on-screen expanded content stripped of styling. Returns "" when there's
// nothing worth copying.
func (m *model) yankText(ev *v1.Event) string {
	if ev == nil {
		return ""
	}
	switch ev.Type {
	case "commit_made":
		return dataField(ev, "sha")
	case "session_error":
		head := sessionErrorHead(ev)
		msg := dataField(ev, "msg")
		switch {
		case head != "" && msg != "":
			return head + "\n" + msg
		case head != "":
			return head
		default:
			return msg
		}
	case "model_turn", "user_input":
		if t := firstField(ev, "text", "report", "question", "answer"); t != "" {
			return strings.TrimSpace(t)
		}
	case "session_idle":
		if t := firstField(ev, "report"); t != "" {
			return strings.TrimSpace(t)
		}
	case "tool_result":
		if t := dataField(ev, "result"); t != "" {
			return strings.TrimSpace(t)
		}
	case "tool_call":
		if a := dataField(ev, "args"); a != "" {
			return strings.TrimSpace(prettyArgs(a))
		}
	}
	return strings.TrimSpace(ansi.Strip(m.bodyFor(ev)))
}

// matchesQuery reports whether event i's searchable text contains q, which must
// already be lower-cased.
func (m *model) matchesQuery(i int, q string) bool {
	if q == "" {
		return false
	}
	return strings.Contains(strings.ToLower(m.searchableText(i)), q)
}

// searchStep moves the selection to the nearest event matching the active query,
// scanning in direction dir (+1 forward / -1 backward) from index `from` and
// wrapping around the whole stream. It is a no-op when there is no query or no
// match. It drives both incremental typing (dir +1 from the current selection,
// which includes it) and n/N cycling (dir ±1 from one past the current match).
func (m *model) searchStep(dir, from int) {
	q := strings.ToLower(m.searchQuery)
	n := len(m.evs)
	if q == "" || n == 0 {
		return
	}
	for k := 0; k < n; k++ {
		i := ((from+dir*k)%n + n) % n
		if m.matchesQuery(i, q) {
			m.selected = i
			m.follow = false
			m.rebuild()
			m.ensureVisible()
			return
		}
	}
}

// runSearch re-jumps the selection to the first match at or after the current
// selection after each incremental edit of the query.
func (m *model) runSearch() {
	from := m.selected
	if from < 0 {
		from = 0
	}
	m.searchStep(1, from)
}

// searchCount returns the total number of matches for the active query and the
// 1-based ordinal of the match at or before the current selection (0 when the
// selection isn't on a match). Feeds the footer counter ⌕ "q" k/N.
func (m *model) searchCount() (total, cur int) {
	q := strings.ToLower(m.searchQuery)
	if q == "" {
		return 0, 0
	}
	for i := range m.evs {
		if m.matchesQuery(i, q) {
			total++
			if i <= m.selected {
				cur = total
			}
		}
	}
	return total, cur
}

// jumpToEvent moves the selection to the nearest non-hidden event in direction
// dir (+1 forward / -1 backward) whose Type is one of types. Unlike search it
// does NOT wrap: a no-op when there is no such event past the current selection.
// Drives the {}()<>[] jump keys.
func (m *model) jumpToEvent(dir int, types ...string) {
	if len(m.evs) == 0 {
		return
	}
	start := m.selected
	if start < 0 {
		if dir > 0 {
			start = -1
		} else {
			start = len(m.evs)
		}
	}
	for i := start + dir; i >= 0 && i < len(m.evs); i += dir {
		if m.hiddenRow(i) {
			continue
		}
		for _, t := range types {
			if m.evs[i].Type == t {
				m.selected = i
				m.follow = false
				m.rebuild()
				m.ensureVisible()
				return
			}
		}
	}
}

// clearSearch resets all transcript-search state. Shared by esc-cancel and the
// pipeline resets (started/transcript load, leaving a transcript).
func (m *model) clearSearch() {
	m.searching = false
	m.searchQuery = ""
}

// searchBar renders the one-row transcript search-entry line shown in place of
// the input/footer while `/` search is being typed (task 0116). It is width-
// clamped like the footer so it can never wrap to a second physical row.
func (m model) searchBar() string {
	total, cur := m.searchCount()
	counter := dimStyle.Render("no matches")
	if total > 0 {
		counter = fmt.Sprintf("%d/%d", cur, total)
	}
	return m.footerBar(" ⌕ " + m.searchQuery + "▌ · " + counter + dimStyle.Render(" · enter keep · esc cancel"))
}
