package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestBacklogHidesDoneByDefault(t *testing.T) {
	tasks := []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "todo", Title: "a"},
		{Id: "0002", Status: "in_progress", Title: "b"},
		{Id: "0003", Status: "done", Title: "c"},
		{Id: "0004", Status: "blocked", Title: "d"},
		{Id: "0005", Status: "done", Title: "e"},
	}
	m := model{backlog: true, backlogTasks: tasks}

	// Default: done tasks are hidden.
	vis := m.visibleBacklogTasks()
	if len(vis) != 3 {
		t.Fatalf("default visible=%d, want 3 (done hidden)", len(vis))
	}
	for _, tk := range vis {
		if tk.Status == "done" {
			t.Fatalf("done task %s visible by default", tk.Id)
		}
	}

	// Toggle with "d": done tasks become visible.
	updated, _ := m.updateBacklog(keyMsg("d"))
	m = updated.(model)
	if !m.backlogShowDone {
		t.Fatalf("backlogShowDone not set after toggle")
	}
	if len(m.visibleBacklogTasks()) != len(tasks) {
		t.Fatalf("after toggle visible=%d, want %d", len(m.visibleBacklogTasks()), len(tasks))
	}

	// Non-done tasks always present regardless of toggle.
	for _, showDone := range []bool{true, false} {
		m.backlogShowDone = showDone
		got := map[string]bool{}
		for _, tk := range m.visibleBacklogTasks() {
			got[tk.Id] = true
		}
		for _, id := range []string{"0001", "0002", "0004"} {
			if !got[id] {
				t.Fatalf("non-done task %s missing (showDone=%v)", id, showDone)
			}
		}
	}

	// Cursor stays in range when toggling show->hide while pointing at a done row.
	m.backlogShowDone = true
	m.backlogCursor = len(m.visibleBacklogTasks()) - 1 // last (done) row
	updated, _ = m.updateBacklog(keyMsg("d"))
	m = updated.(model)
	if m.backlogShowDone {
		t.Fatalf("expected toggle back to hide done")
	}
	if m.backlogCursor >= len(m.visibleBacklogTasks()) {
		t.Fatalf("cursor=%d out of range for %d visible", m.backlogCursor, len(m.visibleBacklogTasks()))
	}
}

// TestBlockedIndicator verifies the home-menu "waiting on you" indicator appears
// only when a backlog task is blocked, and that pressing ctrl+w opens the backlog
// browser filtered to the blocked tasks (task 0101).
func TestBlockedIndicator(t *testing.T) {
	// No blocked tasks: indicator absent.
	m := model{state: stateMenu, backlogTasks: []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "todo", Title: "a"},
		{Id: "0002", Status: "done", Title: "b"},
	}}
	m.prompt = newChatInput("test")
	if m.blockedTaskCount() != 0 {
		t.Fatalf("blockedTaskCount=%d, want 0", m.blockedTaskCount())
	}
	if strings.Contains(m.menuView(), "waiting on you") {
		t.Fatalf("menu shows blocked indicator when nothing is blocked:\n%s", m.menuView())
	}

	// Add blocked tasks: indicator present with a count.
	m.backlogTasks = append(m.backlogTasks,
		&v1.BacklogTaskSummary{Id: "0003", Status: "blocked", Title: "c"},
		&v1.BacklogTaskSummary{Id: "0004", Status: "blocked", Title: "d"},
	)
	if m.blockedTaskCount() != 2 {
		t.Fatalf("blockedTaskCount=%d, want 2", m.blockedTaskCount())
	}
	view := m.menuView()
	if !strings.Contains(view, "waiting on you") {
		t.Fatalf("menu missing blocked indicator:\n%s", view)
	}
	if !strings.Contains(view, "2 tasks blocked") {
		t.Fatalf("menu missing blocked count:\n%s", view)
	}

	// Press ctrl+w: opens the backlog browser filtered to blocked tasks.
	updated, _ := m.updateMenu(keyMsg("ctrl+w"))
	m = updated.(model)
	if !m.backlog {
		t.Fatalf("after ctrl+w, backlog browser not open")
	}
	if !m.backlogBlockedOnly {
		t.Fatalf("after ctrl+w, backlogBlockedOnly not set")
	}
	vis := m.visibleBacklogTasks()
	if len(vis) != 2 {
		t.Fatalf("blocked-only visible=%d, want 2", len(vis))
	}
	for _, tk := range vis {
		if tk.Status != "blocked" {
			t.Fatalf("blocked-only view shows non-blocked task %s (%s)", tk.Id, tk.Status)
		}
	}

	// esc closes and clears the filter.
	updated, _ = m.updateBacklog(keyMsg("esc"))
	m = updated.(model)
	if m.backlog || m.backlogBlockedOnly {
		t.Fatalf("esc did not close/clear blocked filter: backlog=%v blockedOnly=%v", m.backlog, m.backlogBlockedOnly)
	}
}

// TestBlockedIndicatorBareWTypes ensures a naked "w" always types into the
// prompt — menu affordances are ctrl-chords, so a bare letter never triggers
// anything even when tasks are blocked — and that ctrl+w is a no-op (falls
// through) when nothing is blocked (task 0101).
func TestBlockedIndicatorBareWTypes(t *testing.T) {
	// Blocked tasks present: a naked "w" still just types.
	m := model{state: stateMenu, backlogTasks: []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "blocked", Title: "a"},
	}}
	m.prompt = newChatInput("test")
	m.prompt.Focus()
	updated, _ := m.updateMenu(keyMsg("w"))
	m = updated.(model)
	if m.backlog {
		t.Fatalf("naked w opened the backlog browser")
	}
	if m.prompt.Value() != "w" {
		t.Fatalf("w not typed into prompt, got %q", m.prompt.Value())
	}

	// Nothing blocked: ctrl+w does not open the browser.
	m2 := model{state: stateMenu, backlogTasks: []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "todo", Title: "a"},
	}}
	m2.prompt = newChatInput("test")
	m2.prompt.Focus()
	updated, _ = m2.updateMenu(keyMsg("ctrl+w"))
	m2 = updated.(model)
	if m2.backlog {
		t.Fatalf("ctrl+w opened backlog browser when nothing is blocked")
	}
}

// ctrl+b opens the backlog browser while a question is pending, and the picker
// state survives so sessionView restores it when the browser closes.
func TestPickerCtrlBOpensBacklog(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	updated, cmd := m.Update(keyMsg("ctrl+b"))
	m = updated.(model)
	if !m.backlog {
		t.Fatal("ctrl+b while picking should open the backlog browser")
	}
	if !m.picking {
		t.Fatal("opening the backlog browser must not drop the pending picker")
	}
	if cmd == nil {
		t.Fatal("ctrl+b should return the fetchBacklog command")
	}
}

func TestBacklogViewScrollsWithinViewport(t *testing.T) {
	var tasks []*v1.BacklogTaskSummary
	for i := 1; i <= 30; i++ {
		tasks = append(tasks, &v1.BacklogTaskSummary{
			Id:     fmt.Sprintf("%04d", i),
			Status: "todo",
			Title:  fmt.Sprintf("task %d", i),
			Ready:  true,
		})
	}
	m := model{backlog: true, backlogTasks: tasks}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(model)

	// Cursor at top: list clipped to viewport, first task visible.
	out := m.backlogView()
	if lines := len(strings.Split(out, "\n")); lines > 12 {
		t.Fatalf("backlogView produced %d lines, want <= 12", lines)
	}
	if !strings.Contains(out, "0001") {
		t.Fatalf("first task 0001 not visible at cursor top:\n%s", out)
	}
	if strings.Contains(out, "0030") {
		t.Fatalf("last task 0030 unexpectedly visible at cursor top:\n%s", out)
	}

	// Move cursor to the last task: window scrolls.
	m.backlogCursor = len(m.visibleBacklogTasks()) - 1
	out = m.backlogView()
	if lines := len(strings.Split(out, "\n")); lines > 12 {
		t.Fatalf("backlogView (last) produced %d lines, want <= 12", lines)
	}
	if !strings.Contains(out, "0030") {
		t.Fatalf("last task 0030 not visible at cursor bottom:\n%s", out)
	}
	if strings.Contains(out, "0001") {
		t.Fatalf("first task 0001 still visible after scrolling to bottom:\n%s", out)
	}
}

// TestBacklogDetailScrolls verifies the backlog task detail view is a scrollable
// viewport: opening a task starts at the top, scroll keys advance the offset so
// long content is reachable, and opening a different task resets to the top.
func TestBacklogDetailScrolls(t *testing.T) {
	m := model{
		state: stateMenu, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		thinkLevels: map[string]string{"coordinator": "high", "implementer": "high", "reviewers": "high"},
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.backlog = true

	// A body far longer than one viewport page.
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&sb, "line %03d\n", i)
	}
	updated, _ = m.Update(taskDetailMsg{task: &v1.TaskDetail{Id: "0001", Title: "t", Body: sb.String()}})
	m = updated.(model)

	if !m.backlogVP.AtTop() {
		t.Fatalf("detail viewport did not start at top: YOffset=%d", m.backlogVP.YOffset())
	}
	// Render once (detail view) to ensure it does not panic and produces output.
	if out := m.render(); out == "" {
		t.Fatalf("detail render produced no output")
	}

	// Scroll down several times; the offset must increase.
	before := m.backlogVP.YOffset()
	for i := 0; i < 5; i++ {
		updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		m = updated.(model)
	}
	if m.backlogVP.YOffset() <= before {
		t.Fatalf("scrolling did not advance offset: before=%d after=%d", before, m.backlogVP.YOffset())
	}

	// Opening a different task resets scroll to the top.
	updated, _ = m.Update(taskDetailMsg{task: &v1.TaskDetail{Id: "0002", Title: "u", Body: sb.String()}})
	m = updated.(model)
	if !m.backlogVP.AtTop() {
		t.Fatalf("opening a new task did not reset to top: YOffset=%d", m.backlogVP.YOffset())
	}
}

// TestBacklogPriorityKeys verifies +/- reprioritize the cursor row via UpdateTask,
// clamped to 1..5 (no RPC at the clamp edge) — task 0099.
func TestBacklogPriorityKeys(t *testing.T) {
	f := newFakeClient()
	m := newBacklogModel(f, []*v1.BacklogTaskSummary{{Id: "0001", Status: "todo", Priority: 3, Title: "a"}})

	// "+" raises priority toward p1 (3 -> 2).
	driveBacklog(t, m, "+")
	if f.lastUpdateTask == nil || f.lastUpdateTask.GetId() != "0001" {
		t.Fatalf("+ did not fire UpdateTask for the cursor row: %+v", f.lastUpdateTask)
	}
	if f.lastUpdateTask.Priority == nil || f.lastUpdateTask.GetPriority() != 2 {
		t.Fatalf("+ priority = %v, want 2", f.lastUpdateTask.Priority)
	}

	// "-" lowers priority toward p5 (3 -> 4).
	f.lastUpdateTask = nil
	m = newBacklogModel(f, []*v1.BacklogTaskSummary{{Id: "0001", Status: "todo", Priority: 3, Title: "a"}})
	driveBacklog(t, m, "-")
	if f.lastUpdateTask == nil || f.lastUpdateTask.GetPriority() != 4 {
		t.Fatalf("- priority = %v, want 4", f.lastUpdateTask)
	}

	// Clamp edges: p1 "+" and p5 "-" are no-ops (no RPC).
	f.lastUpdateTask = nil
	m = newBacklogModel(f, []*v1.BacklogTaskSummary{{Id: "0001", Priority: 1, Title: "a"}})
	driveBacklog(t, m, "+")
	if f.lastUpdateTask != nil {
		t.Fatalf("+ at p1 fired an RPC, want no-op: %+v", f.lastUpdateTask)
	}
	m = newBacklogModel(f, []*v1.BacklogTaskSummary{{Id: "0001", Priority: 5, Title: "a"}})
	driveBacklog(t, m, "-")
	if f.lastUpdateTask != nil {
		t.Fatalf("- at p5 fired an RPC, want no-op: %+v", f.lastUpdateTask)
	}
}

// TestBacklogStatusPrompt verifies "s" then a digit changes status via UpdateTask,
// and "s" then esc cancels without an RPC (task 0099).
func TestBacklogStatusPrompt(t *testing.T) {
	f := newFakeClient()
	m := newBacklogModel(f, []*v1.BacklogTaskSummary{{Id: "0001", Status: "todo", Priority: 3, Title: "a"}})

	m = driveBacklog(t, m, "s")
	if !m.backlogStatusPrompt {
		t.Fatal("s did not enter the status prompt")
	}
	if f.lastUpdateTask != nil {
		t.Fatalf("s alone fired an RPC: %+v", f.lastUpdateTask)
	}
	m = driveBacklog(t, m, "3") // in_review
	if m.backlogStatusPrompt {
		t.Fatal("status prompt still active after selecting a digit")
	}
	if f.lastUpdateTask == nil || f.lastUpdateTask.GetStatus() != "in_review" {
		t.Fatalf("status digit = %+v, want status=in_review", f.lastUpdateTask)
	}

	// esc cancels the prompt without an RPC.
	f.lastUpdateTask = nil
	m = newBacklogModel(f, []*v1.BacklogTaskSummary{{Id: "0001", Status: "todo", Title: "a"}})
	m = driveBacklog(t, m, "s")
	m = driveBacklog(t, m, "esc")
	if m.backlogStatusPrompt {
		t.Fatal("esc did not cancel the status prompt")
	}
	if f.lastUpdateTask != nil {
		t.Fatalf("esc after s fired an RPC: %+v", f.lastUpdateTask)
	}
}

// TestBacklogEditorGating verifies the open-in-editor affordance is only offered
// when the task file is local, and degrades to a footer notice otherwise (task 0099).
func TestBacklogEditorGating(t *testing.T) {
	// A remote/non-local path: taskFileLocal is false, "e" degrades to a notice
	// (and never execs an editor).
	f := newFakeClient()
	m := newBacklogModel(f, nil)
	m.backlogDetail = &v1.TaskDetail{Id: "0001", Title: "a", Path: "/no/such/task/file.md"}
	if taskFileLocal(m.backlogDetail.Path) {
		t.Fatal("taskFileLocal true for a nonexistent path")
	}
	updated, cmd := m.updateBacklog(keyMsg("e"))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("e on a non-local task returned a command (would exec an editor)")
	}
	if m.backlogNotice == "" {
		t.Fatal("e on a non-local task did not set a footer notice")
	}

	// A real file: taskFileLocal is true and the detail footer advertises "e edit".
	dir := t.TempDir()
	p := filepath.Join(dir, "0001-x.md")
	if err := os.WriteFile(p, []byte("# task\n"), 0o644); err != nil {
		t.Fatalf("write task file: %v", err)
	}
	if !taskFileLocal(p) {
		t.Fatal("taskFileLocal false for an existing file")
	}
	m2 := model{ready: true}
	m2.w, m2.h = 80, 24
	if got := m2.taskDetailView(&v1.TaskDetail{Id: "0001", Title: "a", Path: p}); !strings.Contains(got, "e edit") {
		t.Fatalf("detail footer missing 'e edit' for a local task:\n%s", got)
	}
	if got := m2.taskDetailView(&v1.TaskDetail{Id: "0001", Title: "a", Path: "/nope.md"}); strings.Contains(got, "e edit") {
		t.Fatalf("detail footer advertised 'e edit' for a non-local task:\n%s", got)
	}
}

// TestEditorCommand covers the $EDITOR → $VISUAL → vi resolution order (task 0099).
func TestEditorCommand(t *testing.T) {
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")
	if got := editorCommand(); got != "vi" {
		t.Fatalf("default editor = %q, want vi", got)
	}
	t.Setenv("VISUAL", "nano")
	if got := editorCommand(); got != "nano" {
		t.Fatalf("VISUAL editor = %q, want nano", got)
	}
	t.Setenv("EDITOR", "emacs")
	if got := editorCommand(); got != "emacs" {
		t.Fatalf("EDITOR editor = %q, want emacs (takes precedence over VISUAL)", got)
	}
}

// TestBacklogMultiSelectSpawn covers the multi-select "run in parallel" flow
// (task 0085): space toggles selection on todo tasks, P spawns one workstream per
// selected task with the right project/task ids and opens the Workstreams panel.
func TestBacklogMultiSelectSpawn(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.project = "demo"
	m.backlog = true
	m.backlogTasks = []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "todo", Title: "alpha"},
		{Id: "0002", Status: "todo", Title: "beta"},
		{Id: "0003", Status: "in_progress", Title: "gamma"},
	}

	// Select the first todo task, move down, select the second.
	m = drive(t, m, "space")
	m = drive(t, m, "down")
	m = drive(t, m, "space")
	if len(m.backlogSelected) != 2 || !m.backlogSelected["0001"] || !m.backlogSelected["0002"] {
		t.Fatalf("selection = %v, want {0001,0002}", m.backlogSelected)
	}

	// Move to the in_progress task and try to select it — not spawnable.
	m = drive(t, m, "down")
	m = drive(t, m, "space")
	if m.backlogSelected["0003"] {
		t.Fatal("non-todo task should not be selectable")
	}

	// P spawns one workstream per selected task and opens the panel.
	m = drive(t, m, "P")
	if len(f.spawnReqs) != 2 {
		t.Fatalf("SpawnWorkstream calls = %d, want 2", len(f.spawnReqs))
	}
	gotTasks := map[string]bool{}
	for _, r := range f.spawnReqs {
		if r.Project != "demo" {
			t.Fatalf("spawn project = %q, want demo", r.Project)
		}
		gotTasks[r.TaskId] = true
	}
	if !gotTasks["0001"] || !gotTasks["0002"] {
		t.Fatalf("spawned task ids = %v, want {0001,0002}", gotTasks)
	}
	if !m.ws {
		t.Fatal("spawn should open the Workstreams panel")
	}
	if m.backlog {
		t.Fatal("spawn should close the backlog browser")
	}
}
