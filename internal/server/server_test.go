package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func boolPtr(value bool) *bool { return &value }

// SetThinking with no live session resolves the requested role to its current
// model and persists the level in that model's config — a home-menu change must
// survive a restart (spec §7.4, §18.2). An invalid level is still rejected.
func TestSetThinkingNoSessionPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := config.Save(path, &config.Config{
		Models: map[string]config.Model{
			"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"},
			"b": {Backend: "ollama", BaseURL: "http://localhost:2", Model: "model-b"},
		},
		Roles: config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a", "b"}},
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
	if mdl := reloaded.Models["a"]; mdl.Thinking != "adaptive" || mdl.Effort != "low" {
		t.Fatalf("persisted model thinking = %+v, want adaptive/low", mdl)
	}
	if mdl := reloaded.Models["b"]; mdl.Thinking != "" || mdl.Effort != "" {
		t.Fatalf("untargeted reviewer model changed = %+v", mdl)
	}

	// The reviewer role targets every reviewer model. Here a is shared with the
	// coordinator, so the coordinator row also resolves the newly stored level.
	if _, err := srv.SetThinking(ctx, connect.NewRequest(&v1.SetThinkingRequest{
		Role: "reviewers", Level: "max",
	})); err != nil {
		t.Fatalf("SetThinking(reviewers): %v", err)
	}
	list, err = srv.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{}))
	if err != nil {
		t.Fatalf("ListModels after reviewer update: %v", err)
	}
	if list.Msg.CoordinatorThinking != "max" || list.Msg.ReviewersThinking != "max" {
		t.Fatalf("shared model levels: coordinator=%q reviewers=%q, want max/max", list.Msg.CoordinatorThinking, list.Msg.ReviewersThinking)
	}
	reloaded, err = config.Load(path)
	if err != nil {
		t.Fatalf("reload after reviewer update: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		if mdl := reloaded.Models[name]; mdl.Thinking != "adaptive" || mdl.Effort != "max" {
			t.Fatalf("persisted reviewer model %s = %+v, want adaptive/max", name, mdl)
		}
	}

	// An invalid level is still rejected.
	if _, err := srv.SetThinking(ctx, connect.NewRequest(&v1.SetThinkingRequest{
		Role: "coordinator", Level: "bogus",
	})); err == nil {
		t.Fatal("expected error for invalid thinking level")
	}
}

// TestSetWorkImplementationPersists covers the settings-overlay path: the
// resolved default is listed, valid changes persist, and invalid values map to
// InvalidArgument.
func TestSetWorkImplementationPersists(t *testing.T) {
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

	initial, err := srv.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{}))
	if err != nil {
		t.Fatalf("initial ListModels: %v", err)
	}
	if initial.Msg.WorkImplementation != "delegate" {
		t.Fatalf("initial work implementation = %q, want delegate", initial.Msg.WorkImplementation)
	}
	if _, err := srv.SetWorkImplementation(ctx, connect.NewRequest(&v1.SetWorkImplementationRequest{
		Implementation: "direct",
	})); err != nil {
		t.Fatalf("SetWorkImplementation: %v", err)
	}
	list, err := srv.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{}))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if list.Msg.WorkImplementation != "direct" {
		t.Fatalf("work implementation = %q, want direct", list.Msg.WorkImplementation)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Work.Implementation != "direct" {
		t.Fatalf("persisted work implementation = %q, want direct", reloaded.Work.Implementation)
	}

	if _, err := srv.SetWorkImplementation(ctx, connect.NewRequest(&v1.SetWorkImplementationRequest{
		Implementation: "bogus",
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid implementation code = %v, want InvalidArgument", connect.CodeOf(err))
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
	startupDir := t.TempDir()
	srv := New(session.NewManager(reg, startupDir))
	ctx := context.Background()

	dir := t.TempDir()
	add, err := srv.AddProject(ctx, connect.NewRequest(&v1.AddProjectRequest{Path: dir, Name: "demo"}))
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if add.Msg.Project.GetName() != "demo" || add.Msg.Project.GetPath() != dir {
		t.Fatalf("AddProject = %+v, want name=demo path=%s", add.Msg.Project, dir)
	}
	if add.Msg.Project.NeedsOnboarding == nil || !add.Msg.Project.GetNeedsOnboarding() {
		t.Fatalf("new empty project's needs_onboarding = %v, want explicit true", add.Msg.Project.NeedsOnboarding)
	}

	list, err := srv.ListProjects(ctx, connect.NewRequest(&v1.ListProjectsRequest{}))
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list.Msg.Projects) != 2 || list.Msg.Projects[1].GetName() != "demo" {
		t.Fatalf("ListProjects = %+v, want startup project + demo", list.Msg.Projects)
	}
	if list.Msg.Projects[1].NeedsOnboarding == nil || !list.Msg.Projects[1].GetNeedsOnboarding() {
		t.Fatalf("listed empty project's needs_onboarding = %v, want explicit true", list.Msg.Projects[1].NeedsOnboarding)
	}

	if err := os.WriteFile(filepath.Join(dir, "spec.md"), []byte("# Spec\n\nSubstantive design.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	onboarded, err := srv.ListProjects(ctx, connect.NewRequest(&v1.ListProjectsRequest{}))
	if err != nil {
		t.Fatalf("ListProjects after onboarding: %v", err)
	}
	if p := onboarded.Msg.Projects[1]; p.NeedsOnboarding == nil || p.GetNeedsOnboarding() {
		t.Fatalf("onboarded project's needs_onboarding = %v, want explicit false", p.NeedsOnboarding)
	}

	if _, err := srv.RemoveProject(ctx, connect.NewRequest(&v1.RemoveProjectRequest{Name: "demo"})); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	list2, _ := srv.ListProjects(ctx, connect.NewRequest(&v1.ListProjectsRequest{}))
	if len(list2.Msg.Projects) != 1 || list2.Msg.Projects[0].GetPath() != startupDir {
		t.Fatalf("ListProjects after remove = %+v, want startup project", list2.Msg.Projects)
	}

	// RenameProject: re-register, rename, and confirm the new name resolves to
	// the same path while the old one is gone.
	if _, err := srv.AddProject(ctx, connect.NewRequest(&v1.AddProjectRequest{Path: dir, Name: "demo"})); err != nil {
		t.Fatalf("re-AddProject: %v", err)
	}
	renamed, err := srv.RenameProject(ctx, connect.NewRequest(&v1.RenameProjectRequest{Name: "demo", NewName: "demo2"}))
	if err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	if renamed.Msg.Project.GetName() != "demo2" || renamed.Msg.Project.GetPath() != dir {
		t.Fatalf("RenameProject = %+v, want name=demo2 path=%s", renamed.Msg.Project, dir)
	}
	// Unknown name → NotFound; collision → AlreadyExists.
	if _, err := srv.RenameProject(ctx, connect.NewRequest(&v1.RenameProjectRequest{Name: "demo", NewName: "x"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("rename unknown: code = %v, want NotFound", connect.CodeOf(err))
	}
	startupName := list2.Msg.Projects[0].GetName()
	if _, err := srv.RenameProject(ctx, connect.NewRequest(&v1.RenameProjectRequest{Name: "demo2", NewName: startupName})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("rename collision: code = %v, want AlreadyExists", connect.CodeOf(err))
	}
	if _, err := srv.RemoveProject(ctx, connect.NewRequest(&v1.RemoveProjectRequest{Name: "demo2"})); err != nil {
		t.Fatalf("RemoveProject demo2: %v", err)
	}

	// AddProject without a path is an InvalidArgument error.
	if _, err := srv.AddProject(ctx, connect.NewRequest(&v1.AddProjectRequest{})); err == nil {
		t.Fatal("AddProject with empty path: expected error")
	} else if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
}

func TestListProjectsIncludesGitStatus(t *testing.T) {
	reg := config.NewRegistry(&config.Config{})
	nonRepo := t.TempDir()
	mgr := session.NewManager(reg, nonRepo)
	defer mgr.ReclaimAll()

	gitDir := t.TempDir()
	if _, err := git.Open(gitDir); err != nil {
		t.Fatalf("initialize git project: %v", err)
	}
	if _, err := mgr.AddProject(gitDir, "git-project"); err != nil {
		t.Fatalf("add git project: %v", err)
	}

	resp, err := New(mgr).ListProjects(context.Background(), connect.NewRequest(&v1.ListProjectsRequest{}))
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(resp.Msg.Projects) != 2 {
		t.Fatalf("projects = %+v, want two", resp.Msg.Projects)
	}
	for _, p := range resp.Msg.Projects {
		switch p.Name {
		case "git-project":
			if p.Git == nil || p.Git.Branch == "" || p.Git.LastFetchUnix != 0 {
				t.Fatalf("git project status = %+v, want local status before first fetch", p.Git)
			}
		default:
			if p.Git != nil {
				t.Fatalf("non-repository project status = %+v, want nil", p.Git)
			}
		}
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
	if _, err := store.Complete(a.ID, "Finished first task.", "feat: first task"); err != nil {
		t.Fatalf("Complete first: %v", err)
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
	if second.GetId() != b.ID || !second.GetReady() || len(second.GetBlockedBy()) != 0 {
		t.Fatalf("second task = %+v, want ready after completed dependency", second)
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
	if len(td.GetBlockedBy()) != 0 || !td.GetReady() {
		t.Fatalf("GetTask blocked_by = %v, want ready", td.GetBlockedBy())
	}
	if td.GetPath() == "" {
		t.Fatal("GetTask path empty, want the task file path for local editor gating")
	}
	completed, err := srv.GetTask(ctx, connect.NewRequest(&v1.GetTaskRequest{Id: a.ID}))
	if err != nil {
		t.Fatalf("GetTask completed: %v", err)
	}
	if body := completed.Msg.Task.GetBody(); !strings.Contains(body, "Finished first task.") || !strings.Contains(body, "Commit: feat: first task") {
		t.Fatalf("completed task lookup lost compact detail:\n%s", body)
	}

	if _, err := srv.GetTask(ctx, connect.NewRequest(&v1.GetTaskRequest{Id: "9999"})); err == nil {
		t.Fatal("GetTask with bogus id: expected error")
	} else if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound", got)
	}
}

// TestCreateTask exercises the backlog capture RPC (task 0143): a valid request
// assigns an id, scaffolds the body with the canonical headers, and the new task
// shows up in ListBacklog; blank title and out-of-range priority are rejected.
func TestCreateTask(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	ws := t.TempDir()
	srv := New(session.NewManager(reg, ws))
	ctx := context.Background()

	resp, err := srv.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		Title: "Wire up the widget", Body: "Do the thing.", Priority: 2,
	}))
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	td := resp.Msg.Task
	if td.GetId() == "" {
		t.Fatal("CreateTask: no id assigned")
	}
	if td.GetPriority() != 2 || td.GetTitle() != "Wire up the widget" {
		t.Fatalf("CreateTask detail = %+v, want title+priority set", td)
	}
	for _, want := range []string{"## Description", "## Acceptance criteria", "## Work log"} {
		if !strings.Contains(td.GetBody(), want) {
			t.Fatalf("body missing %q:\n%s", want, td.GetBody())
		}
	}

	// Default priority: 0 => 3.
	def, err := srv.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{Title: "Defaults"}))
	if err != nil {
		t.Fatalf("CreateTask default priority: %v", err)
	}
	if def.Msg.Task.GetPriority() != 3 {
		t.Fatalf("default priority = %d, want 3", def.Msg.Task.GetPriority())
	}

	// Both created tasks appear in the backlog.
	list, err := srv.ListBacklog(ctx, connect.NewRequest(&v1.ListBacklogRequest{}))
	if err != nil {
		t.Fatalf("ListBacklog: %v", err)
	}
	if len(list.Msg.Tasks) != 2 {
		t.Fatalf("ListBacklog = %d tasks, want 2", len(list.Msg.Tasks))
	}

	// Blank title and out-of-range priority are InvalidArgument.
	if _, err := srv.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{Title: "  "})); err == nil {
		t.Fatal("CreateTask blank title: expected error")
	} else if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
	if _, err := srv.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{Title: "x", Priority: 9})); err == nil {
		t.Fatal("CreateTask bad priority: expected error")
	} else if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
}

// TestUpdateTask exercises the backlog grooming RPC: all editable task fields
// persist to the task file; invalid inputs are rejected; an unknown id is
// NotFound; and a no-field "refresh" re-reads the task without altering it.
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

	// Title, body, dependencies, and spec references can be replaced in one edit.
	title := "Edited task"
	body := "## Description\n\nEdited from iOS.\n"
	resp, err = srv.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{
		Id: a.ID, Title: &title, Body: &body,
		DependsOn: []string{" 0099 ", "0099", ""}, ReplaceDependsOn: boolPtr(true),
		SpecRefs: []string{" §6.2 Backlog ", "§6.2 Backlog"}, ReplaceSpecRefs: boolPtr(true),
	}))
	if err != nil {
		t.Fatalf("UpdateTask full edit: %v", err)
	}
	if task := resp.Msg.Task; task.GetTitle() != title || task.GetBody() != body ||
		!reflect.DeepEqual(task.GetDependsOn(), []string{"0099"}) ||
		!reflect.DeepEqual(task.GetSpecRefs(), []string{"§6.2 Backlog"}) {
		t.Fatalf("UpdateTask full edit = %+v", task)
	}
	got, err = store.Get(a.ID)
	if err != nil {
		t.Fatalf("Get after full edit: %v", err)
	}
	if got.Title != title || got.Body != body || !reflect.DeepEqual(got.DependsOn, []string{"0099"}) || !reflect.DeepEqual(got.SpecRefs, []string{"§6.2 Backlog"}) {
		t.Fatalf("persisted full edit = %+v", got)
	}

	// Replacement flags permit explicitly clearing both lists.
	resp, err = srv.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{
		Id: a.ID, ReplaceDependsOn: boolPtr(true), ReplaceSpecRefs: boolPtr(true),
	}))
	if err != nil || len(resp.Msg.Task.GetDependsOn()) != 0 || len(resp.Msg.Task.GetSpecRefs()) != 0 {
		t.Fatalf("UpdateTask clear lists = task:%+v err:%v", resp.Msg.Task, err)
	}

	// Invalid status, priority, list intent, and self-dependency are rejected.
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
	if _, err := srv.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{Id: a.ID, DependsOn: []string{"0001"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("depends_on without replace flag code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	if _, err := srv.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{Id: a.ID, DependsOn: []string{a.ID}, ReplaceDependsOn: boolPtr(true)})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("self dependency code = %v, want InvalidArgument", connect.CodeOf(err))
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

// TestGetMemory exercises the read-only project-memory viewer surface: a
// missing memory.md is empty content (not an error), an existing file is
// returned verbatim with its absolute path, and an unknown project is
// InvalidArgument.
func TestGetMemory(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	ws := t.TempDir()
	srv := New(session.NewManager(reg, ws))
	ctx := context.Background()

	got, err := srv.GetMemory(ctx, connect.NewRequest(&v1.GetMemoryRequest{}))
	if err != nil {
		t.Fatalf("GetMemory (no file): %v", err)
	}
	if got.Msg.GetContent() != "" || got.Msg.GetPath() == "" {
		t.Fatalf("GetMemory (no file) = %+v, want empty content + path set", got.Msg)
	}

	const body = "# Project memory\n\n## Lessons learned\n- 2025-01-01: a note\n"
	if err := os.WriteFile(filepath.Join(ws, "memory.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write memory.md: %v", err)
	}
	got, err = srv.GetMemory(ctx, connect.NewRequest(&v1.GetMemoryRequest{}))
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Msg.GetContent() != body {
		t.Fatalf("GetMemory content = %q, want %q", got.Msg.GetContent(), body)
	}
	if got.Msg.GetPath() != filepath.Join(ws, "memory.md") {
		t.Fatalf("GetMemory path = %q, want %q", got.Msg.GetPath(), filepath.Join(ws, "memory.md"))
	}

	if _, err := srv.GetMemory(ctx, connect.NewRequest(&v1.GetMemoryRequest{Project: "nope"})); err == nil {
		t.Fatal("GetMemory with bogus project: expected error")
	} else if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", code)
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
			Name: "gpt", Backend: "openai", BaseUrl: "https://oai", Model: "gpt-4o", KeyEnv: "OPENAI_API_KEY", Disabled: boolPtr(true),
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
			if !m.Disabled {
				t.Fatal("disabled model was listed as enabled")
			}
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
	if mc.Backend != "openai" || mc.Model != "gpt-4o" || mc.KeyEnv != "OPENAI_API_KEY" || !mc.GetDisabled() {
		t.Fatalf("GetModelConfig = %+v", mc)
	}

	// Model context budgets are configured in TOML rather than this connection
	// form; editing through the RPC must preserve them too.
	configured, _ := reg.GetModel("gpt")
	configured.ContextWindow, configured.ContextSafeFraction = 123456, .7
	if err := reg.UpsertModel("gpt", configured, false); err != nil {
		t.Fatal(err)
	}

	// A version-skewed client that omits the optional availability field while
	// editing another property must not accidentally re-enable the model.
	if _, err := srv.UpsertModel(ctx, connect.NewRequest(&v1.UpsertModelRequest{Model: &v1.ModelConfig{
		Name: "gpt", Backend: "openai", BaseUrl: "https://oai", Model: "gpt-4.1", KeyEnv: "OPENAI_API_KEY",
	}})); err != nil {
		t.Fatalf("legacy UpsertModel: %v", err)
	}
	got, err = srv.GetModelConfig(ctx, connect.NewRequest(&v1.GetModelConfigRequest{Name: "gpt"}))
	if err != nil || !got.Msg.Model.GetDisabled() || got.Msg.Model.Model != "gpt-4.1" {
		t.Fatalf("legacy upsert did not preserve disabled state: model=%+v err=%v", got.Msg.GetModel(), err)
	}
	if current, _ := reg.GetModel("gpt"); current.ContextWindow != 123456 || current.ContextSafeFraction != .7 {
		t.Fatalf("model edit discarded context budget: %+v", current)
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

func TestListSessionHistoryMapsModelUsage(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	ws := t.TempDir()
	logPath := filepath.Join(ws, ".ycc", "sessions", "sess_summary", "events.jsonl")
	lg, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	lg.Record("coordinator", event.ModelTurn, map[string]any{
		"model_name": "claude", "context_tokens_est": 12_345, "usage": event.Usage{Total: 300},
	})
	lg.Record("reviewer:gpt", event.ModelTurn, map[string]any{
		"model_name": "gpt", "context_tokens_est": 777, "usage": event.Usage{Total: 200},
	})
	if err := lg.Close(); err != nil {
		t.Fatalf("Close log: %v", err)
	}

	srv := New(session.NewManager(reg, ws))
	resp, err := srv.ListSessionHistory(context.Background(), connect.NewRequest(&v1.ListSessionHistoryRequest{}))
	if err != nil || len(resp.Msg.Sessions) != 1 {
		t.Fatalf("ListSessionHistory = %+v, %v", resp, err)
	}
	summary := resp.Msg.Sessions[0]
	if summary.TotalTokens != 500 || len(summary.ModelUsage) != 2 ||
		summary.ModelUsage[0].Model != "claude" || summary.ModelUsage[0].Tokens != 300 ||
		summary.ModelUsage[1].Model != "gpt" || summary.ModelUsage[1].Tokens != 200 {
		t.Fatalf("wire summary usage = %+v, total %d", summary.ModelUsage, summary.TotalTokens)
	}
	// The subagent turn is newer but must not replace the coordinator's
	// context readout on the wire.
	if summary.ContextTokens != 12_345 {
		t.Fatalf("wire context tokens = %d, want 12345", summary.ContextTokens)
	}
}

func TestGetUsageFiltersByTask(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	ws := t.TempDir()
	logPath := filepath.Join(ws, ".ycc", "sessions", "sess_usage", "events.jsonl")
	lg, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	lg.Record("coordinator", event.TaskFocus, map[string]any{"task": "0093"})
	lg.Record("coordinator", event.ModelTurn, map[string]any{
		"model_name": "a", "usage": event.Usage{Input: 90, Output: 10, Total: 100},
	})
	lg.Record("implementer", event.TaskFocus, map[string]any{"task": "0094"})
	lg.Record("implementer", event.ModelTurn, map[string]any{
		"model_name": "a", "usage": event.Usage{Input: 800, Output: 100, Total: 900},
	})
	if err := lg.Close(); err != nil {
		t.Fatalf("Close log: %v", err)
	}

	srv := New(session.NewManager(reg, ws))
	resp, err := srv.GetUsage(context.Background(), connect.NewRequest(&v1.GetUsageRequest{
		Task: " 0093 ", GroupBy: []string{"agent"},
	}))
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	if len(resp.Msg.Rows) != 1 || resp.Msg.Rows[0].Agent != "coordinator" {
		t.Fatalf("rows = %+v, want only task 0093 coordinator usage", resp.Msg.Rows)
	}
	if resp.Msg.Rows[0].Total != 100 || resp.Msg.Total.GetTotal() != 100 {
		t.Fatalf("row/total tokens = %d/%d, want 100/100", resp.Msg.Rows[0].Total, resp.Msg.Total.GetTotal())
	}
}

// GetBudget returns the configured spend-guard caps (task 0137, spec §20.6).
func TestGetBudgetReturnsConfiguredCaps(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
		Budget: config.Budget{SessionCost: 5.0, SessionTokens: 2_000_000, LoopCost: 20.0, LoopTokens: 8_000_000},
	})
	srv := New(session.NewManager(reg, t.TempDir()))
	resp, err := srv.GetBudget(context.Background(), connect.NewRequest(&v1.GetBudgetRequest{}))
	if err != nil {
		t.Fatalf("GetBudget: %v", err)
	}
	m := resp.Msg
	if m.SessionCost != 5.0 || m.SessionTokens != 2_000_000 || m.LoopCost != 20.0 || m.LoopTokens != 8_000_000 {
		t.Fatalf("GetBudget = %+v, want the configured caps", m)
	}
}

// An absent [budget] block yields all-zero (unlimited) caps.
func TestGetBudgetDefaultUnlimited(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	srv := New(session.NewManager(reg, t.TempDir()))
	resp, err := srv.GetBudget(context.Background(), connect.NewRequest(&v1.GetBudgetRequest{}))
	if err != nil {
		t.Fatalf("GetBudget: %v", err)
	}
	m := resp.Msg
	if m.SessionCost != 0 || m.SessionTokens != 0 || m.LoopCost != 0 || m.LoopTokens != 0 {
		t.Fatalf("GetBudget default = %+v, want all zero", m)
	}
}

// Review-tier RPCs (task 0297, spec §13.1/§18.2): list the effective tiers,
// upsert/remove configured ones, and set the default — all persisted to
// ycc.toml, with validation errors surfaced as connect errors.
func TestReviewTierRPCs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := config.Save(path, &config.Config{
		Models: map[string]config.Model{
			"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"},
			"b": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-b"},
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

	// The builtins come back with the effective default.
	list, err := srv.ListReviewTiers(ctx, connect.NewRequest(&v1.ListReviewTiersRequest{}))
	if err != nil {
		t.Fatalf("ListReviewTiers: %v", err)
	}
	if list.Msg.DefaultTier != "standard" || len(list.Msg.Tiers) != 3 {
		t.Fatalf("initial tiers = %v default=%q", list.Msg.Tiers, list.Msg.DefaultTier)
	}
	for _, tier := range list.Msg.Tiers {
		if !tier.Builtin || tier.Configured {
			t.Fatalf("builtin tier flags wrong: %+v", tier)
		}
		switch tier.Name {
		case "self-review", "standard", "comprehensive":
		default:
			t.Fatalf("non-canonical builtin returned by RPC: %+v", tier)
		}
	}

	// Legacy mutation input is accepted but listed and persisted canonically.
	if _, err := srv.UpsertReviewTier(ctx, connect.NewRequest(&v1.UpsertReviewTierRequest{
		Tier: &v1.ReviewTierInfo{Name: "simple", Strategy: "coordinator"},
	})); err != nil {
		t.Fatalf("legacy UpsertReviewTier: %v", err)
	}
	list, err = srv.ListReviewTiers(ctx, connect.NewRequest(&v1.ListReviewTiersRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var selfReview *v1.ReviewTierInfo
	for _, tier := range list.Msg.Tiers {
		if tier.Name == "self-review" {
			selfReview = tier
		}
		if tier.Name == "simple" {
			t.Fatalf("legacy alias leaked into RPC listing: %+v", tier)
		}
	}
	if selfReview == nil || !selfReview.Configured {
		t.Fatalf("canonical self-review override missing: %+v", selfReview)
	}
	if _, err := srv.RemoveReviewTier(ctx, connect.NewRequest(&v1.RemoveReviewTierRequest{Name: "simple"})); err != nil {
		t.Fatalf("legacy RemoveReviewTier: %v", err)
	}

	// Upsert a custom tier with per-reviewer focus + thinking, set it default.
	if _, err := srv.UpsertReviewTier(ctx, connect.NewRequest(&v1.UpsertReviewTierRequest{
		Tier: &v1.ReviewTierInfo{
			Name:        "deep",
			Description: "risky changes",
			Prompt:      "Cite file:line.",
			Reviewers: []*v1.ReviewerSlot{
				{Name: "perf", Model: "b", Prompt: "Focus on perf.", Thinking: "max"},
				{Model: "a"},
			},
		},
	})); err != nil {
		t.Fatalf("UpsertReviewTier: %v", err)
	}
	if _, err := srv.SetReviewDefault(ctx, connect.NewRequest(&v1.SetReviewDefaultRequest{Name: "deep"})); err != nil {
		t.Fatalf("SetReviewDefault: %v", err)
	}
	list, err = srv.ListReviewTiers(ctx, connect.NewRequest(&v1.ListReviewTiersRequest{}))
	if err != nil {
		t.Fatalf("ListReviewTiers: %v", err)
	}
	if list.Msg.DefaultTier != "deep" {
		t.Fatalf("default = %q, want deep", list.Msg.DefaultTier)
	}
	var deep *v1.ReviewTierInfo
	for _, tier := range list.Msg.Tiers {
		if tier.Name == "deep" {
			deep = tier
		}
	}
	if deep == nil || deep.Builtin || !deep.Configured || len(deep.Reviewers) != 2 {
		t.Fatalf("deep = %+v", deep)
	}
	if rv := deep.Reviewers[0]; rv.Name != "perf" || rv.Model != "b" || rv.Thinking != "max" {
		t.Fatalf("deep reviewer[0] = %+v", rv)
	}

	// Persisted: a reload sees the tier and default.
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Reviews.Default != "deep" || len(reloaded.Reviews.Tiers["deep"].Reviewers) != 2 {
		t.Fatalf("persisted reviews = %+v", reloaded.Reviews)
	}

	// Validation errors surface as invalid_argument.
	if _, err := srv.UpsertReviewTier(ctx, connect.NewRequest(&v1.UpsertReviewTierRequest{
		Tier: &v1.ReviewTierInfo{Name: "bad", Models: []string{"missing"}},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad upsert error = %v, want invalid_argument", err)
	}
	if _, err := srv.SetReviewDefault(ctx, connect.NewRequest(&v1.SetReviewDefaultRequest{Name: "nope"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad default error = %v, want invalid_argument", err)
	}

	// The default custom tier can't be removed until the default moves.
	if _, err := srv.RemoveReviewTier(ctx, connect.NewRequest(&v1.RemoveReviewTierRequest{Name: "deep"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("remove default tier error = %v, want failed_precondition", err)
	}
	if _, err := srv.SetReviewDefault(ctx, connect.NewRequest(&v1.SetReviewDefaultRequest{})); err != nil {
		t.Fatalf("clear default: %v", err)
	}
	if _, err := srv.RemoveReviewTier(ctx, connect.NewRequest(&v1.RemoveReviewTierRequest{Name: "deep"})); err != nil {
		t.Fatalf("RemoveReviewTier: %v", err)
	}
	list, _ = srv.ListReviewTiers(ctx, connect.NewRequest(&v1.ListReviewTiersRequest{}))
	if len(list.Msg.Tiers) != 3 || list.Msg.DefaultTier != "standard" {
		t.Fatalf("after removal tiers=%d default=%q", len(list.Msg.Tiers), list.Msg.DefaultTier)
	}
}
