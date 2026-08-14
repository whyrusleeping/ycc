// This file owns the persistent-daemon project hub and server-side directory browser.
package tui

import (
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	tea "charm.land/bubbletea/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

type projectPickerMode int

const (
	projectPickerList projectPickerMode = iota
	projectPickerAdd
	projectPickerRename
	projectPickerRemove
)

func (m model) fetchProjects() tea.Msg {
	resp, err := m.client.ListProjects(m.ctx, connect.NewRequest(&v1.ListProjectsRequest{}))
	if err != nil {
		return errMsg{err}
	}
	return projectsMsg{resp.Msg.Projects}
}

// projectsRefreshTick keeps the hub's daemon-cached git snapshots current. The
// chain is active only while the project list (not its add/rename subview) is open.
func (m model) projectsRefreshTick() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return projectsTickMsg{} })
}

func (m model) listDir(path string, suggest bool) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.ListDir(m.ctx, connect.NewRequest(&v1.ListDirRequest{Path: path, Suggest: suggest}))
		if err != nil {
			return dirMsg{err: err}
		}
		return dirMsg{
			path: resp.Msg.Path, parent: resp.Msg.Parent,
			entries: resp.Msg.Entries, suggestions: resp.Msg.Suggestions,
		}
	}
}

func (m model) addProject(path string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.AddProject(m.ctx, connect.NewRequest(&v1.AddProjectRequest{Path: path}))
		if err != nil {
			return projectAddedMsg{err: err}
		}
		return projectAddedMsg{project: resp.Msg.Project}
	}
}

func (m model) renameProject(oldName, newName string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.RenameProject(m.ctx, connect.NewRequest(&v1.RenameProjectRequest{
			Name: oldName, NewName: newName,
		}))
		if err != nil {
			return projectRenamedMsg{oldName: oldName, err: err}
		}
		return projectRenamedMsg{oldName: oldName, project: resp.Msg.Project}
	}
}

func (m model) removeProject(name string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.RemoveProject(m.ctx, connect.NewRequest(&v1.RemoveProjectRequest{Name: name}))
		return projectRemovedMsg{name: name, err: err}
	}
}

func (m *model) selectedProject() *v1.ProjectInfo {
	if len(m.projects) == 0 || m.projectCur < 0 || m.projectCur >= len(m.projects) {
		return nil
	}
	return m.projects[m.projectCur]
}

func (m *model) openProjectHub() tea.Cmd {
	m.state = statePicker
	m.projectMode = projectPickerList
	m.projectBusy = false
	m.projectNote = ""
	for i, p := range m.projects {
		if p.Name == m.project {
			m.projectCur = i
			break
		}
	}
	return tea.Batch(m.fetchProjects, m.projectsRefreshTick())
}

func (m *model) openProjectAdd() tea.Cmd {
	m.projectMode = projectPickerAdd
	m.projectBusy = false
	m.projectNote = ""
	m.dirPath, m.dirParent = "", ""
	m.dirEntries, m.dirSuggestions = nil, nil
	m.dirCur, m.dirLoading = 0, true
	return m.listDir("", true)
}

// selectProject changes the client scope without touching daemon-owned work. A
// subscription left behind by "back home" is canceled locally; the remote
// session continues and will appear in that project's history/waiting counts.
func (m *model) selectProject(p *v1.ProjectInfo) tea.Cmd {
	if p == nil {
		return nil
	}
	if m.sessionCancel != nil {
		m.sessionCancel()
		m.sessionCancel, m.sessionCtx = nil, nil
	}
	m.sessionID = ""
	m.project, m.workspace = p.Name, p.Path
	m.state, m.projectMode = stateMenu, projectPickerList
	m.resetProjectProjection()
	m.applyDaemonGit(p.Git)
	return m.refreshMenu()
}

// resetProjectProjection prevents data fetched for the previous project from
// appearing under the new project name while its refresh RPCs are in flight.
func (m *model) resetProjectProjection() {
	m.projectSeq++
	m.waitingSeq++
	m.loopSeq++
	m.wsTick++
	m.costGen++

	m.backlogTasks, m.backlogDetail = nil, nil
	m.backlogCursor, m.backlogBlockedOnly = 0, false
	m.backlogSelected = map[string]bool{}
	m.history, m.lastSession, m.waitingSessions = nil, nil, nil
	m.historyCursor, m.historyMsgTxt = 0, ""
	m.historyTranscript, m.historyWaitingOnly = false, false
	m.plansList, m.planDetail = nil, nil
	m.costRows, m.costTotal, m.subUsageAccounts = nil, nil, nil
	m.costWorkspace, m.costTask, m.costMsg = "", "", ""
	m.wsList, m.wsLocal = nil, nil
	m.loop, m.looping, m.loopArmed = false, false, false
	m.loopInfo, m.loopDigest = nil, nil
	m.digest = false
	m.gitBranch, m.gitDirty = "", false
	m.todaySpend, m.todaySpendStatus, m.todaySpendLoaded = 0, "", false
	m.lastSpendFetch = time.Time{}
}

func (m *model) applyDaemonGit(status *v1.GitStatus) {
	m.gitBranch, m.gitDirty = "", false
	if status != nil {
		m.gitBranch, m.gitDirty = status.Branch, status.Dirty
	}
}

func (m model) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.projectMode == projectPickerRename {
		return m.updateProjectRename(msg)
	}
	if m.projectMode == projectPickerRemove {
		return m.updateProjectRemove(msg)
	}
	if m.projectMode == projectPickerAdd {
		return m.updateProjectAdd(msg)
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "q":
		return m.confirmQuit()
	case "esc":
		if m.project != "" {
			m.state = stateMenu
			return m, m.refreshMenu()
		}
		return m.confirmQuit()
	case "up":
		m.projectCur = navUp(m.projectCur)
		return m, nil
	case "down":
		m.projectCur = navDown(m.projectCur, len(m.projects))
		return m, nil
	case "ctrl+n":
		return m, m.openProjectAdd()
	case "ctrl+e":
		if p := m.selectedProject(); p != nil {
			m.projectMode = projectPickerRename
			m.projectInput.SetValue(p.Name)
			m.projectInput.CursorEnd()
			return m, m.projectInput.Focus()
		}
	case "ctrl+d":
		if m.selectedProject() != nil {
			m.projectMode = projectPickerRemove
			m.projectNote = ""
		}
		return m, nil
	case "enter":
		if p := m.selectedProject(); p != nil {
			return m, m.selectProject(p)
		}
	}
	return m, nil
}

func (m model) updateProjectRename(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m.confirmQuit()
		case "esc":
			m.projectMode, m.projectBusy, m.projectNote = projectPickerList, false, ""
			m.projectInput.Blur()
			return m, nil
		case "enter":
			p := m.selectedProject()
			name := strings.TrimSpace(m.projectInput.Value())
			if p == nil || name == "" || m.projectBusy {
				return m, nil
			}
			m.projectBusy, m.projectNote = true, "renaming…"
			return m, m.renameProject(p.Name, name)
		}
	}
	var cmd tea.Cmd
	m.projectInput, cmd = m.projectInput.Update(msg)
	return m, cmd
}

func (m model) updateProjectRemove(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc", "n", "N":
		m.projectMode, m.projectBusy, m.projectNote = projectPickerList, false, ""
	case "y", "Y":
		if p := m.selectedProject(); p != nil && !m.projectBusy {
			m.projectBusy, m.projectNote = true, "removing…"
			return m, m.removeProject(p.Name)
		}
	}
	return m, nil
}

func (m model) updateProjectAdd(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	rows := len(m.dirSuggestions) + len(m.dirEntries)
	switch key.String() {
	case "ctrl+c":
		return m.confirmQuit()
	case "esc":
		m.projectMode, m.projectBusy, m.projectNote = projectPickerList, false, ""
		return m, nil
	case "up":
		m.dirCur = navUp(m.dirCur)
	case "down":
		m.dirCur = navDown(m.dirCur, rows)
	case "backspace", "left":
		if m.dirParent != "" && !m.dirLoading {
			m.dirLoading, m.projectNote = true, ""
			return m, m.listDir(m.dirParent, false)
		}
	case "enter", "right":
		if m.dirLoading || rows == 0 {
			return m, nil
		}
		var target string
		if m.dirCur < len(m.dirSuggestions) {
			target = m.dirSuggestions[m.dirCur]
		} else {
			entry := m.dirEntries[m.dirCur-len(m.dirSuggestions)]
			target = serverChildPath(m.dirPath, entry.Name)
		}
		m.dirLoading, m.projectNote = true, ""
		return m, m.listDir(target, false)
	case "ctrl+a":
		if m.dirPath != "" && !m.dirLoading && !m.projectBusy {
			m.projectBusy, m.projectNote = true, "adding…"
			return m, m.addProject(m.dirPath)
		}
	}
	return m, nil
}

func serverChildPath(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return strings.TrimRight(parent, "/") + "/" + name
}

func gitStatusBadge(status *v1.GitStatus) string {
	if status == nil {
		return ""
	}
	var parts []string
	if status.HasUpstream {
		if status.Ahead > 0 {
			parts = append(parts, fmt.Sprintf("↑%d", status.Ahead))
		}
		if status.Behind > 0 {
			parts = append(parts, fmt.Sprintf("↓%d", status.Behind))
		}
	}
	if status.Dirty {
		parts = append(parts, "●")
	}
	if status.LastFetchUnix == 0 || status.FetchError != "" {
		parts = append(parts, "?")
	}
	return strings.Join(parts, " ")
}

func (m model) pickerScreenView() string {
	switch m.projectMode {
	case projectPickerAdd:
		return m.projectAddView()
	case projectPickerRename:
		return m.projectRenameView()
	case projectPickerRemove:
		return m.projectRemoveView()
	}
	var b strings.Builder
	b.WriteString(m.titleBar(" ycc — projects ") + "\n\n")
	if m.flashErr != "" {
		b.WriteString("  " + errStyle.Render("✗ "+m.flashErr) + "\n\n")
	}
	if m.projectNote != "" {
		b.WriteString("  " + dimStyle.Render(m.projectNote) + "\n\n")
	}
	if len(m.projects) == 0 {
		b.WriteString("  " + dimStyle.Render("no projects registered yet") + "\n")
	}
	for i, p := range m.projects {
		cursor := "  "
		name := fmt.Sprintf("%-20s ", p.Name)
		if p.Name == m.project {
			name = "• " + fmt.Sprintf("%-18s ", p.Name)
		}
		badge := gitStatusBadge(p.Git)
		if badge != "" {
			badge = dimStyle.Render(badge + " ")
		}
		label := name + badge + dimStyle.Render(p.Path)
		if i == m.projectCur {
			cursor = selStyle.Render("▸ ")
			label = selStyle.Render(name) + badge + dimStyle.Render(p.Path)
		}
		b.WriteString("  " + cursor + label + "\n")
	}
	b.WriteString("\n" + m.footerBar("  ↑/↓ choose · enter open · ctrl+n add · ctrl+e rename · ctrl+d remove"))
	if m.project != "" {
		b.WriteString("\n" + m.footerBar("  esc back to "+m.project+" · q quit"))
	} else {
		b.WriteString("\n" + m.footerBar("  q quit"))
	}
	return b.String()
}

func (m model) projectAddView() string {
	var b strings.Builder
	b.WriteString(m.titleBar(" ycc — add project on daemon ") + "\n\n")
	path := m.dirPath
	if path == "" {
		path = "daemon home"
	}
	b.WriteString("  " + typeStyle.Render(path) + "\n")
	b.WriteString("  " + dimStyle.Render("Browse the daemon host; ctrl+a registers the directory shown above.") + "\n\n")
	if m.projectNote != "" {
		b.WriteString("  " + dimStyle.Render(m.projectNote) + "\n\n")
	}
	row := 0
	if len(m.dirSuggestions) > 0 {
		b.WriteString("  " + dimStyle.Render("SUGGESTED PROJECTS") + "\n")
		for _, suggestion := range m.dirSuggestions {
			b.WriteString(m.directoryRow(row, "★ "+suggestion, true))
			row++
		}
		b.WriteString("\n")
	}
	if len(m.dirEntries) == 0 && !m.dirLoading {
		b.WriteString("  " + dimStyle.Render("no subdirectories") + "\n")
	}
	for _, entry := range m.dirEntries {
		marker := "  "
		if entry.IsGitRepo {
			marker = "git"
		}
		if entry.IsRegistered {
			marker = "✓ "
		}
		b.WriteString(m.directoryRow(row, fmt.Sprintf("%-3s %s/", marker, entry.Name), false))
		row++
	}
	if m.dirLoading {
		b.WriteString("  " + dimStyle.Render("loading…") + "\n")
	}
	b.WriteString("\n" + m.footerBar("  ↑/↓ choose · enter open · ←/backspace parent · ctrl+a add this directory · esc cancel"))
	return b.String()
}

func (m model) directoryRow(row int, label string, suggestion bool) string {
	cursor := "  "
	style := dimStyle.Render
	if suggestion {
		style = typeStyle.Render
	}
	if row == m.dirCur {
		cursor = selStyle.Render("▸ ")
		style = selStyle.Render
	}
	return "  " + cursor + style(label) + "\n"
}

func (m model) projectRenameView() string {
	name := "project"
	if p := m.selectedProject(); p != nil {
		name = p.Name
	}
	var b strings.Builder
	b.WriteString(m.titleBar(" ycc — rename project ") + "\n\n")
	b.WriteString("  Rename " + typeStyle.Render(name) + " (workspace contents are unchanged)\n\n")
	b.WriteString("  " + m.projectInput.View() + "\n")
	if m.projectNote != "" {
		b.WriteString("\n  " + dimStyle.Render(m.projectNote) + "\n")
	}
	b.WriteString("\n" + m.footerBar("  enter rename · esc cancel"))
	return b.String()
}

func (m model) projectRemoveView() string {
	name := "project"
	path := ""
	if p := m.selectedProject(); p != nil {
		name, path = p.Name, p.Path
	}
	var b strings.Builder
	b.WriteString(m.titleBar(" ycc — remove project ") + "\n\n")
	b.WriteString("  " + errStyle.Render("Remove "+name+" from this daemon?") + "\n")
	b.WriteString("  " + dimStyle.Render(path) + "\n\n")
	b.WriteString("  Workspace files and session logs are not deleted.\n")
	if m.projectNote != "" {
		b.WriteString("\n  " + dimStyle.Render(m.projectNote) + "\n")
	}
	b.WriteString("\n" + m.footerBar("  y remove · n/esc cancel"))
	return b.String()
}
