// This file owns the shared browser list and browse-menu routing.
package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// --- shared list+detail browser surface (spec §18.5/§18.6/§20.5) ---
//
// browser is the reusable modal list+detail component behind every browser
// (backlog today, sessions, and a future cost view): it owns generic list
// navigation (cursor up/down with clamping) and bordered-card rendering via
// modalCard. Each specific browser supplies the rendered row text, footer hint,
// and any extra keybindings — the owner handles enter/extra keys while this
// component handles up/down + cursor clamp + rendering. It deliberately stays
// small: factor the duplicated list+card pattern, don't over-engineer.
type browser struct {
	title  string
	rows   []browserRow
	cursor int
	hint   string
	empty  string // message shown when there are no rows
}

// browserRow is one list entry: text is selection-highlighted when the row is
// under the cursor, while suffix (dim meta/tags) is appended unstyled so a row
// can carry secondary detail without it being swallowed by the selection style.
type browserRow struct {
	text   string
	suffix string
}

// navUp / navDown / clampCursor are the single source of truth for navigable-list
// cursor arithmetic, shared by the browser component AND the specific update
// handlers (backlog/history/browse) so cursor movement is never re-implemented
// inline. navDown/clampCursor take the row count n; an empty list clamps to 0.
func navUp(cursor int) int {
	if cursor > 0 {
		return cursor - 1
	}
	return cursor
}

func navDown(cursor, n int) int {
	if cursor < n-1 {
		return cursor + 1
	}
	return cursor
}

func clampCursor(cursor, n int) int {
	if cursor >= n {
		cursor = n - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	return cursor
}

// listWindow returns the [start,end) bounds of the visible slice of n items
// shown in a scroll window of at most `size` rows, keeping `cursor` visible.
// It center-anchors the cursor and clamps to the list bounds, so the window
// scrolls one row at a time as the cursor moves and the selected item always
// stays on screen. size<=0 or n<=size means no clipping (returns 0,n).
func listWindow(cursor, n, size int) (start, end int) {
	if size <= 0 || n <= size {
		return 0, n
	}
	start = cursor - size/2
	if start < 0 {
		start = 0
	}
	end = start + size
	if end > n {
		end = n
		start = n - size
	}
	return start, end
}

func (b *browser) up() { b.cursor = navUp(b.cursor) }

func (b *browser) down() { b.cursor = navDown(b.cursor, len(b.rows)) }

// clamp keeps the cursor within [0, len-1] after the underlying row set changes
// (e.g. a show/hide-done toggle shrinks the list out from under it).
func (b *browser) clamp() { b.cursor = clampCursor(b.cursor, len(b.rows)) }

// browserCard renders a browser's navigable list as a bordered modal card.
func (m model) browserCard(b browser) string {
	var sb strings.Builder
	if len(b.rows) == 0 {
		empty := b.empty
		if empty == "" {
			empty = "(empty)"
		}
		sb.WriteString(dimStyle.Render(empty) + "\n")
	}
	// Window the rows so the card never overruns the terminal vertically.
	// modalCard's chrome is 6 non-content rows (title + blank + blank + footer
	// + top/bottom border), so the content budget is m.h-6. Before the first
	// WindowSizeMsg (m.h == 0) keep the legacy behaviour of rendering all rows.
	budget := len(b.rows)
	if m.h > 0 {
		budget = m.h - 6
		if budget < 1 {
			budget = 1
		}
	}
	start, end := listWindow(b.cursor, len(b.rows), budget)
	hint := b.hint
	if start > 0 || end < len(b.rows) {
		hint = fmt.Sprintf("%s · %d–%d/%d", b.hint, start+1, end, len(b.rows))
	}
	for i, r := range b.rows[start:end] {
		cursor := "  "
		text := r.text
		if start+i == b.cursor {
			cursor = selStyle.Render("▸ ")
			text = selStyle.Render(text)
		}
		sb.WriteString(cursor + text + r.suffix + "\n")
	}
	return m.modalCard(b.title, strings.TrimRight(sb.String(), "\n"), hint)
}

// --- browse selector (spec §18.6 / §20.5) ---
//
// browseTargets are the routes the browse selector offers. It is the single
// extension point for the shared browser surface: each row maps to a case in
// updateBrowse — no other plumbing is needed (spec §18.6/§20.5).
var browseTargets = []struct{ label, desc string }{
	{"backlog", "tasks · readiness · drill-in detail"},
	{"plans", "saved runbooks · view markdown"},
	{"sessions", "previous + live · transcript · reopen"},
	{"cost", "subscription allowance + token/cost breakdown"},
	{"workstreams", "parallel worktrees · status · merge/discard"},
	{"digest", "last work-loop run — done · blocked · cost"},
}

// openBrowse enters the browse selector modal.
func (m *model) openBrowse() {
	m.browse = true
	m.browseCursor = 0
}

// updateBrowse handles the browse selector: navigate the routes and Enter opens
// the chosen browser (backlog / sessions). Esc/q dismisses it.
func (m model) updateBrowse(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.browse = false
		return m, nil
	case "up":
		m.browseCursor = navUp(m.browseCursor)
		return m, nil
	case "down":
		m.browseCursor = navDown(m.browseCursor, len(browseTargets))
		return m, nil
	case "enter":
		m.browse = false
		switch browseTargets[m.browseCursor].label {
		case "backlog":
			m.backlog, m.backlogCursor, m.backlogDetail = true, 0, nil
			m.backlogShowDone = false
			m.backlogBlockedOnly = false
			return m, m.fetchBacklog
		case "plans":
			m.plans, m.plansCursor, m.planDetail = true, 0, nil
			return m, m.fetchPlans
		case "sessions":
			// From a live session, open the read-only modal variant (task 0112) so
			// browsing never disturbs (or reopens over) the session behind it. From
			// the menu, use the full-state session browser as before.
			if m.state == stateSession {
				m.openHistModal()
				return m, m.fetchHistory
			}
			m.state = stateHistory
			m.historyCursor = 0
			m.history = nil
			m.historyTranscript = false
			m.historyMsgTxt = "loading…"
			return m, m.fetchHistory
		case "cost":
			// The cost view (spec §20.5, task 0039) opens grouped by task.
			m.cost, m.costCursor = true, 0
			m.costTask, m.costTaskCursor = "", 0
			m.costGroupBy = []string{"task"}
			m.costRows, m.costTotal = nil, nil
			m.subUsageAccounts = nil
			m.costMsg = "loading…"
			m.costGen++
			return m, m.fetchUsage
		case "workstreams":
			m.openWorkstreams()
			return m, tea.Batch(m.fetchWorkstreams, m.wsRefreshTick())
		case "digest":
			// Load the daemon's durable snapshot so the last digest survives a TUI
			// restart. A nil loop keeps the digest browser's empty state.
			m.digest, m.digestCursor = true, 0
			m.loopDigest = nil
			return m, m.fetchWorkLoopDigest()
		}
		return m, nil
	}
	return m, nil
}

// browseView renders the browse selector as a bordered modal card via the shared
// list component.
func (m model) browseView() string {
	b := browser{
		title:  " ycc — browse ",
		cursor: m.browseCursor,
		hint:   "↑/↓ choose · enter open · esc back",
	}
	for _, t := range browseTargets {
		b.rows = append(b.rows, browserRow{text: fmt.Sprintf("%-10s", t.label), suffix: dimStyle.Render(t.desc)})
	}
	return m.browserCard(b)
}
