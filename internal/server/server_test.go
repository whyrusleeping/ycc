package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
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

// AnswerQuestions maps an unknown session to a NotFound connect error, mirroring
// the single-question AnswerQuestion RPC.
func TestAnswerQuestionsUnknownSession(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	srv := New(session.NewManager(reg, t.TempDir()))

	_, err := srv.AnswerQuestions(context.Background(), connect.NewRequest(&v1.AnswerQuestionsRequest{
		SessionId: "nope",
		Answers:   []*v1.QuestionAnswer{{Text: "a"}, {OptionIndex: 1}},
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

// TestBacklogRPCs exercises the read-only backlog browser surface (spec §18.5):
// ListBacklog projects summary rows with readiness, GetTask returns a task's full
// detail (with blocking deps), and an unknown id is a NotFound error.
func TestBacklogRPCs(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	ws := t.TempDir()
	store := docs.NewStore(ws)
	a, err := store.Create("First task", "", 1, nil, nil)
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	b, err := store.Create("Second task", "", 2, []string{a.ID}, nil)
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}

	srv := New(session.NewManager(reg, ws))
	ctx := context.Background()

	list, err := srv.ListBacklog(ctx, connect.NewRequest(&v1.ListBacklogRequest{}))
	if err != nil {
		t.Fatalf("ListBacklog: %v", err)
	}
	if len(list.Msg.Tasks) != 2 {
		t.Fatalf("ListBacklog = %d tasks, want 2", len(list.Msg.Tasks))
	}
	first := list.Msg.Tasks[0]
	if first.GetId() != a.ID || !first.GetReady() || len(first.GetBlockedBy()) != 0 {
		t.Fatalf("first task = %+v, want ready with no blockers", first)
	}
	second := list.Msg.Tasks[1]
	if second.GetId() != b.ID || second.GetReady() {
		t.Fatalf("second task = %+v, want not ready", second)
	}
	if len(second.GetBlockedBy()) != 1 || second.GetBlockedBy()[0] != a.ID {
		t.Fatalf("second blocked_by = %v, want [%s]", second.GetBlockedBy(), a.ID)
	}

	det, err := srv.GetTask(ctx, connect.NewRequest(&v1.GetTaskRequest{Id: b.ID}))
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	td := det.Msg.Task
	if td.GetTitle() != "Second task" || td.GetBody() == "" {
		t.Fatalf("GetTask detail = %+v, want title+body populated", td)
	}
	if len(td.GetDependsOn()) != 1 || td.GetDependsOn()[0] != a.ID {
		t.Fatalf("GetTask depends_on = %v, want [%s]", td.GetDependsOn(), a.ID)
	}
	if len(td.GetBlockedBy()) != 1 || td.GetBlockedBy()[0] != a.ID {
		t.Fatalf("GetTask blocked_by = %v, want [%s]", td.GetBlockedBy(), a.ID)
	}

	if _, err := srv.GetTask(ctx, connect.NewRequest(&v1.GetTaskRequest{Id: "9999"})); err == nil {
		t.Fatal("GetTask with bogus id: expected error")
	} else if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound", got)
	}
}

func TestModelBackendRPCs(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	srv := New(session.NewManager(reg, t.TempDir()))
	ctx := context.Background()

	// Add a new backend (live only).
	_, err := srv.UpsertModel(ctx, connect.NewRequest(&v1.UpsertModelRequest{
		Model: &v1.ModelConfig{
			Name: "gpt", Backend: "openai", BaseUrl: "https://oai", Model: "gpt-4o", KeyEnv: "OPENAI_API_KEY",
		},
	}))
	if err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}

	// It now appears in ListModels.
	list, err := srv.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{}))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	var found bool
	for _, m := range list.Msg.Models {
		if m.Name == "gpt" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected gpt in ListModels after upsert")
	}

	// GetModelConfig round-trips the record, including key_env (a reference, not
	// a secret value).
	got, err := srv.GetModelConfig(ctx, connect.NewRequest(&v1.GetModelConfigRequest{Name: "gpt"}))
	if err != nil {
		t.Fatalf("GetModelConfig: %v", err)
	}
	mc := got.Msg.Model
	if mc.Backend != "openai" || mc.Model != "gpt-4o" || mc.KeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("GetModelConfig = %+v", mc)
	}

	// Removing a role-referenced model is rejected.
	if _, err := srv.RemoveModel(ctx, connect.NewRequest(&v1.RemoveModelRequest{Name: "a"})); err == nil {
		t.Fatal("expected error removing role-referenced model")
	}

	// Removing the unreferenced model succeeds.
	if _, err := srv.RemoveModel(ctx, connect.NewRequest(&v1.RemoveModelRequest{Name: "gpt"})); err != nil {
		t.Fatalf("RemoveModel(gpt): %v", err)
	}
	if _, err := srv.GetModelConfig(ctx, connect.NewRequest(&v1.GetModelConfigRequest{Name: "gpt"})); err == nil {
		t.Fatal("expected NotFound after removal")
	}
}
