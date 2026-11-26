package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/tools"
)

func depsFor(t *testing.T) *Deps {
	t.Helper()
	ws := t.TempDir()
	return &Deps{
		Workspace: ws,
		Docs:      docs.NewStore(ws),
		Emitter:   event.NewEmitter(event.NewStdoutRecorder(io.Discard), "coordinator"),
		Asker:     noopAsker{},
	}
}

func TestBuildModeToolsets(t *testing.T) {
	d := depsFor(t)
	d.AgentModels = func() []ModelCatalogEntry { return []ModelCatalogEntry{{Name: "fast"}, {Name: "smart"}} }
	d.ResolveAgent = func(string) (AgentSpec, error) { return AgentSpec{}, nil }
	// pm exposes planning/docs/backlog tools and switch_to_work, but NO
	// implementation tools (no spawn_implementer / commit).
	pmReg, _ := BuildMode("pm", d, false)
	for _, want := range []string{"Read", "Search", "Edit", "Write", "Bash", "list_backlog", "get_task", "create_task", "update_task", "propose_plan", "switch_to_work", "ask_user", "finish"} {
		if !hasTool(pmReg, want) {
			t.Fatalf("pm mode missing %s", want)
		}
	}
	for _, gone := range []string{"spawn_implementer", "spawn_reviewers", "commit"} {
		if hasTool(pmReg, gone) {
			t.Fatalf("pm mode should not have %s (no implementation)", gone)
		}
	}
	// chat is the freeform assistant: file tools + read AND write backlog tools
	// (create_task/update_task) and a commit tool for finished work, but no
	// review pipeline or switch_to_work.
	chatReg, _ := BuildMode("chat", d, false)
	for _, want := range []string{"Read", "Search", "Edit", "Write", "Bash", "list_backlog", "get_task", "create_task", "update_task", "ask_user", "spawn_agent", "send_to_agent", "commit"} {
		if !hasTool(chatReg, want) {
			t.Fatalf("chat mode missing %s", want)
		}
	}
	for _, gone := range []string{"spawn_implementer", "spawn_reviewers", "switch_to_work"} {
		if hasTool(chatReg, gone) {
			t.Fatalf("chat mode should not have %s", gone)
		}
	}
	// integrate is a narrowly scoped worker: editing/git-via-Bash plus only the
	// success and blocked control tools. In particular it cannot delegate, manage
	// backlog, commit through the daemon, or finish without requesting integration.
	extraWriteRoot := t.TempDir()
	d.WriteRoots = []string{extraWriteRoot}
	integrateReg, _ := BuildMode("integrate", d, true)
	for _, want := range []string{"Read", "Search", "Edit", "Write", "Bash", "request_integration", "report_blocked"} {
		if !hasTool(integrateReg, want) {
			t.Fatalf("integrate mode missing %s", want)
		}
	}
	for _, gone := range []string{"spawn_implementer", "spawn_reviewers", "list_backlog", "commit", "finish"} {
		if hasTool(integrateReg, gone) {
			t.Fatalf("integrate mode should not have %s", gone)
		}
	}
	outside := filepath.Join(extraWriteRoot, "must-not-write.txt")
	writeResult := integrateReg.Dispatch(context.Background(), gollama.ToolCall{
		ID: "outside", Type: "function",
		Function: gollama.ToolCallFunction{Name: "Write", Arguments: `{"file_path":"` + outside + `","content":"no"}`},
	})
	if !writeResult.IsError {
		t.Fatalf("integrate mode inherited an outside write root: %s", writeResult.Content)
	}
	// The removed authoring modes no longer build.
	for _, mode := range []string{"spec", "backlog", "feature", "bug"} {
		reg, _ := BuildMode(mode, d, false)
		// Unknown modes fall through to the work coordinator, including review
		// and commit, rather than acting as distinct authoring modes.
		if !hasTool(reg, "spawn_reviewers") || !hasTool(reg, "commit") {
			t.Fatalf("removed mode %q should fall through to work coordinator", mode)
		}
	}
}

type stubLeases struct{}

func (stubLeases) Acquire(name, _ string, _ time.Duration, _ string) (string, error) {
	return "Acquired " + name + ".", nil
}
func (stubLeases) Release(name string) (string, error) { return "Released " + name + ".", nil }

// With the notice board, leases and a job registry all available, pm gets
// none of the lease, notice or background job tools (it keeps read-only
// list_sessions and a foreground-only Bash), and watch drops only the lease
// tools. Chat and work keep everything.
func TestModeCoordinationAndJobTools(t *testing.T) {
	d := depsFor(t)
	d.Board = &fakeBoard{}
	d.Leases = stubLeases{}
	d.Jobs = jobs.NewRegistry()
	d.ListSessions = func(string) (string, error) { return "", nil }
	d.AgentModels = func() []ModelCatalogEntry { return []ModelCatalogEntry{{Name: "fast"}} }
	d.ResolveAgent = func(string) (AgentSpec, error) { return AgentSpec{}, nil }
	jobTools := []string{"job_output", "wait", "kill_job", "list_jobs"}
	cases := []struct {
		mode       string
		want, gone []string
	}{
		{"pm", []string{"list_sessions", "Bash"}, append([]string{"acquire_lease", "release_lease", "notice"}, jobTools...)},
		{"watch", []string{"list_sessions", "notice", "job_output", "wait", "kill_job", "spawn_agent"}, []string{"acquire_lease", "release_lease"}},
		{"chat", []string{"list_sessions", "notice", "acquire_lease", "release_lease", "job_output", "wait", "kill_job"}, nil},
		{"work", []string{"list_sessions", "notice", "acquire_lease", "release_lease", "job_output", "wait", "kill_job"}, nil},
	}
	for _, c := range cases {
		reg, system := BuildMode(c.mode, d, false)
		for _, name := range c.want {
			if !hasTool(reg, name) {
				t.Errorf("%s mode missing %s", c.mode, name)
			}
		}
		for _, name := range c.gone {
			if hasTool(reg, name) {
				t.Errorf("%s mode offers %s", c.mode, name)
			}
		}
		background := false
		for _, def := range reg.APIDefs() {
			if def.Function != nil && def.Function.Name == "Bash" {
				background = strings.Contains(fmt.Sprint(def.Function.Parameters), "run_in_background")
			}
		}
		if background != (c.mode != "pm") {
			t.Errorf("%s mode Bash run_in_background = %v", c.mode, background)
		}
		if board := strings.Contains(system, "NOTICE BOARD"); board != (c.mode != "pm") {
			t.Errorf("%s mode board guidance = %v", c.mode, board)
		}
	}
	pmReg, _ := BuildMode("pm", d, false)
	res := pmReg.Dispatch(context.Background(), gollama.ToolCall{ID: "bg", Type: "function",
		Function: gollama.ToolCallFunction{Name: "Bash", Arguments: `{"command":"true","run_in_background":true}`}})
	if !res.IsError || len(d.Jobs.List("", true)) != 0 {
		t.Fatalf("pm Bash started a background job: %s", res.Content)
	}
}

func TestExplicitNoGenericModelsOmitsSpawnTools(t *testing.T) {
	for _, mode := range []string{"chat", "watch", "work"} {
		t.Run(mode, func(t *testing.T) {
			d := depsFor(t)
			d.DisableGenericAgents = true
			reg, _ := BuildMode(mode, d, false)
			for _, name := range []string{"spawn_agent", "send_to_agent"} {
				if hasTool(reg, name) {
					t.Errorf("%s mode mounted %s despite explicit empty policy", mode, name)
				}
			}
		})
	}
}

// In the "direct" implementation strategy (spec §10) the work coordinator
// implements changes itself: the implementer spawn/revise tools are dropped, but
// it keeps the editing tools and the review pipeline, and gets the direct prompt.
func TestWorkCoordinatorDirectImplementation(t *testing.T) {
	d := depsFor(t)
	// The zero-value strategy is direct.
	reg, _ := BuildMode("work", d, false)
	// No implementer subagent tools in direct mode.
	for _, gone := range []string{"spawn_implementer", "send_to_implementer"} {
		if hasTool(reg, gone) {
			t.Fatalf("direct work coordinator should not have %s", gone)
		}
	}
	// It still edits the workspace itself and reviews.
	for _, want := range []string{"Read", "Search", "Write", "Edit", "Bash", "spawn_reviewers", "re_review", "commit", "list_backlog", "create_task", "propose_plan"} {
		if !hasTool(reg, want) {
			t.Fatalf("direct work coordinator missing %s", want)
		}
	}
	// Explicit delegation keeps the implementer tools and independent review.
	d.WorkImplementation = "delegate"
	reg, _ = BuildMode("work", d, false)
	if !hasTool(reg, "spawn_implementer") || !hasTool(reg, "send_to_implementer") {
		t.Fatalf("delegated work coordinator must keep the implementer pipeline tools")
	}
}

func TestListBacklogUsesWorkLoopEligibility(t *testing.T) {
	d := depsFor(t)
	done, _ := d.Docs.Create("done dependency", "", 1, nil, nil)
	d.Docs.Update(done.ID, func(tk *docs.Task) { tk.Status = docs.StatusDone })
	d.Docs.Create("ready todo", "", 1, []string{done.ID}, nil)
	blocked, _ := d.Docs.Create("external gate", "", 1, []string{done.ID}, nil)
	d.Docs.Update(blocked.ID, func(tk *docs.Task) { tk.Status = docs.StatusBlocked })
	continuation, _ := d.Docs.Create("active continuation", "", 1, []string{done.ID}, nil)
	d.Docs.Update(continuation.ID, func(tk *docs.Task) { tk.Status = docs.StatusInProgress })
	d.Docs.Create("missing dependency", "", 1, []string{"9999"}, nil)

	res, _ := listBacklog(d).Call(context.Background(), map[string]any{})
	out := res.Content
	for _, want := range []string{
		"0002 [todo]", "[READY TO START]",
		"0003 [blocked]", "dependencies satisfied; explicit blocked status remains",
		"0004 [in_progress]", "[READY TO CONTINUE]",
		"0005 [todo]", "missing dependency 9999; create or restore it, then complete it",
		"Work-loop eligible: 0002 (ready to start), 0004 (ready to continue)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("list_backlog missing %q:\n%s", want, out)
		}
	}
	view := tools.ViewOf(res)
	if view == nil || view.Summary != "2 work-loop eligible task(s)" {
		t.Fatalf("structured view = %+v", view)
	}
	var structured strings.Builder
	for _, node := range view.Nodes {
		structured.WriteString(node.Label)
		for _, child := range node.Children {
			structured.WriteString(" " + child.Label + ": " + child.Detail)
		}
	}
	for _, want := range []string{"missing: 9999", "resolve the recorded blocker"} {
		if !strings.Contains(structured.String(), want) {
			t.Fatalf("structured backlog missing %q: %s", want, structured.String())
		}
	}
}

// A ready task another live session is working on must not look actionable to
// a work session, or it picks the task, declines to collide, and the loop spins.
func TestListBacklogHeldTaskNotActionable(t *testing.T) {
	d := depsFor(t)
	held, _ := d.Docs.Create("chat-owned audit", "", 1, nil, nil)
	d.Docs.Update(held.ID, func(tk *docs.Task) { tk.Status = docs.StatusInProgress })
	free, _ := d.Docs.Create("free work", "", 2, nil, nil)
	d.HeldTasks = func() map[string]string { return map[string]string{held.ID: "s_other"} }

	res, _ := listBacklog(d).Call(context.Background(), map[string]any{})
	for _, want := range []string{
		held.ID + " [in_progress]", "another live session (s_other) is working on it",
		"Work-loop eligible: " + free.ID + " (ready to start)\n",
	} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("list_backlog missing %q:\n%s", want, res.Content)
		}
	}
	got, _ := getTask(d).Call(context.Background(), map[string]any{"task_id": held.ID})
	if !strings.Contains(got.Content, "Work eligibility: [NOT ACTIONABLE: another live session (s_other)") {
		t.Fatalf("get_task eligibility:\n%s", got.Content)
	}
}

func TestListBacklogAllBlocked(t *testing.T) {
	d := depsFor(t)
	blocked, _ := d.Docs.Create("waiting for authorization", "", 1, nil, nil)
	d.Docs.Update(blocked.ID, func(tk *docs.Task) { tk.Status = docs.StatusBlocked })
	d.Docs.Create("waiting for missing work", "", 1, []string{"9999"}, nil)

	res, _ := listBacklog(d).Call(context.Background(), map[string]any{})
	if strings.Contains(res.Content, "READY TO") || !strings.Contains(res.Content, "no tasks are work-loop eligible") {
		t.Fatalf("all-blocked backlog reported actionable work:\n%s", res.Content)
	}
	view := tools.ViewOf(res)
	if view == nil || view.Status != "warn" || view.Summary != "0 work-loop eligible task(s)" {
		t.Fatalf("all-blocked structured view = %+v", view)
	}
}

// A "proposed" task is an idea awaiting the user's acceptance: create_task can
// file it directly, and list_backlog must never mark it READY (it stays out of
// the work pipeline's ready pool until promoted to todo).
func TestCreateTaskProposedNeverReady(t *testing.T) {
	d := depsFor(t)
	res, _ := createTask(d).Call(context.Background(), map[string]any{"title": "an idea", "status": "proposed"})
	if !strings.Contains(res.Content, "[proposed]") {
		t.Fatalf("create_task result should carry the proposed status: %q", res.Content)
	}
	got, err := d.Docs.Get("0001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != docs.StatusProposed {
		t.Fatalf("status = %q, want proposed", got.Status)
	}

	out, _ := listBacklog(d).Call(context.Background(), map[string]any{})
	if strings.Contains(out.Content, "[READY") {
		t.Fatalf("proposed task must not be READY:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "user acceptance required") || !strings.Contains(out.Content, "proposed task(s)") {
		t.Fatalf("expected proposed unblock requirement:\n%s", out.Content)
	}
	view := tools.ViewOf(out)
	if view == nil || len(view.Nodes) != 1 || len(view.Nodes[0].Children) != 2 ||
		!strings.Contains(view.Nodes[0].Children[1].Detail, "user acceptance required") {
		t.Fatalf("proposed structured eligibility = %+v", view)
	}

	// Default (no status) stays todo; a bogus status is rejected.
	res, _ = createTask(d).Call(context.Background(), map[string]any{"title": "real work"})
	if !strings.Contains(res.Content, "[todo]") {
		t.Fatalf("default create_task should be todo: %q", res.Content)
	}
	res, _ = createTask(d).Call(context.Background(), map[string]any{"title": "bad", "status": "done"})
	if !strings.Contains(res.Content, "invalid initial status") {
		t.Fatalf("expected invalid-status error: %q", res.Content)
	}
}

// Accepted work that is starting immediately can be created directly as
// in_progress, avoiding a second update_task tool call.
func TestCreateTaskInProgress(t *testing.T) {
	d := depsFor(t)
	tool := createTask(d)
	params := tool.Params.(gollama.ToolFunctionParams)
	statusSchema := params.Properties["status"].(map[string]any)
	if !slices.Contains(statusSchema["enum"].([]string), "in_progress") {
		t.Fatalf("create_task status enum = %v, want in_progress", statusSchema["enum"])
	}

	res, _ := tool.Call(context.Background(), map[string]any{"title": "active workstream", "status": "in_progress"})
	if res.IsError || !strings.Contains(res.Content, "[in_progress]") {
		t.Fatalf("create_task result should carry in_progress: %+v", res)
	}
	got, err := d.Docs.Get("0001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != docs.StatusInProgress {
		t.Fatalf("status = %q, want in_progress", got.Status)
	}
}

func TestSwitchToWorkSignalsModeChangeWithTask(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	res, err := switchToWork(d).Call(context.Background(), map[string]any{"task_id": "0021", "plan": "do the thing"})
	if err != nil {
		t.Fatalf("switch_to_work: %v", err)
	}
	if got := rec.focusTasks(); len(got) != 1 || got[0] != "0021" {
		t.Fatalf("focus events = %v, want [0021]", got)
	}
	ctrl := tools.ControlOf(res)
	if ctrl == nil || !ctrl.Stop || ctrl.Mode != "work" {
		t.Fatalf("switch_to_work control = %+v", ctrl)
	}
	// The carried prompt must name the specific task so work implements THAT task.
	if !strings.Contains(ctrl.Prompt, "0021") {
		t.Fatalf("switch_to_work prompt does not carry the target task: %q", ctrl.Prompt)
	}
}

func TestSwitchToWorkRequiresApproval(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	d.Asker = declineAsker{}
	res, err := switchToWork(d).Call(context.Background(), map[string]any{"task_id": "0021", "plan": "p"})
	if err != nil {
		t.Fatalf("switch_to_work: %v", err)
	}
	if ctrl := tools.ControlOf(res); ctrl != nil && ctrl.Mode == "work" {
		t.Fatal("switch_to_work transitioned despite the user declining approval")
	}
	if got := rec.focusTasks(); len(got) != 0 {
		t.Fatalf("declined hand-off emitted focus events %v", got)
	}
}

func TestCreateTask(t *testing.T) {
	d := depsFor(t)
	ctx := context.Background()
	r, err := createTask(d).Call(ctx, map[string]any{
		"title": "Wire the TUI", "description": "build it", "priority": float64(2),
		"depends_on": []any{"0003"},
	})
	if err != nil || r.IsError {
		t.Fatalf("create_task: %v %s", err, r.Content)
	}
	tasks, _ := d.Docs.List()
	if len(tasks) != 1 || tasks[0].Title != "Wire the TUI" || tasks[0].Priority != 2 {
		t.Fatalf("task not created correctly: %+v", tasks)
	}
}

func TestCreateTaskDispatchRejectsInvalidPriorityWithoutMutation(t *testing.T) {
	d := depsFor(t)
	reg := tools.New()
	reg.Add(createTask(d))
	res := reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{
		Name: "create_task", Arguments: `{"title":"bad priority","priority":0}`,
	}})
	if !res.IsError || !strings.Contains(res.Content, "priority") {
		t.Fatalf("result = %q (error=%v), want priority validation error", res.Content, res.IsError)
	}
	if tasks, err := d.Docs.List(); err != nil || len(tasks) != 0 {
		t.Fatalf("invalid create_task mutated backlog: tasks=%v err=%v", tasks, err)
	}
}

// Creating a task with status in_progress claims focus immediately so another
// live session sees it as held right away; todo and proposed do not claim focus.
func TestCreateTaskInProgressClaimsFocus(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	var claimed []string
	d.ClaimTask = func(id string, record func()) string {
		claimed = append(claimed, id)
		record()
		return ""
	}
	tool := createTask(d)
	for _, status := range []string{"todo", "proposed"} {
		res, err := tool.Call(context.Background(), map[string]any{"title": "task " + status, "status": status})
		if err != nil || res.IsError {
			t.Fatalf("create_task %s: %v %+v", status, err, res)
		}
	}
	if len(claimed) != 0 || len(rec.focusTasks()) != 0 {
		t.Fatalf("todo/proposed claimed focus: claimed=%v focus=%v", claimed, rec.focusTasks())
	}
	res, err := tool.Call(context.Background(), map[string]any{"title": "starting now", "status": "in_progress"})
	if err != nil || res.IsError {
		t.Fatalf("create_task in_progress: %v %+v", err, res)
	}
	if len(claimed) != 1 || claimed[0] != "0003" {
		t.Fatalf("claimed = %v, want [0003]", claimed)
	}
	if got := rec.focusTasks(); len(got) != 1 || got[0] != "0003" {
		t.Fatalf("focusTasks = %v, want [0003]", got)
	}
}

// Writing spec.md through an authoring mode's Write tool persists the file and
// emits a doc_updated event via the Workspace OnWrite hook.
func TestSpecEditEmitsDocUpdated(t *testing.T) {
	ws := t.TempDir()
	var buf bytes.Buffer
	d := &Deps{
		Workspace: ws,
		Docs:      docs.NewStore(ws),
		Emitter:   event.NewEmitter(event.NewStdoutRecorder(&buf), "coordinator"),
		Asker:     noopAsker{},
	}
	reg, _ := BuildMode("pm", d, false)

	res := reg.Dispatch(context.Background(), gollama.ToolCall{
		Function: gollama.ToolCallFunction{
			Name:      "Write",
			Arguments: `{"file_path":"spec.md","content":"# Spec\n\n## Goals\nship it\n"}`,
		},
	})
	if res.IsError {
		t.Fatalf("Write spec.md: %s", res.Content)
	}
	body, _ := d.Docs.ReadSpec()
	if !strings.Contains(body, "## Goals") || !strings.Contains(body, "ship it") {
		t.Fatalf("spec not written:\n%s", body)
	}
	if !strings.Contains(buf.String(), string(event.DocUpdated)) {
		t.Fatalf("expected a %s event, got events:\n%s", event.DocUpdated, buf.String())
	}
}

func hasTool(reg *tools.Registry, name string) bool {
	for _, def := range reg.APIDefs() {
		if def.Function != nil && def.Function.Name == name {
			return true
		}
	}
	return false
}

// declineAsker rejects every confirmation, simulating a user who declines (or no
// human being available in unattended execution).
type declineAsker struct{}

func (declineAsker) Ask(context.Context, string, []string) (string, error) { return "ok", nil }
func (declineAsker) AskMany(_ context.Context, qs []Question) ([]string, error) {
	out := make([]string, len(qs))
	for i := range qs {
		out[i] = "ok"
	}
	return out, nil
}
func (declineAsker) Confirm(context.Context, string) (bool, error) { return false, nil }

// captureRec records every event in memory so tests can assert on emission.
// Record is called concurrently (e.g. reviewer fan-out in runReviewers), so it
// must honor the event.Recorder contract and synchronize; direct reads of
// r.events are safe only after the recording call has returned.
type captureRec struct {
	mu     sync.Mutex
	events []event.Event
}

func (r *captureRec) Record(actor string, t event.Type, data map[string]any) event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev := event.Event{Seq: len(r.events) + 1, Actor: actor, Type: t, Data: data}
	r.events = append(r.events, ev)
	return ev
}

func (r *captureRec) focusTasks() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, ev := range r.events {
		if ev.Type == event.TaskFocus {
			out = append(out, ev.Data["task"].(string))
		}
	}
	return out
}

// The work coordinator records focus when it accepts a task (update_task→
// in_progress), and dedupes: re-focusing the same task is a no-op, focusing a new
// task emits again. Other status changes don't establish focus.
func TestUpdateTaskInProgressEmitsFocusWithDedupe(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	ctx := context.Background()

	a, err := d.Docs.Create("task a", "", 3, nil, nil)
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := d.Docs.Create("task b", "", 3, nil, nil)
	if err != nil {
		t.Fatalf("create b: %v", err)
	}

	call := func(id, status string) {
		if _, err := updateTask(d).Call(ctx, map[string]any{"task_id": id, "status": status}); err != nil {
			t.Fatalf("update_task %s %s: %v", id, status, err)
		}
	}
	call(a.ID, "in_progress") // focus a
	call(a.ID, "in_progress") // dedupe: still a, no new event
	call(a.ID, "in_review")   // non-pickup status: no focus
	call(b.ID, "in_progress") // focus b

	want := []string{a.ID, b.ID}
	got := rec.focusTasks()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("focus events = %v, want %v", got, want)
	}

	// The focus event carries the task's title (best-effort) so UIs can label
	// the focused task without a backlog lookup of their own.
	for _, ev := range rec.events {
		if ev.Type == event.TaskFocus && ev.Data["task"] == a.ID {
			if title, _ := ev.Data["title"].(string); title != "task a" {
				t.Fatalf("focus event title = %v, want %q", ev.Data["title"], "task a")
			}
		}
	}
}

// Without the notice board, a work coordinator with SetIntent gets the notice
// tool for its session notice alone; without SetIntent it gets no notice tool.
func TestWorkModeSessionNoticeWithoutBoard(t *testing.T) {
	d := depsFor(t)
	reg, system := BuildMode("work", d, true)
	if hasTool(reg, "notice") {
		t.Fatal("notice offered without a board or SetIntent")
	}
	var got string
	d.SetIntent = func(text string) (string, error) { got = text; return "Session notice set.", nil }
	reg, system = BuildMode("work", d, true)
	if !hasTool(reg, "notice") || strings.Contains(system, "NOTICE BOARD") {
		t.Fatalf("work tools with SetIntent: notice %v, board guidance %v", hasTool(reg, "notice"), strings.Contains(system, "NOTICE BOARD"))
	}
	for _, name := range []string{"set_intent", "post_notice", "clear_notice"} {
		if hasTool(reg, name) {
			t.Fatalf("work mode offers retired %s", name)
		}
	}
	for _, def := range reg.APIDefs() {
		if def.Function != nil && def.Function.Name == "notice" {
			if params := fmt.Sprint(def.Function.Parameters); strings.Contains(params, "expires_in") || strings.Contains(params, "clear_id") {
				t.Fatalf("board-off notice schema offers timed operations: %s", params)
			}
		}
	}
	call := func(args string) *gollama.ToolResult {
		return reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: "notice", Arguments: args}})
	}
	if res := call(`{"text":"editing internal/session"}`); res.IsError || got != "editing internal/session" {
		t.Fatalf("notice = %+v, recorded %q", res, got)
	}
	if res := call(`{}`); !res.IsError || got != "editing internal/session" {
		t.Fatalf("notice {} without board = %+v, recorded %q", res, got)
	}
	// Timed and clear operations report the board is off and change nothing.
	for _, args := range []string{`{"text":"deploying","expires_in":"1h"}`, `{"clear_id":"n1","reason":"done"}`,
		`{"text":"planned maintenance","expires_in":""}`, `{"clear_id":""}`,
		`{"text":"planned maintenance","expires_in":null}`, `{"text":"","clear_id":null}`,
		`{"text":"planned maintenance","expires_in":3600}`} {
		res := call(args)
		if res.IsError || !strings.Contains(res.Content, "notice board is off") || got != "editing internal/session" {
			t.Fatalf("notice %s without board = %+v, recorded %q", args, res, got)
		}
	}
}

// fakeBoard records notice tool calls.
type fakeBoard struct {
	status        string
	posts, clears []string
	err           error
}

func (b *fakeBoard) SetIntent(text string) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	b.status = text
	return "Session notice set.", nil
}

func (b *fakeBoard) Post(text, subject string, expiresIn time.Duration) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	b.posts = append(b.posts, fmt.Sprintf("%s|%s|%s", text, subject, expiresIn))
	return "Posted timed notice n1.", nil
}

func (b *fakeBoard) Clear(id, reason string) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	b.clears = append(b.clears, id+"|"+reason)
	return "Cleared notice " + id + ".", nil
}

// The unified notice tool sets, replaces and clears the session notice
// without expires_in, posts a timed notice with it, and clears a timed notice
// by clear_id and reason; mixed or misplaced arguments fail without effect.
func TestNoticeToolOperations(t *testing.T) {
	d := depsFor(t)
	board := &fakeBoard{}
	d.Board = board
	reg, system := BuildMode("work", d, true)
	if !hasTool(reg, "notice") || !strings.Contains(system, "NOTICE BOARD") {
		t.Fatal("work mode with a board lacks the notice tool or its guidance")
	}
	for _, name := range []string{"set_intent", "post_notice", "clear_notice"} {
		if hasTool(reg, name) {
			t.Fatalf("work mode offers retired %s", name)
		}
	}
	call := func(args string) *gollama.ToolResult {
		return reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: "notice", Arguments: args}})
	}
	for _, text := range []string{"editing internal/session", "now on the web client", ""} {
		if res := call(`{"text":"` + text + `"}`); res.IsError || board.status != text {
			t.Fatalf("session notice %q = %+v, status %q", text, res, board.status)
		}
	}
	if res := call(`{"text":"master tests red","expires_in":"2h","subject":"master tests"}`); res.IsError ||
		len(board.posts) != 1 || board.posts[0] != "master tests red|master tests|2h0m0s" {
		t.Fatalf("timed notice = %+v, posts %q", res, board.posts)
	}
	if res := call(`{"clear_id":"n1","reason":"green again"}`); res.IsError || len(board.clears) != 1 || board.clears[0] != "n1|green again" {
		t.Fatalf("clear = %+v, clears %q", res, board.clears)
	}
	for _, args := range []string{
		`{"clear_id":"n1","reason":"x","text":"also post"}`,
		`{"clear_id":"n1","reason":"x","expires_in":"1h"}`,
		`{"text":"x","reason":"why"}`,
		`{"text":"x","subject":"svc"}`,
		`{"text":"x","expires_in":"soon"}`,
		`{"text":"planned maintenance","expires_in":""}`,
		`{"text":"","expires_in":" "}`,
		`{}`,
		`{"clear_id":"n1","reason":"done","text":""}`,
		`{"clear_id":" ","reason":"done"}`,
		`{"text":"x","expires_in":3600}`,
		`{"text":7}`,
		`{"text":null}`,
		`{"clear_id":null,"reason":"done"}`,
	} {
		if res := call(args); !res.IsError {
			t.Fatalf("notice %s = %+v, want an error", args, res)
		}
	}
	if res := call(`{"text":"still here"}`); res.IsError || board.status != "still here" {
		t.Fatalf("session notice = %+v, status %q", res, board.status)
	}
	for _, args := range []string{
		`{"text":"planned maintenance","expires_in":""}`, `{}`, `{"clear_id":"n1","reason":"done","text":""}`,
	} {
		if res := call(args); !res.IsError {
			t.Fatalf("notice %s = %+v, want an error", args, res)
		}
	}
	if board.status != "still here" || len(board.posts) != 1 || len(board.clears) != 1 {
		t.Fatalf("rejected calls changed the board: %+v", board)
	}
	// Coordination turned off after the session was built: reported, not an error.
	board.err = ErrCoordinationDisabled
	if res := call(`{"text":"x","expires_in":"1h"}`); res.IsError || !strings.Contains(res.Content, "turned off") {
		t.Fatalf("timed notice with coordination off = %+v", res)
	}
}

// A claim of a task another live session holds fails, names the holder, and
// leaves the task and this session's focus unchanged.
func TestUpdateTaskRefusesTaskHeldByAnotherSession(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	held, err := d.Docs.Create("held elsewhere", "", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.ClaimTask = func(id string, record func()) string {
		if docs.NormalizeID(id) == held.ID {
			return "s_other"
		}
		record()
		return ""
	}
	res, _ := updateTask(d).Call(context.Background(), map[string]any{"task_id": held.ID, "status": "in_progress"})
	if !res.IsError || !strings.Contains(res.Content, "s_other") {
		t.Fatalf("update_task of a held task = %+v, want an error naming s_other", res)
	}
	if got, _ := d.Docs.Get(held.ID); got.Status != docs.StatusTodo {
		t.Fatalf("held task status = %s, want todo unchanged", got.Status)
	}
	if got := rec.focusTasks(); len(got) != 0 {
		t.Fatalf("refused claim recorded focus %v", got)
	}
	res, _ = spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": held.ID})
	if !res.IsError || !strings.Contains(res.Content, "s_other") {
		t.Fatalf("spawn_reviewers of a held task = %+v, want an error naming s_other", res)
	}
}

// A work session's file-tool edit of a task's backlog file makes that task its
// focus; when another session holds the task, the edit lands but its result
// names the holder and no focus is recorded. Other modes' edits record nothing.
func TestBacklogFileEditClaimsFocusInWorkMode(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	mine, err := d.Docs.Create("mine", "## Work log\n", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := d.Docs.Create("theirs", "## Work log\n", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.ClaimTask = func(id string, record func()) string {
		if id == theirs.ID {
			return "s_other"
		}
		record()
		return ""
	}
	edit := func(mode string, task *docs.Task, note string) *gollama.ToolResult {
		t.Helper()
		reg, _ := BuildMode(mode, d, true)
		args, _ := json.Marshal(map[string]string{"file_path": task.Path, "old_string": "## Work log\n", "new_string": "## Work log\n- " + note + "\n"})
		res := reg.Dispatch(context.Background(), gollama.ToolCall{Function: gollama.ToolCallFunction{Name: "Edit", Arguments: string(args)}})
		if res.IsError {
			t.Fatalf("Edit %s: %s", task.ID, res.Content)
		}
		return res
	}
	edit("pm", mine, "groomed")
	if got := rec.focusTasks(); len(got) != 0 {
		t.Fatalf("pm edit recorded focus %v", got)
	}
	if res := edit("work", mine, "worked"); strings.Contains(res.Content, "held by") {
		t.Fatalf("unheld edit reported a holder: %s", res.Content)
	}
	if got := rec.focusTasks(); len(got) != 1 || got[0] != mine.ID {
		t.Fatalf("focus after editing %s = %v", mine.ID, got)
	}
	updated := 0
	for _, ev := range rec.events {
		if ev.Type == event.DocUpdated && ev.Data["task"] == mine.ID {
			updated++
		}
	}
	if updated != 1 {
		t.Fatalf("work edit of %s recorded %d doc_updated events, want 1 (the pm edit records none)", mine.ID, updated)
	}
	res := edit("work", theirs, "collided")
	if !strings.Contains(res.Content, "held by live session s_other") {
		t.Fatalf("held edit result does not name the holder: %s", res.Content)
	}
	if got := rec.focusTasks(); len(got) != 1 {
		t.Fatalf("held edit recorded focus: %v", got)
	}
	if got, _ := d.Docs.Get(theirs.ID); !strings.Contains(got.Body, "collided") {
		t.Fatalf("held edit did not land: %q", got.Body)
	}
}

// remember is available to coordinator-level agents (chat, pm, work) but NOT to
// the implementer or reviewers.
func TestRememberRegisteredForCoordinatorRoles(t *testing.T) {
	d := depsFor(t)
	for _, mode := range []string{"chat", "pm", "work"} {
		reg, _ := BuildMode(mode, d, false)
		if !hasTool(reg, "remember") || !hasTool(reg, "forget") {
			t.Fatalf("mode %q should have the remember and forget tools", mode)
		}
	}
}

// The remember tool appends a dated entry to memory.md and emits a doc_updated
// event with doc:"memory".
func TestRememberAppendsAndEmitsDocUpdated(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")

	res, err := remember(d).Call(context.Background(), map[string]any{"note": "prefers table-driven tests", "category": "preference"})
	if err != nil || res.IsError {
		t.Fatalf("remember: %v %s", err, res.Content)
	}
	body, _ := d.Docs.ReadMemory()
	if !strings.Contains(body, "## User preferences") || !strings.Contains(body, "prefers table-driven tests") {
		t.Fatalf("memory not written:\n%s", body)
	}
	var sawDocUpdated bool
	for _, ev := range rec.events {
		if ev.Type == event.DocUpdated && ev.Data["doc"] == "memory" {
			sawDocUpdated = true
		}
	}
	if !sawDocUpdated {
		t.Fatalf("expected a doc_updated event with doc:memory; got %+v", rec.events)
	}
}

// Over the active-memory soft budget, remember still records the note but its
// result carries a grooming nudge. Over the active hard ceiling, remember
// returns an error result whose guidance ("consolidate") reaches the model.
func TestRememberSoftNudgeThenHardRefusal(t *testing.T) {
	d := depsFor(t)

	// Over soft budget, under hard ceiling: recorded, with a nudge.
	overSoft := "# Project memory\n\n## Lessons learned\n" + strings.Repeat("- 2020-01-01: filler line\n", 480)
	if err := os.WriteFile(d.Docs.MemoryPath(), []byte(overSoft), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := remember(d).Call(context.Background(), map[string]any{"note": "keep me"})
	if err != nil {
		t.Fatalf("remember returned a hard error: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "soft budget") {
		t.Fatalf("expected a success result with a soft-budget nudge; got IsError=%v %q", res.IsError, res.Content)
	}
	if body, _ := d.Docs.ReadMemory(); !strings.Contains(body, "keep me") {
		t.Fatalf("note must be recorded over the soft budget:\n%s", body)
	}

	// Over the hard ceiling: refused with consolidate guidance.
	overHard := "# Project memory\n\n## Lessons learned\n" + strings.Repeat("- 2020-01-01: filler line\n", 1600)
	if err := os.WriteFile(d.Docs.MemoryPath(), []byte(overHard), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = remember(d).Call(context.Background(), map[string]any{"note": "x"})
	if err != nil {
		t.Fatalf("remember returned a hard error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "consolidate") || !strings.Contains(res.Content, "Largest active notes") {
		t.Fatalf("expected a hard-ceiling error result mentioning consolidate; got IsError=%v %q", res.IsError, res.Content)
	}
}

// With a daemon memory-pressure hook, the over-budget writer is told grooming
// is handled instead of being asked to groom mid-task; a refusal also names it.
func TestRememberDefersToAutomaticGroom(t *testing.T) {
	d := depsFor(t)
	var calls []int
	d.MemoryPressure = func(active int) string {
		calls = append(calls, active)
		return "an automatic memory-groom session (s_groom) is already consolidating project memory"
	}
	overSoft := "# Project memory\n\n## Lessons learned\n" + strings.Repeat("- 2020-01-01: filler line\n", 480)
	if err := os.WriteFile(d.Docs.MemoryPath(), []byte(overSoft), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := remember(d).Call(context.Background(), map[string]any{"note": "keep me"})
	if res.IsError || !strings.Contains(res.Content, "s_groom") || strings.Contains(res.Content, "forget") {
		t.Fatalf("over-soft write should report the automatic groom instead of asking for manual grooming: %q", res.Content)
	}
	if len(calls) != 1 || calls[0] < docs.MemorySoftBudget {
		t.Fatalf("pressure hook calls = %v", calls)
	}

	overHard := "# Project memory\n\n## Lessons learned\n" + strings.Repeat("- 2020-01-01: filler line\n", 1600)
	if err := os.WriteFile(d.Docs.MemoryPath(), []byte(overHard), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ = remember(d).Call(context.Background(), map[string]any{"note": "x"})
	if !res.IsError || !strings.Contains(res.Content, "s_groom") || !strings.Contains(res.Content, "retry") {
		t.Fatalf("refusal should name the running groom: %q", res.Content)
	}

	// Under the soft budget the hook is not consulted.
	calls = nil
	if err := os.WriteFile(d.Docs.MemoryPath(), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if res, _ := remember(d).Call(context.Background(), map[string]any{"note": "small"}); res.IsError || len(calls) != 0 {
		t.Fatalf("under-budget write consulted the hook: %v %q", calls, res.Content)
	}
}

func TestForgetRetiresNotesAndEmitsDocUpdated(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	a, err := d.Docs.AppendMemory("stale gotcha", "gotcha")
	if err != nil {
		t.Fatal(err)
	}
	res, err := forget(d).Call(context.Background(), map[string]any{"ids": []any{a.ID}, "reason": "fixed in 0405"})
	if err != nil || res.IsError || !strings.Contains(res.Content, "retired "+a.ID) {
		t.Fatalf("forget: %v %q", err, res.Content)
	}
	if prompt := assemble("X", false, d.Workspace, true); strings.Contains(prompt, "stale gotcha") {
		t.Fatalf("retired note still injected:\n%s", prompt)
	}
	var saw bool
	for _, ev := range rec.events {
		saw = saw || (ev.Type == event.DocUpdated && ev.Data["doc"] == "memory")
	}
	if !saw {
		t.Fatal("forget should emit doc_updated for memory")
	}
	if res, _ := forget(d).Call(context.Background(), map[string]any{"ids": []any{"m-unknown"}}); !res.IsError {
		t.Fatalf("unknown id should be an error result: %q", res.Content)
	}
}

// assemble injects memory.md contents (with the advisory framing) into the system
// prompt when present, and adds nothing when absent — a byte-identical prompt.
func TestAssembleInjectsMemory(t *testing.T) {
	ws := t.TempDir()

	base := assemble("BASE PROMPT", false, ws, true)
	if strings.Contains(base, "PROJECT MEMORY") {
		t.Fatalf("absent memory should add nothing:\n%s", base)
	}

	if err := os.WriteFile(filepath.Join(ws, "memory.md"), []byte("# Project memory\n\n## Lessons learned\n- 2026-01-01: use -run while iterating\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withMem := assemble("BASE PROMPT", false, ws, true)
	if !strings.Contains(withMem, "PROJECT MEMORY") {
		t.Fatalf("memory not injected:\n%s", withMem)
	}
	if !strings.Contains(withMem, "use -run while iterating") {
		t.Fatalf("memory content not injected:\n%s", withMem)
	}
	// The pre-memory portion is unchanged (memory is only ever appended).
	if !strings.HasPrefix(withMem, base) {
		t.Fatalf("memory injection changed the base prompt")
	}
}

// Committed project instructions reach the prompt ahead of advisory memory.
func TestAssembleInjectsProjectInstructionsBeforeMemory(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("run make check before committing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "memory.md"), []byte("# Project memory\n\n## Lessons learned\n- 2026-01-01: use -run while iterating\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := inspectSys("BASE PROMPT", ws)
	instr, mem := strings.Index(prompt, "run make check before committing"), strings.Index(prompt, "PROJECT MEMORY")
	if instr < 0 || mem < 0 || instr > mem || !strings.Contains(prompt, "committed CLAUDE.md") {
		t.Fatalf("instructions missing or misplaced:\n%s", prompt)
	}
}

func TestFreshSessionPromptOmitsSupersededPolicy(t *testing.T) {
	d := depsFor(t)
	when := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	invented, err := d.Docs.AppendMemoryEntry(docs.MemoryEntry{
		Note: "Use plus or minus 13 percent as the user's benchmark rule", Kind: docs.MemoryProposedPolicy,
		Provenance: docs.MemoryProvenance{SessionID: "s_vals", EventSeq: 690, EventTime: when, Actor: "coordinator"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.MemorySource = func(docs.MemoryKind) docs.MemoryProvenance {
		return docs.MemoryProvenance{SessionID: "s_vals", EventSeq: 694, EventTime: when.Add(time.Minute), Actor: "user", Scope: "workspace"}
	}
	res, err := remember(d).Call(context.Background(), map[string]any{
		"note": "No benchmark variance threshold exists unless the user explicitly sets one", "kind": "user_guidance",
		"supersedes": []any{invented.ID},
	})
	if err != nil || res.IsError {
		t.Fatalf("record correction: %v %+v", err, res)
	}
	// The result must say the superseded note is already retired, so the agent
	// does not follow up with a forget that would fail as a no-op.
	if want := "superseded " + invented.ID + " (now inactive; no forget needed)"; !strings.Contains(res.Content, want) {
		t.Fatalf("remember result missing %q: %s", want, res.Content)
	}

	// assemble models a new session: it reads memory.md from disk rather than
	// retaining any state from the writing session.
	prompt := assemble("FRESH SESSION", false, d.Workspace, true)
	if strings.Contains(prompt, "user's benchmark rule") {
		t.Fatalf("fresh session resurrected superseded invented policy:\n%s", prompt)
	}
	for _, want := range []string{"No benchmark variance threshold exists", "[user-stated; ", "s_vals#694/user"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("fresh session prompt missing %q:\n%s", want, prompt)
		}
	}
	body, _ := d.Docs.ReadMemory()
	if !strings.Contains(body, "user's benchmark rule") {
		t.Fatalf("superseded audit record was deleted:\n%s", body)
	}
}

// A direct Edit/Write of memory.md surfaces as a doc_updated event (doc:"memory")
// via the Workspace OnWrite hook, even though memory is not part of the docs set.
func TestMemoryEditEmitsDocUpdated(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	reg, _ := BuildMode("pm", d, false)

	res := reg.Dispatch(context.Background(), gollama.ToolCall{
		Function: gollama.ToolCallFunction{
			Name:      "Write",
			Arguments: `{"file_path":"memory.md","content":"# Project memory\n\n## Lessons learned\n- 2026-01-01: x\n"}`,
		},
	})
	if res.IsError {
		t.Fatalf("Write memory.md: %s", res.Content)
	}
	var sawMemory bool
	for _, ev := range rec.events {
		if ev.Type == event.DocUpdated && ev.Data["doc"] == "memory" {
			sawMemory = true
		}
	}
	if !sawMemory {
		t.Fatalf("expected doc_updated doc:memory on direct edit; got %+v", rec.events)
	}
}
