package session

import (
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/project"
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

// TestListByProjectUnknown returns no sessions for an unknown project filter.
func TestListByProjectUnknown(t *testing.T) {
	m := NewManager(testRegistry(), t.TempDir())
	m.SetProjects(project.NewMemory())
	if got := m.ListByProject("missing"); got != nil {
		t.Fatalf("ListByProject(missing) = %v, want nil", got)
	}
}
