package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// SetThinking with no live session persists the new level as the default
// (roles.thinking.*) rather than erroring — a thinking change from the home menu
// must survive a restart (spec §7.4, §18.2). An invalid level is still rejected.
func TestSetThinkingNoSessionPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := config.Save(path, &config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	reg := config.NewRegistry(cfg)
	reg.SetPath(path)
	srv := New(session.NewManager(reg, t.TempDir()))
	ctx := context.Background()

	// No session id → persist the coordinator's default thinking level.
	if _, err := srv.SetThinking(ctx, connect.NewRequest(&v1.SetThinkingRequest{
		Role: "coordinator", Level: "low",
	})); err != nil {
		t.Fatalf("SetThinking (no session): %v", err)
	}
	list, err := srv.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{}))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if list.Msg.CoordinatorThinking != "low" {
		t.Fatalf("coordinator thinking = %q, want low", list.Msg.CoordinatorThinking)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Roles.Thinking.Coordinator != "low" {
		t.Fatalf("persisted thinking = %q, want low", reloaded.Roles.Thinking.Coordinator)
	}

	// An invalid level is still rejected.
	if _, err := srv.SetThinking(ctx, connect.NewRequest(&v1.SetThinkingRequest{
		Role: "coordinator", Level: "bogus",
	})); err == nil {
		t.Fatal("expected error for invalid thinking level")
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
	if td.GetPath() == "" {
		t.Fatal("GetTask path empty, want the task file path for local editor gating")
	}

	if _, err := srv.GetTask(ctx, connect.NewRequest(&v1.GetTaskRequest{Id: "9999"})); err == nil {
		t.Fatal("GetTask with bogus id: expected error")
	} else if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound", got)
	}
}

// TestUpdateTask exercises the backlog grooming RPC (task 0099): status/priority
// mutations persist to the task file; invalid inputs are rejected; an unknown id
// is NotFound; and a no-field "refresh" re-reads the task without altering it.
func TestUpdateTask(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	ws := t.TempDir()
	store := docs.NewStore(ws)
	a, err := store.Create("First task", "", 3, nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	srv := New(session.NewManager(reg, ws))
	ctx := context.Background()

	// Status + priority change persists.
	status := "in_review"
	prio := int32(1)
	resp, err := srv.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{Id: a.ID, Status: &status, Priority: &prio}))
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if resp.Msg.Task.GetStatus() != "in_review" || resp.Msg.Task.GetPriority() != 1 {
		t.Fatalf("UpdateTask result = %+v, want status=in_review priority=1", resp.Msg.Task)
	}
	got, err := store.Get(a.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Status != docs.StatusInReview || got.Priority != 1 {
		t.Fatalf("persisted task = status:%s p%d, want in_review p1", got.Status, got.Priority)
	}

	// Invalid status and out-of-range priority are rejected.
	bad := "nonsense"
	if _, err := srv.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{Id: a.ID, Status: &bad})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid status code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	badPrio := int32(9)
	if _, err := srv.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{Id: a.ID, Priority: &badPrio})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid priority code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	blank := "   "
	if _, err := srv.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{Id: a.ID, Title: &blank})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("blank title code = %v, want InvalidArgument", connect.CodeOf(err))
	}

	// Unknown id is NotFound.
	if _, err := srv.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{Id: "9999", Status: &status})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown id code = %v, want NotFound", connect.CodeOf(err))
	}

	// No-field refresh succeeds and re-reads the task (used after $EDITOR).
	refresh, err := srv.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{Id: a.ID}))
	if err != nil {
		t.Fatalf("refresh UpdateTask: %v", err)
	}
	if refresh.Msg.Task.GetStatus() != "in_review" {
		t.Fatalf("refresh result = %+v, want status=in_review unchanged", refresh.Msg.Task)
	}
}

// TestPlanRPCs exercises the read-only plan library browser surface (task 0077):
// ListPlans projects saved plans (name/title), GetPlan returns a plan's markdown
// content, and an unknown name is a NotFound error.
func TestPlanRPCs(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	ws := t.TempDir()
	store := docs.NewStore(ws)
	if _, err := store.SavePlan("my-plan", "# My Plan\nsteps"); err != nil {
		t.Fatalf("SavePlan: %v", err)
	}

	srv := New(session.NewManager(reg, ws))
	ctx := context.Background()

	list, err := srv.ListPlans(ctx, connect.NewRequest(&v1.ListPlansRequest{}))
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if len(list.Msg.Plans) != 1 {
		t.Fatalf("ListPlans = %d plans, want 1", len(list.Msg.Plans))
	}
	p := list.Msg.Plans[0]
	if p.GetName() != "my-plan" || p.GetTitle() != "My Plan" {
		t.Fatalf("plan = %+v, want name=my-plan title=My Plan", p)
	}

	got, err := srv.GetPlan(ctx, connect.NewRequest(&v1.GetPlanRequest{Name: "my-plan"}))
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if !strings.HasPrefix(got.Msg.GetContent(), "# My Plan\nsteps") || got.Msg.GetTitle() != "My Plan" {
		t.Fatalf("GetPlan = %+v, want content+title populated", got.Msg)
	}

	if _, err := srv.GetPlan(ctx, connect.NewRequest(&v1.GetPlanRequest{Name: "nope"})); err == nil {
		t.Fatal("GetPlan with bogus name: expected error")
	} else if code := connect.CodeOf(err); code != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound", code)
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

// TestSetRoleConfigNoSessionPersists covers the home-menu path: a role change
// made with no live session (empty session_id) updates the persisted default and
// is reflected by ListModels (spec §18.2).
func TestSetRoleConfigNoSessionPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := config.Save(path, &config.Config{
		Models: map[string]config.Model{
			"a":     {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"},
			"fable": {Backend: "anthropic", BaseURL: "https://api", Model: "claude-fable-5", KeyEnv: "ANTHROPIC_API_KEY"},
		},
		Roles: config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	reg := config.NewRegistry(cfg)
	reg.SetPath(path)
	srv := New(session.NewManager(reg, t.TempDir()))
	ctx := context.Background()

	// Change the coordinator with NO session id — must not error and must persist.
	if _, err := srv.SetRoleConfig(ctx, connect.NewRequest(&v1.SetRoleConfigRequest{
		Coordinator: "fable",
	})); err != nil {
		t.Fatalf("SetRoleConfig (no session): %v", err)
	}

	// ListModels reflects the new coordinator.
	list, err := srv.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{}))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if list.Msg.Coordinator != "fable" {
		t.Fatalf("ListModels coordinator = %q, want fable", list.Msg.Coordinator)
	}

	// And it is on disk: a fresh Load sees the new default.
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Roles.Coordinator != "fable" {
		t.Fatalf("persisted coordinator = %q, want fable", reloaded.Roles.Coordinator)
	}
}

// TestGetSessionTranscript covers the read-only transcript RPC (spec §18.6): a
// persisted on-disk session log is read and converted to proto events, an unknown
// session is a NotFound error, and a live session returns its in-memory snapshot.
func TestGetSessionTranscript(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	ws := t.TempDir()
	srv := New(session.NewManager(reg, ws))
	ctx := context.Background()

	// Persist a session log on disk under .ycc/sessions/<id>/events.jsonl.
	id := "sess_persisted"
	logPath := filepath.Join(ws, ".ycc", "sessions", id, "events.jsonl")
	lg, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	lg.Record("user", event.UserInput, map[string]any{"text": "do the thing"})
	lg.Record("coordinator", event.ModelTurn, map[string]any{"text": "on it"})
	lg.Close()

	resp, err := srv.GetSessionTranscript(ctx, connect.NewRequest(&v1.GetSessionTranscriptRequest{SessionId: id}))
	if err != nil {
		t.Fatalf("GetSessionTranscript: %v", err)
	}
	if len(resp.Msg.Events) != 2 {
		t.Fatalf("transcript = %d events, want 2", len(resp.Msg.Events))
	}
	if resp.Msg.Events[0].Type != string(event.UserInput) || resp.Msg.Events[1].Type != string(event.ModelTurn) {
		t.Fatalf("transcript event types = %q/%q", resp.Msg.Events[0].Type, resp.Msg.Events[1].Type)
	}

	// Unknown session => NotFound.
	if _, err := srv.GetSessionTranscript(ctx, connect.NewRequest(&v1.GetSessionTranscriptRequest{SessionId: "nope"})); err == nil {
		t.Fatal("expected NotFound for unknown session")
	} else if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound", got)
	}

	// Live session: reopening registers it in the manager, after which the
	// transcript is served from the in-memory snapshot (the Get path) rather than
	// re-read from disk. The reopened session's own marker event is included.
	if _, err := srv.ResumeSession(ctx, connect.NewRequest(&v1.ResumeSessionRequest{SessionId: id})); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	defer srv.mgr.Stop(id)
	live, err := srv.GetSessionTranscript(ctx, connect.NewRequest(&v1.GetSessionTranscriptRequest{SessionId: id}))
	if err != nil {
		t.Fatalf("GetSessionTranscript (live): %v", err)
	}
	if len(live.Msg.Events) < 2 {
		t.Fatalf("live transcript = %d events, want >= 2", len(live.Msg.Events))
	}
}
