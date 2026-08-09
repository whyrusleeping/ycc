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
