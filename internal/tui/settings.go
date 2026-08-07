// This file owns the settings overlay and role/model persistence commands.
package tui

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"

	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"

	"github.com/whyrusleeping/ycc/internal/clientconfig"
)

// setThinking issues SetThinking per role (spec §7.4, §18.2). With a live session
// it applies to that session and persists; with no session an empty session_id
// just persists the new default. An empty role updates all roles. Either way the
// level is written to ycc.toml so it survives a restart.
func (m model) setThinking(role, level string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.SetThinking(m.ctx, connect.NewRequest(&v1.SetThinkingRequest{
			SessionId: m.sessionID, Level: level, Role: role,
		})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// setRoleConfig issues SetRoleConfig (spec §18.2). With a live session it applies
// the change to that session and persists it; with no session (changed from the
// home menu) an empty session_id just persists the new default. Either way the
// selection is written to ycc.toml so it survives a restart.
func (m model) setRoleConfig(coord, impl string, reviewers []string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.SetRoleConfig(m.ctx, connect.NewRequest(&v1.SetRoleConfigRequest{
			SessionId: m.sessionID, Coordinator: coord, Implementer: impl, Reviewers: reviewers,
		})); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// openOverlay enters the modal settings overlay, seeding role defaults from the
// configured models when this is a fresh session.
func (m *model) openOverlay() {
	m.overlay = true
	m.ovCursor = 0
	if m.roleCoord == "" && len(m.models) > 0 {
		m.roleCoord = m.models[0].Name
		m.roleImpl = m.models[0].Name
		m.roleReviewrs = []string{m.models[0].Name}
	}
}

// overlay rows (indices into the navigable list).
const (
	ovCoord = iota
	ovImpl
	ovReviewers
	ovBackends
	ovTheme
	ovFollow
	ovAutoExpand
	ovNotifyBell
	ovNotifyDesktop
	ovInterrupt
	ovBackHome
	ovQuit
	ovCount
)

var (
	thinkLevels = []string{"off", "low", "medium", "high", "xhigh", "max"}
	themes      = []string{"dark", "light"}
)

func (m model) updateOverlay(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "esc":
		// Esc closes the overlay without leaving the session (spec §18.2).
		m.overlay = false
		return m, nil
	case "ctrl+c":
		return m.confirmQuit()
	case "up":
		if m.ovCursor > 0 {
			m.ovCursor--
		} else {
			m.ovCursor = ovCount - 1
		}
		return m, nil
	case "down":
		if m.ovCursor < ovCount-1 {
			m.ovCursor++
		} else {
			m.ovCursor = 0
		}
		return m, nil
	case "left":
		return m.overlayAdjust(-1)
	case "right":
		return m.overlayAdjust(1)
	case "+", "=":
		return m.overlayAdjustThinking(1)
	case "-", "_":
		return m.overlayAdjustThinking(-1)
	case " ", "space":
		if m.ovCursor == ovReviewers {
			return m.toggleReviewerAndPersist()
		}
		return m, nil
	case "enter":
		return m.overlayActivate()
	}
	return m, nil
}

// overlayAdjust cycles the value under the cursor (left/right).
func (m model) overlayAdjust(d int) (tea.Model, tea.Cmd) {
	switch m.ovCursor {
	case ovCoord:
		m.roleCoord = cycleModel(m.models, m.roleCoord, d)
		// Apply immediately so the choice sticks without a separate "apply" step —
		// the daemon persists it to ycc.toml.
		return m, m.setRoleConfig(m.roleCoord, "", nil)
	case ovImpl:
		m.roleImpl = cycleModel(m.models, m.roleImpl, d)
		return m, m.setRoleConfig("", m.roleImpl, nil)
	case ovReviewers:
		// Move the visible sub-cursor across the reviewer chips (no toggle, no
		// persist) so the user can see which model the next space/enter affects.
		if n := len(m.models); n > 0 {
			m.reviewerSub = (m.reviewerSub + d + n) % n
		}
		return m, nil
	case ovTheme:
		m.prefs.Theme = cycle(themes, m.prefs.Theme, d)
		clientconfig.Save(m.prefs)
		// Live-switch the palette so the open menu/session repaints in the new
		// theme without a restart.
		applyTheme(themeByName(m.prefs.Theme))
		m.restyleInputs()
		m.makeRenderer()
		m.invalidateRender()
		m.rebuild()
		return m, nil
	case ovFollow:
		m.prefs.Follow = !m.prefs.Follow
		m.follow = m.prefs.Follow
		clientconfig.Save(m.prefs)
		return m, nil
	case ovAutoExpand:
		m.toggleAutoExpand()
		return m, nil
	case ovNotifyBell:
		m.prefs.NotifyBell = !m.prefs.NotifyBell
		clientconfig.Save(m.prefs)
		return m, nil
	case ovNotifyDesktop:
		m.prefs.NotifyDesktop = !m.prefs.NotifyDesktop
		clientconfig.Save(m.prefs)
		return m, nil
	}
	return m, nil
}

// overlayAdjustThinking cycles the per-role thinking level under the cursor
// (+/-). The thinking level lives inline on each role's row (e.g. "claude opus
// (xhigh)") rather than as a separate menu entry.
func (m model) overlayAdjustThinking(d int) (tea.Model, tea.Cmd) {
	var role string
	switch m.ovCursor {
	case ovCoord:
		role = "coordinator"
	case ovImpl:
		role = "implementer"
	case ovReviewers:
		role = "reviewers"
	default:
		return m, nil
	}
	m.thinkLevels[role] = cycle(thinkLevels, m.thinkLevels[role], d)
	return m, m.setThinking(role, m.thinkLevels[role])
}

// overlayActivate runs the action under the cursor (enter).
func (m model) overlayActivate() (tea.Model, tea.Cmd) {
	switch m.ovCursor {
	case ovReviewers:
		return m.toggleReviewerAndPersist()
	case ovBackends:
		// Open the model-backends management modal (task 0044) and refresh the
		// model list so it lists the current backends.
		m.overlay = false
		m.mbOpen = true
		m.mbView = 0
		m.mbCursor = 0
		m.mbErr = ""
		return m, m.fetchModels
	case ovInterrupt:
		// Interrupt the running agent (or resume a paused one) — the overlay
		// route promised by spec §18.7, and the reliable path on terminals where
		// ctrl+i can't be distinguished from tab (ctrl+x is the universal direct
		// chord for the same action). Close the overlay so the user
		// sees the paused/running state and can steer immediately.
		if m.sessionID == "" || m.state != stateSession {
			return m, nil
		}
		if m.paused {
			m.overlay = false
			return m, m.resume()
		}
		if m.status == "running" {
			m.overlay = false
			return m, m.interrupt()
		}
		return m, nil
	case ovBackHome:
		// Explicit, intentional exit from the session (spec §18.2).
		m.overlay = false
		m.state = stateMenu
		return m, m.refreshMenu()
	case ovQuit:
		return m.confirmQuit()
	case ovAutoExpand:
		m.toggleAutoExpand()
		return m, nil
	case ovNotifyBell:
		m.prefs.NotifyBell = !m.prefs.NotifyBell
		clientconfig.Save(m.prefs)
		return m, nil
	case ovNotifyDesktop:
		m.prefs.NotifyDesktop = !m.prefs.NotifyDesktop
		clientconfig.Save(m.prefs)
		return m, nil
	}
	return m, nil
}

// toggleAutoExpand flips the auto-expand-agent-logs preference, persists it, and
// rebuilds the event stream so the new default takes effect immediately.
func (m *model) toggleAutoExpand() {
	m.prefs.AutoExpandLogs = !m.prefs.AutoExpandLogs
	clientconfig.Save(m.prefs)
	m.invalidateRender()
	m.rebuild()
}

// eventExpanded reports whether the event with the given seq/type should render
// expanded. The final session report is a human-facing result, not an agent log,
// so it is unconditionally expanded. Other rows honor a manual per-row override
// first, then the auto-expand preference and per-type default.
func (m *model) eventExpanded(seq int, typ string) bool {
	if typ == "session_idle" {
		return true
	}
	if v, ok := m.expanded[seq]; ok {
		return v
	}
	return m.prefs.AutoExpandLogs || autoExpand(typ)
}

// toggleReviewer flips membership of the model under the visible sub-cursor
// (m.reviewerSub). The sub-cursor stays put so the next toggle's target remains
// exactly what the user sees highlighted; it is moved explicitly with ←/→.
func (m *model) toggleReviewer() {
	if len(m.models) == 0 {
		return
	}
	if m.reviewerSub >= len(m.models) {
		m.reviewerSub = 0
	}
	name := m.models[m.reviewerSub].Name
	if m.contains(name) {
		m.roleReviewrs = remove(m.roleReviewrs, name)
	} else {
		m.roleReviewrs = append(m.roleReviewrs, name)
	}
}

// toggleReviewerAndPersist toggles the highlighted reviewer, guards the
// non-empty invariant (a session never points at zero reviewers), and persists
// the new set immediately via SetRoleConfig. Shared by the space and enter key
// paths on the reviewers row.
func (m model) toggleReviewerAndPersist() (tea.Model, tea.Cmd) {
	m.toggleReviewer()
	revs := m.roleReviewrs
	if len(revs) == 0 && len(m.models) > 0 {
		revs = []string{m.models[0].Name}
		m.roleReviewrs = revs
	}
	if len(revs) > 0 {
		return m, m.setRoleConfig("", "", revs)
	}
	return m, nil
}

func (m model) contains(name string) bool {
	for _, r := range m.roleReviewrs {
		if r == name {
			return true
		}
	}
	return false
}

func remove(s []string, name string) []string {
	out := s[:0]
	for _, v := range s {
		if v != name {
			out = append(out, v)
		}
	}
	return append([]string(nil), out...)
}

func cycle(vals []string, cur string, d int) string {
	idx := 0
	for i, v := range vals {
		if v == cur {
			idx = i
			break
		}
	}
	idx = (idx + d + len(vals)) % len(vals)
	return vals[idx]
}

func cycleModel(models []*v1.ModelInfo, cur string, d int) string {
	if len(models) == 0 {
		return cur
	}
	idx := 0
	for i, mm := range models {
		if mm.Name == cur {
			idx = i
			break
		}
	}
	idx = (idx + d + len(models)) % len(models)
	return models[idx].Name
}

func (m model) overlayView() string {
	var b strings.Builder
	// The interrupt row doubles as resume while paused (spec §18.7). It also
	// serves as the fallback route to interrupt on terminals where ctrl+i is
	// indistinguishable from tab (no kitty keyboard protocol).
	interruptLabel, interruptVal := "interrupt agent", "pause at next safe checkpoint"
	switch {
	case m.sessionID == "" || m.state != stateSession:
		interruptVal = "(no active session)"
	case m.paused:
		interruptLabel, interruptVal = "resume agent", "continue from the pause"
	case m.status != "running":
		interruptVal = "(agent is " + m.status + ")"
	}
	rows := []struct{ label, val string }{
		{"coordinator model", m.roleCoord + " (" + m.thinkLevels["coordinator"] + ")"},
		{"implementer model", m.roleImpl + " (" + m.thinkLevels["implementer"] + ")"},
		{"reviewers", strings.Join(m.roleReviewrs, ", ")},
		{"model backends", "add / edit / remove…"},
		{"theme", m.prefs.Theme},
		{"follow / auto-scroll", boolStr(m.prefs.Follow)},
		{"auto-expand agent logs", boolStr(m.prefs.AutoExpandLogs)},
		{"notify: terminal bell", boolStr(m.prefs.NotifyBell)},
		{"notify: desktop (OSC 9)", boolStr(m.prefs.NotifyDesktop)},
		{interruptLabel, interruptVal},
		{"back to home menu", ""},
		{"quit", ""},
	}
	// Window the rows around the cursor so the card fits short terminals
	// (mirrors browserCard). Fixed chrome: modalCard's 6 non-content rows plus
	// the no-session note (2 lines) when shown.
	budget := len(rows)
	if m.h > 0 {
		chrome := 6
		if m.sessionID == "" {
			chrome += 2
		}
		budget = m.h - chrome
		if budget < 1 {
			budget = 1
		}
	}
	start, end := listWindow(m.ovCursor, len(rows), budget)
	for i, r := range rows[start:end] {
		cursor := "  "
		label := fmt.Sprintf("%-22s", r.label)
		if start+i == m.ovCursor {
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render(label)
		}
		val := r.val
		if start+i == ovReviewers && len(m.models) > 0 {
			// Highlight the chip the next toggle affects only while the cursor is
			// on this row, so the target is always visible before pressing space.
			val = "(" + m.thinkLevels["reviewers"] + ")  " + m.reviewerSummary(start+i == m.ovCursor)
		}
		b.WriteString(cursor + label + dimStyle.Render(val) + "\n")
	}
	if m.sessionID == "" {
		b.WriteString("\n" + dimStyle.Render("(no active session: role changes are saved as defaults; thinking overrides apply only within a session)"))
	}
	help := "↑/↓ move · ←/→ change · +/- thinking · enter activate · esc close"
	if m.ovCursor == ovReviewers {
		help = "←/→ highlight model · space/enter toggle · +/- thinking · esc close"
	}
	return m.modalCard(" settings ", strings.TrimRight(b.String(), "\n"), help)
}

func (m model) reviewerSummary(highlight bool) string {
	var parts []string
	for i, mm := range m.models {
		mark := "[ ]"
		if m.contains(mm.Name) {
			mark = "[x]"
		}
		chip := mark + " " + mm.Name
		if highlight && i == m.reviewerSub {
			chip = selStyle.Render(chip)
		}
		parts = append(parts, chip)
	}
	return strings.Join(parts, "  ")
}

func boolStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
