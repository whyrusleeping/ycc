// This file owns plan browsing and plan details.
package tui

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// fetchPlans loads the saved plan library list for the plans browser (task 0077).
func (m model) fetchPlans() tea.Msg {
	resp, err := m.client.ListPlans(m.ctx, connect.NewRequest(&v1.ListPlansRequest{Project: m.project}))
	if err != nil {
		return errMsg{err}
	}
	return plansMsg{resp.Msg.Plans}
}

// fetchPlan loads one saved plan's markdown for the plans browser (task 0077).
func (m model) fetchPlan(name string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.GetPlan(m.ctx, connect.NewRequest(&v1.GetPlanRequest{Project: m.project, Name: name}))
		if err != nil {
			return errMsg{err}
		}
		return planDetailMsg{resp.Msg}
	}
}

// updatePlans handles the modal plan library browser: a list of saved plans with
// drill-down into one plan's read-only markdown (task 0077).
func (m model) updatePlans(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.planDetail != nil {
		// Detail view: back returns to the list; everything else scrolls the
		// markdown viewport.
		switch key.String() {
		case "ctrl+c", "q":
			return m.confirmQuit()
		case "esc", "backspace", "left":
			m.planDetail = nil
			return m, nil
		}
		var cmd tea.Cmd
		m.plansVP, cmd = m.plansVP.Update(msg)
		return m, cmd
	}
	// List view.
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.plans = false
		return m, nil
	case "up":
		m.plansCursor = navUp(m.plansCursor)
		return m, nil
	case "down":
		m.plansCursor = navDown(m.plansCursor, len(m.plansList))
		return m, nil
	case "enter":
		if len(m.plansList) > 0 {
			return m, m.fetchPlan(m.plansList[m.plansCursor].Name)
		}
		return m, nil
	}
	return m, nil
}

// plansView renders the modal plan library browser (list or detail) as a card.
func (m model) plansView() string {
	if m.planDetail != nil {
		return m.planDetailView(m.planDetail)
	}
	b := browser{
		title:  " ycc — plans ",
		cursor: m.plansCursor,
		hint:   "↑/↓ select · enter view · esc close",
		empty:  "(no saved plans)",
	}
	for _, p := range m.plansList {
		b.rows = append(b.rows, browserRow{
			text:   fmt.Sprintf("%-20s", p.Name),
			suffix: dimStyle.Render(p.Title),
		})
	}
	return m.browserCard(b)
}

// planDetailView renders a single saved plan's markdown content (task 0077) as a
// full-screen scrollable viewport (mirroring the backlog task detail drill-in).
func (m model) planDetailView(p *v1.GetPlanResponse) string {
	top := m.titleBar(" " + p.Name + " — " + p.Title + " ")
	body := ""
	if m.ready {
		body = m.plansVP.View()
	}
	help := m.footerBar(" ↑↓/pgup/pgdn scroll · esc/← back · ctrl+c quit · (ask a session to run it) ")
	return top + "\n" + body + "\n" + help
}

// planDetailContent builds the glamour-rendered markdown body placed into the
// plan detail viewport (m.plansVP).
func (m model) planDetailContent(p *v1.GetPlanResponse) string {
	body := p.Content
	if m.glam != nil {
		if out, err := m.glam.Render(body); err == nil {
			body = strings.Trim(out, "\n")
		}
	}
	return body
}

// refreshPlanDetailVP (re)sizes the plan detail viewport to the current terminal
// dimensions and loads the open plan's content. It is a no-op when no plan is
// open or the terminal size is not yet known.
func (m *model) refreshPlanDetailVP() {
	if m.planDetail == nil || !m.ready {
		return
	}
	h := m.h - 2 // one row for the title bar, one for the footer
	if h < 3 {
		h = 3
	}
	if m.plansVP.Height() == 0 && m.plansVP.Width() == 0 {
		m.plansVP = viewport.New(viewport.WithWidth(m.w), viewport.WithHeight(h))
	} else {
		m.plansVP.SetWidth(m.w)
		m.plansVP.SetHeight(h)
	}
	m.plansVP.SetContent(m.planDetailContent(m.planDetail))
}
