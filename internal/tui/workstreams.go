// This file owns workstream commands, panels, and merge previews.
package tui

import (
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// fetchWorkstreams loads the workstreams for the current project for the panel
// (task 0085, design §8).
func (m model) fetchWorkstreams() tea.Msg {
	resp, err := m.client.ListWorkstreams(m.ctx, connect.NewRequest(&v1.ListWorkstreamsRequest{Project: m.project}))
	if err != nil {
		return workstreamsMsg{err: err}
	}
	return workstreamsMsg{list: resp.Msg.Workstreams}
}

// wsRefreshTick arms the next workstreams-panel refresh, tagged with the current
// wsTick so a stale tick (from a previous panel visit) is dropped rather than
// compounding timers (mirrors menuRefreshTick, task 0085).
func (m model) wsRefreshTick() tea.Cmd {
	seq := m.wsTick
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return wsTickMsg{seq} })
}

// spawnWorkstreamsCmd fires one SpawnWorkstream per selected backlog task (task
// 0085, design §8), stopping at the first error. Each session is seeded with a
// prompt naming the task.
func (m model) spawnWorkstreamsCmd(tasks []*v1.BacklogTaskSummary) tea.Cmd {
	project := m.project
	ctx := m.ctx
	client := m.client
	return func() tea.Msg {
		n := 0
		for _, t := range tasks {
			prompt := fmt.Sprintf("Work on backlog task %s: %s", t.Id, t.Title)
			if _, err := client.SpawnWorkstream(ctx, connect.NewRequest(&v1.SpawnWorkstreamRequest{
				Project: project, TaskId: t.Id, Prompt: prompt,
			})); err != nil {
				return wsSpawnedMsg{count: n, err: err}
			}
			n++
		}
		return wsSpawnedMsg{count: n}
	}
}

// previewMergeCmd trial-merges a workstream for the merge overlay (task 0085,
// design §6 step 1): clean + integrated diff, or the conflicted paths.
func (m model) previewMergeCmd(id string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.PreviewMerge(m.ctx, connect.NewRequest(&v1.PreviewMergeRequest{WorkstreamId: id}))
		if err != nil {
			return wsPreviewMsg{id: id, err: err}
		}
		return wsPreviewMsg{id: id, preview: resp.Msg}
	}
}

// mergeWorkstreamCmd integrates a workstream's branch back to base with accept=true
// (task 0085, design §6). A conflict returns the conflicted paths; base untouched.
func (m model) mergeWorkstreamCmd(id string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.MergeWorkstream(m.ctx, connect.NewRequest(&v1.MergeWorkstreamRequest{WorkstreamId: id, Accept: true}))
		if err != nil {
			return wsMergedMsg{id: id, err: err}
		}
		return wsMergedMsg{id: id, res: resp.Msg}
	}
}

// discardWorkstreamCmd abandons a workstream without merging (task 0085, design §6).
func (m model) discardWorkstreamCmd(id string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.client.DiscardWorkstream(m.ctx, connect.NewRequest(&v1.DiscardWorkstreamRequest{WorkstreamId: id})); err != nil {
			return wsDiscardedMsg{id: id, err: err}
		}
		return wsDiscardedMsg{id: id}
	}
}

// openWorkstreams enters the modal Workstreams panel, resetting its transient
// state and bumping the refresh-tick generation so an older tick can't multiply
// the in-flight timers.
func (m *model) openWorkstreams() {
	m.ws = true
	m.wsCursor = 0
	m.wsMerge, m.wsMergeID = nil, ""
	m.wsDiscardID = ""
	m.wsTick++
	if m.wsList == nil {
		m.wsNotice = "loading…"
	}
}

// wsRowStatus resolves the status cell for a workstream row. Registry completion
// and terminal states win over the session status; needs-attention and conflicts
// use the loud error style returned by the second value.
func (m model) wsRowStatus(w *v1.WorkstreamInfo) (string, bool) {
	switch w.GetStatus() {
	case "merged", "discarded", "stale":
		return w.GetStatus(), false
	case "needs_attention":
		return "⚠ needs attention", true
	}
	switch m.wsLocal[w.GetId()] {
	case "conflict":
		return "conflict", true
	case "awaiting-review":
		return "awaiting-review", false
	}
	if w.GetStatus() == "ready" {
		return "ready", false
	}
	if s := w.GetSessionStatus(); s != "" {
		return s, false
	}
	return w.GetStatus(), false
}

// updateWorkstreams handles the modal Workstreams panel: list navigation, drill
// into the session (enter), merge overlay (m), discard confirm (d), refresh (r).
// The merge overlay and the discard confirm own input while active.
func (m model) updateWorkstreams(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	// Merge/accept overlay owns input while open (task 0085, design §6).
	if m.wsMerge != nil {
		switch key.String() {
		case "ctrl+c":
			return m.confirmQuit()
		case "esc", "q", "backspace", "left":
			m.wsMerge, m.wsMergeID = nil, ""
			return m, nil
		case "enter", "y":
			if m.wsMerge.GetClean() {
				id := m.wsMergeID
				m.wsNotice = "merging " + short(id) + "…"
				return m, m.mergeWorkstreamCmd(id)
			}
			// A conflicted preview cannot be accepted; keep it surfaced.
			m.wsNotice = "cannot merge: conflicts must be resolved first"
			return m, nil
		}
		var cmd tea.Cmd
		m.wsMergeVP, cmd = m.wsMergeVP.Update(msg)
		return m, cmd
	}

	// Two-step discard confirm (footer prompt).
	if m.wsDiscardID != "" {
		id := m.wsDiscardID
		m.wsDiscardID = ""
		switch key.String() {
		case "y":
			m.wsNotice = "discarding " + short(id) + "…"
			return m, m.discardWorkstreamCmd(id)
		default:
			m.wsNotice = "discard cancelled"
			return m, nil
		}
	}

	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.ws = false
		m.wsTick++ // invalidate any in-flight refresh tick
		m.wsNotice = ""
		return m, nil
	case "up":
		m.wsCursor = navUp(m.wsCursor)
		return m, nil
	case "down":
		m.wsCursor = navDown(m.wsCursor, len(m.wsList))
		return m, nil
	case "r":
		m.wsNotice = "refreshing…"
		return m, m.fetchWorkstreams
	case "enter":
		// Drill into the workstream's session (design §8): ResumeSession is
		// idempotent for a live session, so this attaches rather than restarts.
		if w := m.wsCurrent(); w != nil && w.GetSessionId() != "" {
			m.status = "reopening " + short(w.GetSessionId()) + "…"
			return m, m.reopenSession(w.GetSessionId())
		}
		return m, nil
	case "m":
		if w := m.wsCurrent(); w != nil {
			status := w.GetStatus()
			if status != "active" && status != "ready" && status != "needs_attention" {
				m.wsNotice = "cannot merge: workstream is " + status
				return m, nil
			}
			m.wsNotice = "previewing merge…"
			return m, m.previewMergeCmd(w.GetId())
		}
		return m, nil
	case "d":
		if w := m.wsCurrent(); w != nil {
			m.wsDiscardID = w.GetId()
			return m, nil
		}
		return m, nil
	}
	return m, nil
}

// wsCurrent returns the workstream under the cursor, or nil when the list is empty.
func (m model) wsCurrent() *v1.WorkstreamInfo {
	if m.wsCursor >= 0 && m.wsCursor < len(m.wsList) {
		return m.wsList[m.wsCursor]
	}
	return nil
}

// shortBranch trims the ycc/ws/ namespace prefix for a compact row cell.
func shortBranch(b string) string {
	return strings.TrimPrefix(b, "ycc/ws/")
}

// workstreamsView renders the Workstreams panel: the merge overlay when open,
// otherwise the list of workstreams with a live status cell (conflicts loud).
func (m model) workstreamsView() string {
	if m.wsMerge != nil {
		return m.wsMergeView()
	}
	b := browser{
		title:  " ycc — workstreams ",
		cursor: m.wsCursor,
		hint:   "↑/↓ select · enter open session · m merge · d discard · r refresh · esc close",
		empty:  "(no workstreams — spawn from the backlog with space + P)",
	}
	if m.wsDiscardID != "" {
		b.hint = "discard " + short(m.wsDiscardID) + "? y confirm · any other key cancel"
	} else if m.wsNotice != "" {
		b.hint = m.wsNotice
	}
	for _, w := range m.wsList {
		status, conflict := m.wsRowStatus(w)
		statusCell := fmt.Sprintf("%-15s", status)
		if conflict {
			statusCell = errStyle.Render(fmt.Sprintf("%-15s", "⚠ conflict"))
		}
		task := w.GetTaskId()
		if task == "" {
			task = "—"
		}
		row := fmt.Sprintf("%-10s %-6s %-18s %2d↑ ", short(w.GetId()), task, shortBranch(w.GetBranch()), w.GetCommitCount())
		b.rows = append(b.rows, browserRow{text: row, suffix: statusCell})
	}
	return m.browserCard(b)
}

// refreshWsMergeVP (re)sizes the merge overlay viewport and loads its content:
// a clean integrated diff, or the conflicted paths rendered distinctly.
func (m *model) refreshWsMergeVP() {
	if m.wsMerge == nil || !m.ready {
		return
	}
	h := m.h - 2
	if h < 3 {
		h = 3
	}
	if m.wsMergeVP.Height() == 0 && m.wsMergeVP.Width() == 0 {
		m.wsMergeVP = viewport.New(viewport.WithWidth(m.w), viewport.WithHeight(h))
	} else {
		m.wsMergeVP.SetWidth(m.w)
		m.wsMergeVP.SetHeight(h)
	}
	m.wsMergeVP.SetContent(m.wsMergeContent())
}

// wsMergeContent builds the scrollable body of the merge overlay (task 0085).
func (m model) wsMergeContent() string {
	var b strings.Builder
	if m.wsMerge.GetClean() {
		b.WriteString(successStyle.Render("✓ clean — no conflicts") + "\n\n")
		diff := m.wsMerge.GetDiff()
		if strings.TrimSpace(diff) == "" {
			b.WriteString(dimStyle.Render("(no changes to integrate)"))
		} else {
			b.WriteString(colorizeDiff(diff))
		}
	} else {
		b.WriteString(errStyle.Render("⚠ conflict — merge blocked until resolved") + "\n\n")
		b.WriteString(dimStyle.Render("conflicted paths:") + "\n")
		for _, p := range m.wsMerge.GetConflicts() {
			b.WriteString("  " + errStyle.Render(p) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// wsMergeView renders the merge/accept overlay as a full-screen scrollable view.
func (m model) wsMergeView() string {
	top := m.titleBar(" merge " + short(m.wsMergeID) + " ")
	body := ""
	if m.ready {
		body = m.wsMergeVP.View()
	}
	var hint string
	if m.wsMerge.GetClean() {
		hint = " enter/y merge · ↑↓ scroll · esc/← cancel · ctrl+c quit "
	} else {
		hint = " conflict — resolve in the worktree first · esc/← back · ctrl+c quit "
	}
	if m.wsNotice != "" {
		hint = " " + m.wsNotice + " "
	}
	return top + "\n" + body + "\n" + m.footerBar(hint)
}
