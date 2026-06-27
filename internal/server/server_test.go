package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// SetThinking maps an unknown session to a NotFound connect error, mirroring the
// other settings RPCs. (A live session requires a running backend, so the
// happy/InvalidArgument paths are covered by the session package's unit tests.)
func TestSetThinkingUnknownSession(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	srv := New(session.NewManager(reg, t.TempDir()))

	_, err := srv.SetThinking(context.Background(), connect.NewRequest(&v1.SetThinkingRequest{
		SessionId: "nope", Level: "high",
	}))
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound", got)
	}
}

// TestProjectRPCs roundtrips the project registry through the RPC surface:
// AddProject registers a workspace, ListProjects returns it, and RemoveProject
// drops it (spec §3.1).
func TestProjectRPCs(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	srv := New(session.NewManager(reg, t.TempDir()))
	ctx := context.Background()

	dir := t.TempDir()
	add, err := srv.AddProject(ctx, connect.NewRequest(&v1.AddProjectRequest{Path: dir, Name: "demo"}))
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if add.Msg.Project.GetName() != "demo" || add.Msg.Project.GetPath() != dir {
		t.Fatalf("AddProject = %+v, want name=demo path=%s", add.Msg.Project, dir)
	}

	list, err := srv.ListProjects(ctx, connect.NewRequest(&v1.ListProjectsRequest{}))
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list.Msg.Projects) != 1 || list.Msg.Projects[0].GetName() != "demo" {
		t.Fatalf("ListProjects = %+v, want [demo]", list.Msg.Projects)
	}

	if _, err := srv.RemoveProject(ctx, connect.NewRequest(&v1.RemoveProjectRequest{Name: "demo"})); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	list2, _ := srv.ListProjects(ctx, connect.NewRequest(&v1.ListProjectsRequest{}))
	if len(list2.Msg.Projects) != 0 {
		t.Fatalf("ListProjects after remove = %+v, want empty", list2.Msg.Projects)
	}

	// AddProject without a path is an InvalidArgument error.
	if _, err := srv.AddProject(ctx, connect.NewRequest(&v1.AddProjectRequest{})); err == nil {
		t.Fatal("AddProject with empty path: expected error")
	} else if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
}
