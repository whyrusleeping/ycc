package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// help.go — the keybinding cheat-sheet modal (task 0111).
//
// SINGLE SOURCE OF TRUTH for the TUI's documented keybindings. The footer is
// width-clamped and can only surface a handful of hints, so the full catalog
// lives here, grouped by context and rendered on `?` (or ctrl+h / ctrl+_).
//
// ▸ MAINTENANCE CONTRACT: whenever you add, remove, or change a key binding in
//   an update* function, update the matching helpSection below in the same
//   change. The sections mirror these handlers — keep them in sync:
//     • home menu ............ updateMenu
//     • session .............. updateSession (main key switch)
//     • question picker ...... updateSession (m.picking branch)
//     • backlog browser ...... updateBacklog (+ detail / status-choice modes)
//     • session browser ..... updateHistory + updateHistoryModal (read-only, over a session)
//     • commit diff .......... updateCommitDiff (git-show overlay, task 0140)
//     • workstreams .......... updateWorkstreams (+ merge / discard prompts)
//     • plans ................ updatePlans
//     • cost ................. updateCost
//     • digest ............... updateDigest
//     • settings overlay ..... updateOverlay
//     • model backends ....... updateModelBackends / mbUpdateList
//     • capture overlay ...... updateCapture
//   Do NOT hand-copy this list elsewhere; render from helpSections().

// helpBind is a single documented binding: its key(s) and what it does.
type helpBind struct {
	keys string
	desc string
}

// helpSection groups bindings that share a context (a screen or modal).
type helpSection struct {
	title string
	binds []helpBind
}

// helpSections returns the full keybinding catalog, grouped by context. It is
// the single source of truth rendered by helpView (see the maintenance contract
// above). Dynamic hints (e.g. the interrupt chord) are resolved from the model.
func (m model) helpSections() []helpSection {
	return []helpSection{
		{"global", []helpBind{
			{"?  ctrl+h", "open this help (ctrl+_ always, ? when the input is empty)"},
			{"ctrl+c", "quit ycc"},
			{"esc", "settings overlay (from menu or a session)"},
			{"ctrl+n", "new backlog task (quick-add capture)"},
			{"ctrl+b", "browse the backlog"},
			{"ctrl+o", "browse selector (backlog · plans · sessions · cost · workstreams · digest)"},
			{"ctrl+r", "browse previous sessions (from a session: read-only)"},
		}},
		{"home menu", []helpBind{
			{"↑ / ↓", "choose a mode"},
			{"← / →", "cycle the interaction level for the next session (when the prompt is empty)"},
			{"tab", "toggle work (loop) on the work entry"},
			{"enter", "start the selected mode with the typed prompt"},
			{"w", "jump to blocked tasks (when any are blocked)"},
			{"s", "open a session waiting for you (when any are waiting)"},
			{"c", "continue the last session (when one exists)"},
			{"type…", "compose an opening prompt"},
		}},
		{"session", []helpBind{
			{"enter", "send input · expand/collapse selected turn · resume when paused"},
			{"shift+enter", "newline in the input"},
			{"↑ / ↓", "select a turn"},
			{"pgup / pgdn", "scroll the transcript"},
			{"click", "expand a turn"},
			{"/", "search the transcript (when the input is empty)"},
			{"n / N", "next / previous search match"},
			{"{ / }", "jump to previous / next question"},
			{"( / )", "jump to previous / next review verdict"},
			{"< / >", "jump to previous / next commit"},
			{"[ / ]", "jump to previous / next error"},
			{"y", "copy the selected row to the clipboard (commit → sha, error → message; input empty)"},
			{"enter (on ● commit)", "view the commit's diff (git show)"},
			{m.interruptKeyHint(), "interrupt the running agent to steer it"},
			{"shift+tab", "toggle work (loop) mid-session (work mode)"},
			{"q", "return to the menu when the session has finished (input empty; stops the session cleanly)"},
		}},
		{"question picker", []helpBind{
			{"↑ / ↓", "move between options"},
			{"1–9", "pick an option by number"},
			{"enter", "select the option (‹other…› drops to free text)"},
			{"pgup / pgdn", "scroll the transcript for context"},
		}},
		{"backlog browser", []helpBind{
			{"↑ / ↓", "move · enter opens task detail"},
			{"d", "toggle showing done tasks"},
			{"+ / -", "raise / lower priority"},
			{"s", "set status (then a digit)"},
			{"space", "multi-select a todo task"},
			{"P", "run selected tasks in parallel workstreams"},
			{"e", "edit the task in $EDITOR (detail view)"},
			{"esc / q", "close · back from detail"},
		}},
		{"session browser", []helpBind{
			{"↑ / ↓", "move between sessions"},
			{"enter", "view the transcript (read-only replay)"},
			{"o", "reopen / attach the selected session (from the menu only)"},
			{"r", "refresh the list"},
			{"/  n / N", "search a transcript · next / previous match"},
			{"{ } ( ) < > [ ]", "jump to question · review · commit · error"},
			{"enter (on a commit)", "view the commit's diff (git show)"},
			{"esc / q", "close · clear a search · back from a transcript"},
		}},
		{"commit diff", []helpBind{
			{"tab / shift+tab", "move between files"},
			{"enter / space", "fold / unfold the file under the cursor"},
			{"a", "fold / unfold all files"},
			{"↑ / ↓  pgup / pgdn", "scroll the diff"},
			{"esc / q", "close the diff"},
		}},
		{"workstreams", []helpBind{
			{"↑ / ↓", "move between workstreams"},
			{"enter", "attach to the workstream's session"},
			{"m", "preview & merge the workstream"},
			{"d", "discard the workstream (confirm with y)"},
			{"r", "refresh"},
			{"esc / q", "close"},
		}},
		{"plans", []helpBind{
			{"↑ / ↓", "move between plans"},
			{"enter", "view the plan markdown"},
			{"esc / q", "close · back from detail"},
		}},
		{"cost", []helpBind{
			{"↑ / ↓", "scroll the breakdown"},
			{"g", "cycle grouping (task · model · day)"},
			{"esc / q", "close"},
		}},
		{"digest", []helpBind{
			{"↑ / ↓", "move between rows"},
			{"enter", "open the task for the selected row"},
			{"esc / q", "close"},
		}},
		{"settings overlay", []helpBind{
			{"↑ / ↓", "move between settings"},
			{"← / →", "change the value under the cursor"},
			{"+ / -", "adjust thinking level"},
			{"space", "toggle a reviewer"},
			{"enter", "activate (e.g. open model backends)"},
			{"esc", "close the overlay"},
		}},
		{"model backends", []helpBind{
			{"↑ / ↓", "move between backends"},
			{"a", "add a backend"},
			{"e / enter", "edit the selected backend"},
			{"d", "duplicate the selected backend"},
			{"x", "delete the selected backend"},
			{"esc", "back to settings"},
		}},
		{"capture overlay", []helpBind{
			{"enter", "submit the description · dismiss when done"},
			{"esc", "close without saving"},
		}},
	}
}

// openHelp enters the keybinding help modal (task 0111), resetting the scroll.
func (m *model) openHelp() {
	m.helpOpen = true
	m.helpScroll = 0
}

// updateHelp handles the help modal: scroll the catalog, or close it. esc/q/?
// close; ctrl+c still quits.
func (m model) updateHelp(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q", "?", "ctrl+h", "ctrl+_":
		m.helpOpen = false
		m.helpScroll = 0
		return m, nil
	case "up", "k":
		m.helpScroll--
	case "down", "j":
		m.helpScroll++
	case "pgup":
		m.helpScroll -= 5
	case "pgdown":
		m.helpScroll += 5
	default:
		return m, nil
	}
	// Clamp so scrolling can't run past the last screenful (helpView also clamps
	// its render window, but keeping helpScroll bounded keeps up-scroll responsive).
	max := len(m.helpLines()) - m.helpBudget()
	if max < 0 {
		max = 0
	}
	if m.helpScroll > max {
		m.helpScroll = max
	}
	if m.helpScroll < 0 {
		m.helpScroll = 0
	}
	return m, nil
}

// helpLines flattens the catalog into rendered lines (section headers + padded
// binding rows), the content windowed by helpView and measured by updateHelp.
func (m model) helpLines() []string {
	sections := m.helpSections()
	keyW := 0
	for _, sec := range sections {
		for _, b := range sec.binds {
			if w := lipgloss.Width(b.keys); w > keyW {
				keyW = w
			}
		}
	}
	var lines []string
	for i, sec := range sections {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, cardTitleStyle.Render(sec.title))
		for _, b := range sec.binds {
			pad := strings.Repeat(" ", keyW-lipgloss.Width(b.keys))
			lines = append(lines, "  "+selStyle.Render(b.keys)+pad+"  "+dimStyle.Render(b.desc))
		}
	}
	return lines
}

// helpBudget is the number of content rows the modal card can show. modalCard's
// chrome is 6 non-content rows (title + blank + blank + footer + top/bottom
// border), so the budget is m.h-6. Before the first WindowSizeMsg (m.h == 0)
// there is no limit.
func (m model) helpBudget() int {
	if m.h <= 0 {
		return len(m.helpLines())
	}
	if b := m.h - 6; b > 0 {
		return b
	}
	return 1
}

// helpView renders the keybinding catalog as a scrollable modal card. Content
// lines beyond the height budget are windowed at helpScroll, mirroring
// browserCard's vertical accounting so the card never overruns the terminal.
func (m model) helpView() string {
	lines := m.helpLines()
	budget := m.helpBudget()
	start := m.helpScroll
	if start > len(lines)-budget {
		start = len(lines) - budget
	}
	if start < 0 {
		start = 0
	}
	end := start + budget
	if end > len(lines) {
		end = len(lines)
	}

	hint := " ↑/↓ scroll · esc close"
	if start > 0 || end < len(lines) {
		hint = fmt.Sprintf("%s · %d–%d/%d", hint, start+1, end, len(lines))
	}
	return m.modalCard(" help — keybindings ", strings.Join(lines[start:end], "\n"), hint)
}
