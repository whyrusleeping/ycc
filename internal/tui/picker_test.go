package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestProjectsTickRefreshesOnlyActivePicker(t *testing.T) {
	fc := newFakeClient()
	fc.projects = []*v1.ProjectInfo{{Name: "demo", Path: "/tmp/demo"}}
	m := model{client: fc, ctx: context.Background(), state: statePicker}

	next, cmd := m.Update(projectsTickMsg{})
	m = next.(model)
	if cmd == nil {
		t.Fatal("picker tick did not schedule a project refresh")
	}
	batchMsg := cmd()
	batch, ok := batchMsg.(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("picker tick command = %T len=%d, want two-command batch", batchMsg, len(batch))
	}
	msg := batch[0]()
	if fc.listProjectsCalls != 1 {
		t.Fatalf("ListProjects calls = %d, want 1", fc.listProjectsCalls)
	}
	updated, _ := m.Update(msg)
	m = updated.(model)
	if len(m.projects) != 1 || m.projects[0].Name != "demo" {
		t.Fatalf("projects after refresh = %+v", m.projects)
	}

	m.state = stateMenu
	if _, stopped := m.Update(projectsTickMsg{}); stopped != nil {
		t.Fatal("picker tick rescheduled after leaving picker")
	}
}

func TestRemoteAddBrowsesDaemonHostInsteadOfUsingClientCWD(t *testing.T) {
	fc := newFakeClient()
	fc.listDirResponses = map[string]*v1.ListDirResponse{
		"": {
			Path:    "/srv/code",
			Entries: []*v1.DirEntry{{Name: "demo", IsGitRepo: true}},
		},
		"/srv/code/demo": {Path: "/srv/code/demo", Parent: "/srv/code"},
	}
	m := initialModel(context.Background(), fc, "/client-only/cwd", true)
	m.state = statePicker

	m = drive(t, m, "ctrl+n")
	if fc.lastListDir == nil || fc.lastListDir.Path != "" || !fc.lastListDir.Suggest {
		t.Fatalf("initial ListDir = %+v, want daemon home with suggestions", fc.lastListDir)
	}
	m = drive(t, m, "enter")
	if m.dirPath != "/srv/code/demo" {
		t.Fatalf("opened path = %q, want daemon child", m.dirPath)
	}
	m = drive(t, m, "ctrl+a")
	if fc.lastAddProject == nil || fc.lastAddProject.Path != "/srv/code/demo" {
		t.Fatalf("AddProject = %+v, want daemon-host path", fc.lastAddProject)
	}
	if fc.lastAddProject.Path == "/client-only/cwd" {
		t.Fatal("remote add leaked the TUI process cwd to AddProject")
	}
	if m.project != "demo" || m.state != stateMenu {
		t.Fatalf("added project selection = %q state=%v, want demo home", m.project, m.state)
	}
}

func TestProjectSwitchResetsScopedStateAndDetachesSubscription(t *testing.T) {
	fc := newFakeClient()
	fc.projects = []*v1.ProjectInfo{
		{Name: "one", Path: "/remote/one", Git: &v1.GitStatus{Branch: "main", Dirty: true}},
		{Name: "two", Path: "/remote/two", Git: &v1.GitStatus{Branch: "feature"}},
	}
	m := initialModel(context.Background(), fc, "/client/cwd", true)
	m.state, m.project, m.workspace = statePicker, "one", "/remote/one"
	m.projects, m.projectCur = fc.projects, 1
	m.backlogTasks = []*v1.BacklogTaskSummary{{Id: "1"}}
	m.history = []*v1.SessionSummary{{SessionId: "old"}}
	m.lastSession = &v1.SessionSummary{SessionId: "old"}
	m.waitingSessions = []*v1.SessionSummary{{SessionId: "waiting"}}
	m.costRows = []*v1.UsageRow{{}}
	m.looping, m.todaySpendLoaded = true, true
	m.sessionID = "live-old"
	oldProjectSeq := m.projectSeq
	canceled := false
	m.sessionCancel = func() { canceled = true }

	m = drive(t, m, "enter")
	if m.project != "two" || m.workspace != "/remote/two" || m.state != stateMenu {
		t.Fatalf("selection = project %q workspace %q state %v", m.project, m.workspace, m.state)
	}
	if !canceled || m.sessionID != "" {
		t.Fatalf("subscription detach: canceled=%v session=%q", canceled, m.sessionID)
	}
	if m.backlogTasks != nil || m.history != nil || m.lastSession != nil || m.waitingSessions != nil || m.costRows != nil {
		t.Fatalf("project-scoped state survived switch: backlog=%v history=%v last=%v waiting=%v cost=%v",
			m.backlogTasks, m.history, m.lastSession, m.waitingSessions, m.costRows)
	}
	if m.looping || m.todaySpendLoaded {
		t.Fatalf("loop/spend state survived switch: looping=%v spendLoaded=%v", m.looping, m.todaySpendLoaded)
	}
	if m.gitBranch != "feature" || m.gitDirty {
		t.Fatalf("daemon git projection = %q dirty=%v", m.gitBranch, m.gitDirty)
	}

	// Slow responses issued under the old scope cannot repopulate cleared state.
	updated, _ := m.Update(backlogMsg{projectSeq: oldProjectSeq, tasks: []*v1.BacklogTaskSummary{{Id: "stale"}}})
	m = updated.(model)
	updated, _ = m.Update(waitingSessionsMsg{projectSeq: oldProjectSeq, sessions: []*v1.SessionSummary{{SessionId: "stale"}}})
	m = updated.(model)
	updated, _ = m.Update(menuSpendMsg{projectSeq: oldProjectSeq, cost: 99, status: "priced"})
	m = updated.(model)
	if m.backlogTasks != nil || m.waitingSessions != nil || m.todaySpendLoaded {
		t.Fatalf("stale project responses applied: backlog=%v waiting=%v spendLoaded=%v",
			m.backlogTasks, m.waitingSessions, m.todaySpendLoaded)
	}

	// A queued event from the canceled stream is tagged and ignored.
	updated, cmd := m.Update(sessionEvMsg{sessionID: "live-old", ev: &v1.Event{Type: "session_error"}})
	m = updated.(model)
	if cmd != nil || len(m.evs) != 0 {
		t.Fatalf("stale detached event applied: cmd=%v events=%d", cmd != nil, len(m.evs))
	}
}

func TestProjectRenameAndRemoveManageRegistrySafely(t *testing.T) {
	fc := newFakeClient()
	fc.projects = []*v1.ProjectInfo{{Name: "old", Path: "/srv/old"}}
	m := initialModel(context.Background(), fc, "", true)
	m.state, m.project, m.workspace = statePicker, "old", "/srv/old"
	m.projects = fc.projects

	updated, _ := m.Update(keyMsg("ctrl+e")) // ignore the cursor blink command
	m = updated.(model)
	m.projectInput.SetValue("renamed")
	m = drive(t, m, "enter")
	if fc.lastRenameProject == nil || fc.lastRenameProject.Name != "old" || fc.lastRenameProject.NewName != "renamed" {
		t.Fatalf("RenameProject = %+v", fc.lastRenameProject)
	}
	if m.project != "renamed" || m.projectMode != projectPickerList {
		t.Fatalf("renamed active project = %q mode=%v", m.project, m.projectMode)
	}

	m = drive(t, m, "ctrl+d")
	m = drive(t, m, "y")
	if fc.lastRemoveProject == nil || fc.lastRemoveProject.Name != "renamed" {
		t.Fatalf("RemoveProject = %+v", fc.lastRemoveProject)
	}
	if m.project != "" || m.workspace != "" || m.state != statePicker {
		t.Fatalf("removed active project left scope project=%q workspace=%q state=%v", m.project, m.workspace, m.state)
	}
}

func TestGitStatusBadge(t *testing.T) {
	tests := []struct {
		name   string
		status *v1.GitStatus
		want   string
	}{
		{name: "not a repository", want: ""},
		{name: "in sync", status: &v1.GitStatus{HasUpstream: true, LastFetchUnix: 1}, want: ""},
		{name: "ahead behind dirty", status: &v1.GitStatus{HasUpstream: true, Ahead: 2, Behind: 3, Dirty: true, LastFetchUnix: 1}, want: "↑2 ↓3 ●"},
		{name: "no upstream omits counts", status: &v1.GitStatus{Ahead: 2, Behind: 3, Dirty: true, LastFetchUnix: 1}, want: "●"},
		{name: "not fetched", status: &v1.GitStatus{HasUpstream: true}, want: "?"},
		{name: "fetch failed", status: &v1.GitStatus{HasUpstream: true, Behind: 1, LastFetchUnix: 1, FetchError: "offline"}, want: "↓1 ?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gitStatusBadge(tt.status); got != tt.want {
				t.Fatalf("gitStatusBadge() = %q, want %q", got, tt.want)
			}
		})
	}
}
