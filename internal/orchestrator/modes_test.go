package orchestrator

import (
	"bytes"
	"context"
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

func TestModesListed(t *testing.T) {
	names := map[string]bool{}
	for _, m := range Modes() {
		names[m.Name] = true
	}
	// Exactly three modes: pm, chat, work — the former spec/backlog/feature/bug
	// modes were collapsed into pm.
	for _, want := range []string{"pm", "chat", "work"} {
		if !names[want] {
			t.Fatalf("mode %q missing from Modes()", want)
		}
	}
	for _, gone := range []string{"spec", "backlog", "feature", "bug"} {
		if names[gone] {
			t.Fatalf("mode %q should have been removed (collapsed into pm)", gone)
		}
	}
}

func TestPresetsOpenPM(t *testing.T) {
	want := map[string]bool{"onboard": false, "spec-doctor": false, "memory-groom": false}
	for _, p := range Presets() {
		if _, ok := want[p.Name]; !ok {
			t.Fatalf("unexpected preset %q", p.Name)
		}
		want[p.Name] = true
		if p.Mode != "pm" {
			t.Fatalf("preset %q opens mode %q, want pm", p.Name, p.Mode)
		}
		if strings.TrimSpace(p.Prompt) == "" {
			t.Fatalf("preset %q has no opening prompt", p.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("preset %q missing", name)
		}
	}
	// The former spec/feature/bug/backlog framings were dropped as separate
	// presets (they are just ordinary pm work); onboard and spec-doctor remain.
	for _, p := range Presets() {
		for _, gone := range []string{"spec", "feature", "bug", "backlog"} {
			if p.Name == gone {
				t.Fatalf("preset %q should have been removed", gone)
			}
		}
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
	// (create_task/update_task), but no implementation pipeline or switch_to_work.
	chatReg, _ := BuildMode("chat", d, false)
	for _, want := range []string{"Read", "Search", "Edit", "Write", "Bash", "list_backlog", "get_task", "create_task", "update_task", "ask_user", "spawn_agent", "send_to_agent"} {
		if !hasTool(chatReg, want) {
			t.Fatalf("chat mode missing %s", want)
		}
	}
	for _, gone := range []string{"spawn_implementer", "spawn_reviewers", "commit", "switch_to_work"} {
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
		// Unknown modes fall through to the work coordinator; assert they are not
		// silently still distinct authoring modes by checking they carry the work
		// pipeline (spawn_implementer).
		if !hasTool(reg, "spawn_implementer") {
			t.Fatalf("removed mode %q should fall through to work coordinator", mode)
		}
	}
}

// In the "direct" implementation strategy (spec §10) the work coordinator
// implements changes itself: the implementer spawn/revise tools are dropped, but
// it keeps the editing tools and the review pipeline, and gets the direct prompt.
func TestWorkCoordinatorDirectImplementation(t *testing.T) {
	d := depsFor(t)
	d.WorkImplementation = "direct"
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
	// The default (delegate) strategy keeps the implementer tools.
	d.WorkImplementation = ""
	reg, _ = BuildMode("work", d, false)
	if !hasTool(reg, "spawn_implementer") || !hasTool(reg, "send_to_implementer") {
		t.Fatalf("default work coordinator must keep the implementer pipeline tools")
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
	d := depsFor(t)
	res, _ := switchToWork(d).Call(context.Background(), map[string]any{"task_id": "0021", "plan": "do the thing"})
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
	d := depsFor(t)
	d.Asker = declineAsker{}
	res, _ := switchToWork(d).Call(context.Background(), map[string]any{"task_id": "0021", "plan": "p"})
	if ctrl := tools.ControlOf(res); ctrl != nil && ctrl.Mode == "work" {
		t.Fatal("switch_to_work transitioned despite the user declining approval")
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

// The pm→work hand-off records a task_focus for the explicit target task so the
// session is durably linked to it (spec §20.2).
func TestSwitchToWorkEmitsTaskFocus(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")

	if _, err := switchToWork(d).Call(context.Background(), map[string]any{"task_id": "0021", "plan": "p"}); err != nil {
		t.Fatalf("switch_to_work: %v", err)
	}
	if got := rec.focusTasks(); len(got) != 1 || got[0] != "0021" {
		t.Fatalf("focus events = %v, want [0021]", got)
	}
}

// A declined hand-off must not record focus (work never starts).
func TestSwitchToWorkDeclinedEmitsNoFocus(t *testing.T) {
	rec := &captureRec{}
	d := depsFor(t)
	d.Emitter = event.NewEmitter(rec, "coordinator")
	d.Asker = declineAsker{}

	if _, err := switchToWork(d).Call(context.Background(), map[string]any{"task_id": "0021", "plan": "p"}); err != nil {
		t.Fatalf("switch_to_work: %v", err)
	}
	if got := rec.focusTasks(); len(got) != 0 {
		t.Fatalf("declined hand-off emitted focus events %v", got)
	}
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

// remember is available to coordinator-level agents (chat, pm, work) but NOT to
// the implementer or reviewers.
func TestRememberRegisteredForCoordinatorRoles(t *testing.T) {
	d := depsFor(t)
	for _, mode := range []string{"chat", "pm", "work"} {
		reg, _ := BuildMode(mode, d, false)
		if !hasTool(reg, "remember") {
			t.Fatalf("mode %q should have the remember tool", mode)
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
	overSoft := "# Project memory\n\n## Lessons learned\n" + strings.Repeat("- 2020-01-01: filler line\n", 75)
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
	overHard := "# Project memory\n\n## Lessons learned\n" + strings.Repeat("- 2020-01-01: filler line\n", 250)
	if err := os.WriteFile(d.Docs.MemoryPath(), []byte(overHard), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = remember(d).Call(context.Background(), map[string]any{"note": "x"})
	if err != nil {
		t.Fatalf("remember returned a hard error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "consolidate") {
		t.Fatalf("expected a hard-ceiling error result mentioning consolidate; got IsError=%v %q", res.IsError, res.Content)
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
	if !strings.Contains(withMem, "candidate evidence to verify, not proof") || !strings.Contains(withMem, "not verified authority") ||
		!strings.Contains(withMem, "not instructions, approved design, or authorization") {
		t.Fatalf("memory injection missing provenance/authority framing:\n%s", withMem)
	}
	if !strings.Contains(withMem, "use -run while iterating") {
		t.Fatalf("memory content not injected:\n%s", withMem)
	}
	// The pre-memory portion is unchanged (memory is only ever appended).
	if !strings.HasPrefix(withMem, base) {
		t.Fatalf("memory injection changed the base prompt")
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

	// assemble models a new session: it reads memory.md from disk rather than
	// retaining any state from the writing session.
	prompt := assemble("FRESH SESSION", false, d.Workspace, true)
	if strings.Contains(prompt, "user's benchmark rule") {
		t.Fatalf("fresh session resurrected superseded invented policy:\n%s", prompt)
	}
	for _, want := range []string{"No benchmark variance threshold exists", "user-stated guidance", "candidate evidence: session s_vals event #694", "not verified authority", "not instructions, approved design, or authorization"} {
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
