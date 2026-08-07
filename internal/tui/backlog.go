// This file owns backlog browsing, task details, prioritization, and editor integration.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"connectrpc.com/connect"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// fetchBacklog loads the backlog summary rows for the backlog browser (spec §18.5).
func (m model) fetchBacklog() tea.Msg {
	resp, err := m.client.ListBacklog(m.ctx, connect.NewRequest(&v1.ListBacklogRequest{Project: m.project}))
	if err != nil {
		return errMsg{err}
	}
	return backlogMsg{resp.Msg.Tasks}
}

// fetchTask loads one task's full detail for the backlog browser (spec §18.5).
func (m model) fetchTask(id string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.GetTask(m.ctx, connect.NewRequest(&v1.GetTaskRequest{Project: m.project, Id: id}))
		if err != nil {
			return errMsg{err}
		}
		return taskDetailMsg{resp.Msg.Task}
	}
}

// updateTaskCmd grooms a backlog task via the daemon UpdateTask RPC (spec §18.5,
// task 0099). status/priority are nil-passthrough (leave field untouched); a call
// with both nil is a "refresh" that re-reads the file.
func (m model) updateTaskCmd(id string, status *string, priority *int32) tea.Cmd {
	return func() tea.Msg {
		req := &v1.UpdateTaskRequest{Project: m.project, Id: id, Status: status, Priority: priority}
		resp, err := m.client.UpdateTask(m.ctx, connect.NewRequest(req))
		if err != nil {
			return taskUpdatedMsg{err: err}
		}
		return taskUpdatedMsg{task: resp.Msg.Task}
	}
}

// editorCommand resolves the user's preferred editor: $EDITOR, then $VISUAL, then
// "vi" (task 0099). Kept small and side-effect-free so it is unit-testable.
func editorCommand() string {
	if e := strings.TrimSpace(os.Getenv("EDITOR")); e != "" {
		return e
	}
	if e := strings.TrimSpace(os.Getenv("VISUAL")); e != "" {
		return e
	}
	return "vi"
}

// openEditorCmd suspends the Bubble Tea program and opens path in the user's
// $EDITOR, returning an editorClosedMsg when it exits (task 0099).
func (m model) openEditorCmd(id, path string) tea.Cmd {
	fields := strings.Fields(editorCommand())
	name := fields[0]
	args := append(append([]string{}, fields[1:]...), path)
	return tea.ExecProcess(exec.Command(name, args...), func(err error) tea.Msg {
		return editorClosedMsg{id: id, err: err}
	})
}

func (m model) updateBacklog(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	// Status-choice mode (spec §18.5 grooming, task 0099): the next digit picks a
	// status; esc/any other key cancels. Applies to the cursor row (list) or the
	// open detail task.
	if m.backlogStatusPrompt {
		m.backlogStatusPrompt = false
		if st, ok := statusForDigit(key.String()); ok {
			if id := m.backlogTargetID(); id != "" {
				return m, m.updateTaskCmd(id, &st, nil)
			}
		}
		return m, nil
	}
	if m.backlogDetail != nil {
		// Detail view: grooming keys, editor escape hatch, then scroll.
		switch key.String() {
		case "ctrl+c", "q":
			return m.confirmQuit()
		case "esc", "backspace", "left":
			m.backlogDetail = nil
			m.backlogNotice = ""
			return m, nil
		case "+", "=":
			return m, m.reprioritizeCmd(m.backlogDetail.Id, int(m.backlogDetail.Priority), -1)
		case "-", "_":
			return m, m.reprioritizeCmd(m.backlogDetail.Id, int(m.backlogDetail.Priority), +1)
		case "s":
			m.backlogStatusPrompt = true
			return m, nil
		case "e":
			return m.openTaskInEditor(m.backlogDetail.Id, m.backlogDetail.Path)
		}
		var cmd tea.Cmd
		m.backlogVP, cmd = m.backlogVP.Update(msg)
		return m, cmd
	}
	// List view.
	vis := m.visibleBacklogTasks()
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "q":
		m.backlog = false
		m.backlogBlockedOnly = false
		m.backlogNotice = ""
		m.backlogSelected = nil
		return m, nil
	case "up":
		m.backlogCursor = navUp(m.backlogCursor)
		return m, nil
	case "down":
		m.backlogCursor = navDown(m.backlogCursor, len(vis))
		return m, nil
	case "d":
		m.backlogShowDone = !m.backlogShowDone
		m.backlogCursor = clampCursor(m.backlogCursor, len(m.visibleBacklogTasks()))
		return m, nil
	case "+", "=":
		if len(vis) > 0 {
			t := vis[m.backlogCursor]
			return m, m.reprioritizeCmd(t.Id, int(t.Priority), -1)
		}
		return m, nil
	case "-", "_":
		if len(vis) > 0 {
			t := vis[m.backlogCursor]
			return m, m.reprioritizeCmd(t.Id, int(t.Priority), +1)
		}
		return m, nil
	case "s":
		if len(vis) > 0 {
			m.backlogStatusPrompt = true
		}
		return m, nil
	case "enter":
		if len(vis) > 0 {
			return m, m.fetchTask(vis[m.backlogCursor].Id)
		}
		return m, nil
	case "space", " ":
		// Toggle multi-select for a spawnable (todo) task (task 0085). Selection is
		// a set of task ids, cleared when the browser closes.
		if len(vis) > 0 {
			t := vis[m.backlogCursor]
			if t.Status == "todo" {
				if m.backlogSelected == nil {
					m.backlogSelected = map[string]bool{}
				}
				if m.backlogSelected[t.Id] {
					delete(m.backlogSelected, t.Id)
				} else {
					m.backlogSelected[t.Id] = true
				}
			}
		}
		return m, nil
	case "P":
		// Run the selected tasks in parallel workstreams (task 0085, design §8).
		// Workstreams need a registered project (daemon-registry mode); a one-shot
		// (empty project) can't spawn them.
		sel := m.selectedBacklogTasks()
		if len(sel) == 0 {
			m.backlogNotice = "select tasks with space, then P to run in parallel"
			return m, nil
		}
		if strings.TrimSpace(m.project) == "" {
			m.backlogNotice = "workstreams need a registered project (open ycc on a project)"
			return m, nil
		}
		m.backlogNotice = fmt.Sprintf("spawning %d workstream(s)…", len(sel))
		return m, m.spawnWorkstreamsCmd(sel)
	}
	return m, nil
}

// selectedBacklogTasks returns the currently multi-selected backlog tasks (task
// 0085), restricted to those still visible and todo.
func (m model) selectedBacklogTasks() []*v1.BacklogTaskSummary {
	if len(m.backlogSelected) == 0 {
		return nil
	}
	var out []*v1.BacklogTaskSummary
	for _, t := range m.backlogTasks {
		if m.backlogSelected[t.Id] {
			out = append(out, t)
		}
	}
	return out
}

// backlogTargetID returns the task the grooming keys act on: the open detail task,
// else the cursor row in the list (task 0099).
func (m model) backlogTargetID() string {
	if m.backlogDetail != nil {
		return m.backlogDetail.Id
	}
	vis := m.visibleBacklogTasks()
	if len(vis) > 0 && m.backlogCursor < len(vis) {
		return vis[m.backlogCursor].Id
	}
	return ""
}

// statusForDigit maps a status-prompt digit to a docs status (task 0099).
func statusForDigit(k string) (string, bool) {
	switch k {
	case "1":
		return "todo", true
	case "2":
		return "in_progress", true
	case "3":
		return "in_review", true
	case "4":
		return "done", true
	case "5":
		return "blocked", true
	case "6":
		return "proposed", true
	}
	return "", false
}

// reprioritizeCmd nudges a task's priority toward p1 (dir<0) or p5 (dir>0), clamped
// to 1..5; it is a no-op at the clamp edge to avoid a needless RPC (task 0099).
func (m model) reprioritizeCmd(id string, cur, dir int) tea.Cmd {
	next := cur + dir
	if next < 1 {
		next = 1
	}
	if next > 5 {
		next = 5
	}
	if next == cur {
		return nil
	}
	p := int32(next)
	return m.updateTaskCmd(id, nil, &p)
}

// openTaskInEditor opens the task's markdown file in $EDITOR when the workspace is
// local to the client (the task file exists on this filesystem — the common
// in-process/loopback case). Remote clients can't reach the workspace editor, so
// the affordance degrades to a footer notice (task 0099).
func (m model) openTaskInEditor(id, path string) (tea.Model, tea.Cmd) {
	if !taskFileLocal(path) {
		m.backlogNotice = "open-in-editor unavailable: workspace not local"
		return m, nil
	}
	m.backlogNotice = ""
	return m, m.openEditorCmd(id, path)
}

// taskFileLocal reports whether the task's file is reachable on the client's
// filesystem (gates the open-in-editor affordance, task 0099).
func taskFileLocal(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// visibleBacklogTasks returns the backlog rows to display: all tasks when
// backlogShowDone is set, otherwise only non-done (actionable) tasks. This keeps
// the overlay focused on open work by default while letting done tasks be revealed.
func (m model) visibleBacklogTasks() []*v1.BacklogTaskSummary {
	if m.backlogBlockedOnly {
		out := make([]*v1.BacklogTaskSummary, 0, len(m.backlogTasks))
		for _, t := range m.backlogTasks {
			if t.Status == "blocked" {
				out = append(out, t)
			}
		}
		return out
	}
	if m.backlogShowDone {
		return m.backlogTasks
	}
	out := make([]*v1.BacklogTaskSummary, 0, len(m.backlogTasks))
	for _, t := range m.backlogTasks {
		if t.Status != "done" {
			out = append(out, t)
		}
	}
	return out
}

// backlogView renders the modal backlog browser (list or detail) as a bordered card.
func (m model) backlogView() string {
	if m.backlogDetail != nil {
		return m.taskDetailView(m.backlogDetail)
	}
	b := browser{
		title:  " ycc — backlog ",
		cursor: m.backlogCursor,
		hint:   "↑/↓ select · enter inspect · +/- priority · s status · d show/hide done · esc close",
		empty:  "(no backlog tasks)",
	}
	if m.backlogBlockedOnly {
		b.title = " ycc — blocked tasks "
		b.hint = "↑/↓ select · enter inspect (see why) · +/- priority · s status · esc close"
		b.empty = "(no blocked tasks)"
	}
	if m.backlogStatusPrompt {
		b.hint = "set status: 1 todo · 2 in_progress · 3 in_review · 4 done · 5 blocked · 6 proposed · esc cancel"
	} else if m.backlogNotice != "" {
		b.hint = m.backlogNotice
	} else if n := len(m.backlogSelected); n > 0 {
		b.hint = fmt.Sprintf("%d selected · space toggle · P run in parallel (%d workstreams) · esc close", n, n)
	} else {
		b.hint += " · space select · P run in parallel"
	}
	for _, t := range m.visibleBacklogTasks() {
		// Multi-select checkbox for spawnable (todo) tasks (task 0085).
		mark := "   "
		if t.Status == "todo" {
			if m.backlogSelected[t.Id] {
				mark = "[x]"
			} else {
				mark = "[ ]"
			}
		}
		row := fmt.Sprintf("%s %-5s %-12s p%d  %s", mark, t.Id, t.Status, t.Priority, t.Title)
		var tag string
		if t.Status != "done" {
			if t.Ready {
				tag = " " + dimStyle.Render("[ready]")
			} else {
				tag = " " + dimStyle.Render("[blocked by "+strings.Join(t.BlockedBy, ", ")+"]")
			}
		}
		b.rows = append(b.rows, browserRow{text: row, suffix: tag})
	}
	return m.browserCard(b)
}

// taskDetailContent builds the read-only body shown for a single task: a dim
// meta line followed by the glamour-rendered task body. It is the scrollable
// content placed into the detail viewport (m.backlogVP).
func (m model) taskDetailContent(t *v1.TaskDetail) string {
	var b strings.Builder
	readiness := "ready"
	if t.Status == "done" {
		readiness = "done"
	} else if !t.Ready {
		readiness = "blocked by " + strings.Join(t.BlockedBy, ", ")
	}
	meta := fmt.Sprintf("status:%s · p%d · %s", t.Status, t.Priority, readiness)
	if len(t.DependsOn) > 0 {
		meta += " · deps: " + strings.Join(t.DependsOn, ", ")
	}
	if len(t.SpecRefs) > 0 {
		meta += " · spec: " + strings.Join(t.SpecRefs, ", ")
	}
	b.WriteString(dimStyle.Render(meta) + "\n\n")
	body := t.Body
	if m.glam != nil {
		if out, err := m.glam.Render(body); err == nil {
			body = strings.Trim(out, "\n")
		}
	}
	b.WriteString(body)
	return strings.TrimRight(b.String(), "\n")
}

// refreshBacklogDetailVP (re)sizes the detail viewport to the current terminal
// dimensions and loads the current task's content. It is a no-op when no detail
// task is open or the terminal size is not yet known.
func (m *model) refreshBacklogDetailVP() {
	if m.backlogDetail == nil || !m.ready {
		return
	}
	h := m.h - 2 // one row for the title bar, one for the footer
	if h < 3 {
		h = 3
	}
	if m.backlogVP.Height() == 0 && m.backlogVP.Width() == 0 {
		m.backlogVP = viewport.New(viewport.WithWidth(m.w), viewport.WithHeight(h))
	} else {
		m.backlogVP.SetWidth(m.w)
		m.backlogVP.SetHeight(h)
	}
	m.backlogVP.SetContent(m.taskDetailContent(m.backlogDetail))
}

// taskDetailView renders a single task's full, read-only detail (spec §18.5) as a
// full-screen scrollable viewport (mirroring the transcript drill-in).
func (m model) taskDetailView(t *v1.TaskDetail) string {
	top := m.titleBar(" " + t.Id + " — " + t.Title + " ")
	body := ""
	if m.ready {
		body = m.backlogVP.View()
	}
	// Grooming footer (task 0099): the status prompt and transient notices take
	// precedence; the "e edit" affordance shows only when the file is local.
	hint := " ↑↓/pgup/pgdn scroll · +/- priority · s status"
	if taskFileLocal(t.Path) {
		hint += " · e edit"
	}
	hint += " · esc/← back · ctrl+c quit "
	if m.backlogStatusPrompt {
		hint = " set status: 1 todo · 2 in_progress · 3 in_review · 4 done · 5 blocked · 6 proposed · esc cancel "
	} else if m.backlogNotice != "" {
		hint = " " + m.backlogNotice + " "
	}
	help := m.footerBar(hint)
	return top + "\n" + body + "\n" + help
}
