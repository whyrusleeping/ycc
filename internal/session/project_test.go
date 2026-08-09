package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/project"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// TestManagerProjectCRUD exercises the manager's project registry surface used by
// the Add/List/Remove RPCs (spec §3.1).
func TestManagerProjectCRUD(t *testing.T) {
	m := NewManager(testRegistry(), t.TempDir())
	m.SetProjects(project.NewMemory())

	dir := t.TempDir()
	p, err := m.AddProject(dir, "demo")
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if p.Name != "demo" || p.Path != dir {
		t.Fatalf("AddProject = %+v, want name=demo path=%s", p, dir)
	}
	if got := m.Projects(); len(got) != 1 || got[0].Name != "demo" {
		t.Fatalf("Projects = %+v, want [demo]", got)
	}
	if err := m.RemoveProject("demo"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if got := m.Projects(); len(got) != 0 {
		t.Fatalf("Projects after remove = %+v, want empty", got)
	}
}

// TestManagerRenameProject verifies the manager-level rename updates the project
// registry and relabels the project's workstreams (spec §3.1).
func TestManagerRenameProject(t *testing.T) {
	m := NewManager(testRegistry(), t.TempDir())
	m.SetProjects(project.NewMemory())

	dir := t.TempDir()
	if _, err := m.AddProject(dir, "demo"); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if err := m.workstreams.Add(workstream.Workstream{
		ID: "ws_1", Project: "demo", Branch: "ycc/ws/ws_1",
		WorktreePath: t.TempDir(), Status: workstream.StatusActive,
	}); err != nil {
		t.Fatalf("workstreams.Add: %v", err)
	}

	p, err := m.RenameProject("demo", "demo2")
	if err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	if p.Name != "demo2" || p.Path != dir {
		t.Fatalf("RenameProject = %+v, want name=demo2 path=%s", p, dir)
	}
	if got := m.Projects(); len(got) != 1 || got[0].Name != "demo2" {
		t.Fatalf("Projects = %+v, want [demo2]", got)
	}
	if got := m.workstreams.ListByProject("demo2"); len(got) != 1 || got[0].ID != "ws_1" {
		t.Fatalf("workstreams under demo2 = %+v, want [ws_1]", got)
	}
	if _, err := m.RenameProject("demo", "x"); err == nil {
		t.Fatal("rename of stale old name succeeded, want error")
	}
}

// TestStartUnknownProject verifies Start rejects an unregistered project name
// before doing any session work.
func TestStartUnknownProject(t *testing.T) {
	m := NewManager(testRegistry(), t.TempDir())
	m.SetProjects(project.NewMemory())

	_, err := m.Start(Config{Project: "nope", Prompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "unknown project") {
		t.Fatalf("Start unknown project err = %v, want unknown project", err)
	}
}

func TestResolveProjectWorkspaceRejectsAmbiguousOmission(t *testing.T) {
	m := NewManager(testRegistry(), t.TempDir())
	if _, err := m.AddProject(t.TempDir(), "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.resolveProjectWorkspace(""); !errors.Is(err, ErrUnknownProject) || !strings.Contains(err.Error(), "project is required") {
		t.Fatalf("resolve omitted project = %v, want required-selection error", err)
	}
}

// TestListByProjectUnknown returns no sessions for an unknown project filter.
func TestListByProjectUnknown(t *testing.T) {
	m := NewManager(testRegistry(), t.TempDir())
	m.SetProjects(project.NewMemory())
	if got := m.ListByProject("missing"); got != nil {
		t.Fatalf("ListByProject(missing) = %v, want nil", got)
	}
}
