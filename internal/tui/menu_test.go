package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"connectrpc.com/connect"
	"github.com/charmbracelet/x/ansi"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// needsOnboarding flags an un-onboarded workspace (no real spec.md + no backlog
// tasks) so the home menu can surface onboarding prominently (spec §19.2).
func TestNeedsOnboarding(t *testing.T) {
	t.Run("fresh empty dir", func(t *testing.T) {
		if !needsOnboarding(t.TempDir()) {
			t.Fatal("empty workspace should need onboarding")
		}
	})
	t.Run("non-trivial spec", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "spec.md", "# Spec\n\n## Goals\nship it\n")
		if needsOnboarding(ws) {
			t.Fatal("workspace with a real spec should not need onboarding")
		}
	})
	t.Run("backlog task but no spec", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "backlog/0001-foo.md", "# task\n")
		if needsOnboarding(ws) {
			t.Fatal("workspace with a backlog task should not need onboarding")
		}
	})
	t.Run("trivially empty spec, no backlog", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "spec.md", "# Spec\n")
		if !needsOnboarding(ws) {
			t.Fatal("trivially-empty spec + no backlog should need onboarding")
		}
	})
	t.Run("only stray non-task .md, no tasks", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "backlog/notes.md", "# Notes\n")
		if !needsOnboarding(ws) {
			t.Fatal("stray non-task .md without tasks should still need onboarding")
		}
	})
	t.Run("configured spec entry point with content", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, ".ycc/config.toml", "spec_path = \"docs/index.md\"\n")
		writeFile(t, ws, "docs/index.md", "# Spec\n\n## Goals\nship it\n")
		if specIsEmpty(ws) {
			t.Fatal("configured entry point with real content should not be empty")
		}
		if needsOnboarding(ws) {
			t.Fatal("workspace with a real configured spec should not need onboarding")
		}
	})
	t.Run("configured spec entry point missing", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, ".ycc/config.toml", "spec_path = \"docs/index.md\"\n")
		// Root spec.md exists but is not the configured entry point.
		writeFile(t, ws, "spec.md", "# Real spec\n\ncontent\n")
		if !specIsEmpty(ws) {
			t.Fatal("missing configured entry point should be empty (root spec.md must not count)")
		}
		if !needsOnboarding(ws) {
			t.Fatal("missing configured spec + no backlog should need onboarding")
		}
	})
}

// TestWaitingSessionIndicator verifies the home-menu "session waiting for you"
// indicator: absent when nothing needs the user, present with a count when a
// live session has a pending question or is paused, and that pressing ctrl+s
// attaches directly (one session) or opens the filtered browser (several), while
// a naked "s" types into the prompt and never triggers anything (task 0107).
func TestWaitingSessionIndicator(t *testing.T) {
	// (a) No waiting sessions: line absent.
	m := model{state: stateMenu}
	m.prompt = newChatInput("test")
	if strings.Contains(m.menuView(), "waiting for you") || strings.Contains(m.menuView(), "waiting for your answer") {
		t.Fatalf("menu shows waiting-session line when nothing needs the user:\n%s", m.menuView())
	}

	// (b) One session with a pending question: line present with count + "press ctrl+s".
	m.waitingSessions = []*v1.SessionSummary{
		{SessionId: "s_q", Mode: "work", Status: "running", Live: true, WaitingInput: true},
	}
	view := m.menuView()
	if !strings.Contains(view, "1 session waiting for your answer") {
		t.Fatalf("menu missing waiting-session line:\n%s", view)
	}
	if !strings.Contains(view, "press ctrl+s to open") {
		t.Fatalf("menu missing 'ctrl+s' route hint:\n%s", view)
	}

	// (c) ctrl+s with exactly one waiting session attaches directly (reopen).
	f := newFakeClient()
	f.history = []*v1.SessionSummary{
		{SessionId: "s_q", Mode: "work", Status: "running", Live: true, WaitingInput: true},
	}
	one := initialModel(context.Background(), f, t_tempWorkspace, false)
	one.waitingSessions = f.history
	one = drive(t, one, "ctrl+s")
	if f.lastReopened != "s_q" {
		t.Fatalf("ctrl+s with one waiting session reopened %q, want s_q", f.lastReopened)
	}
	if one.state != stateSession || one.sessionID != "s_q" {
		t.Fatalf("ctrl+s did not attach to the waiting session: state=%v id=%q", one.state, one.sessionID)
	}

	// (d) ctrl+s with two waiting sessions opens the browser filtered to them.
	f2 := newFakeClient()
	f2.history = []*v1.SessionSummary{
		{SessionId: "s_q", Mode: "work", Status: "running", Live: true, WaitingInput: true, LastActivity: "2024-01-02T10:00:00Z"},
		{SessionId: "s_p", Mode: "chat", Status: "paused", Live: true, LastActivity: "2024-01-01T10:00:00Z"},
		{SessionId: "s_done", Mode: "work", Status: "idle", Live: false, LastActivity: "2024-01-03T10:00:00Z"},
	}
	two := initialModel(context.Background(), f2, t_tempWorkspace, false)
	two.waitingSessions = []*v1.SessionSummary{f2.history[0], f2.history[1]}
	two = drive(t, two, "ctrl+s")
	if two.state != stateHistory {
		t.Fatalf("ctrl+s with two waiting sessions state=%v, want stateHistory", two.state)
	}
	if !two.historyWaitingOnly {
		t.Fatalf("ctrl+s with two waiting sessions did not set historyWaitingOnly")
	}
	if len(two.history) != 2 {
		t.Fatalf("waiting-only browser shows %d rows, want 2 (filtered): %+v", len(two.history), two.history)
	}
	for _, s := range two.history {
		if !s.Live || (!s.WaitingInput && s.Status != "paused") {
			t.Fatalf("waiting-only browser shows a non-waiting session: %+v", s)
		}
	}

	// (e) A naked "s" always types into the prompt, even with sessions waiting.
	typing := model{state: stateMenu, waitingSessions: f.history}
	typing.prompt = newChatInput("test")
	typing.prompt.Focus()
	typing = typeText(t, typing, "ba")
	updated, _ := typing.updateMenu(keyMsg("s"))
	typing = updated.(model)
	if typing.state != stateMenu {
		t.Fatalf("s hijacked typing: state=%v", typing.state)
	}
	if typing.prompt.Value() != "bas" {
		t.Fatalf("s not typed into prompt, got %q", typing.prompt.Value())
	}
}

// TestMenuContextHeader verifies the home-menu project-context header (task
// 0139): the project name and ready/blocked backlog counts render from the
// backlog, the git and today's-spend segments appear only when their data has
// arrived, and the spend segment stays hidden at zero cost.
func TestMenuContextHeader(t *testing.T) {
	m := model{state: stateMenu, w: 200, workspace: "/home/user/myproj", backlogTasks: []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "todo", Title: "a", Ready: true},
		{Id: "0002", Status: "todo", Title: "b", Ready: true},
		{Id: "0003", Status: "in_progress", Title: "c", Ready: true},
		{Id: "0004", Status: "todo", Title: "d", Ready: false},
		{Id: "0005", Status: "blocked", Title: "e"},
	}}
	m.prompt = newChatInput("test")

	view := ansi.Strip(m.menuView())
	if !strings.Contains(view, "myproj") {
		t.Fatalf("header missing project name:\n%s", view)
	}
	if !strings.Contains(view, "3 ready") {
		t.Fatalf("header missing ready count (want 3):\n%s", view)
	}
	if !strings.Contains(view, "1 blocked") {
		t.Fatalf("header missing blocked count (want 1):\n%s", view)
	}
	// Git + spend segments absent until their data arrives.
	if strings.Contains(view, "⎇") {
		t.Fatalf("git segment present before git info loaded:\n%s", view)
	}
	if strings.Contains(view, "today") {
		t.Fatalf("spend segment present before spend loaded:\n%s", view)
	}

	// Git info arrives -> branch + dirty marker shown.
	updated, _ := m.Update(menuGitMsg{branch: "main", dirty: true})
	m = updated.(model)
	view = ansi.Strip(m.menuView())
	if !strings.Contains(view, "main") || !strings.Contains(view, "⎇") {
		t.Fatalf("header missing git branch after menuGitMsg:\n%s", view)
	}

	// Zero-cost spend still hides the segment.
	updated, _ = m.Update(menuSpendMsg{cost: 0, status: "priced"})
	m = updated.(model)
	if strings.Contains(ansi.Strip(m.menuView()), "today") {
		t.Fatalf("spend segment shown at zero cost:\n%s", m.menuView())
	}

	// Positive spend -> segment appears.
	updated, _ = m.Update(menuSpendMsg{cost: 1.23, status: "priced"})
	m = updated.(model)
	view = ansi.Strip(m.menuView())
	if !strings.Contains(view, "$1.23 today") {
		t.Fatalf("header missing spend segment after menuSpendMsg:\n%s", view)
	}
}

// TestMenuHeaderFitsNarrowTerminal checks the context header stays on exactly one
// physical row and never exceeds the terminal width, dropping segments by
// priority on a narrow terminal (task 0139).
func TestMenuHeaderFitsNarrowTerminal(t *testing.T) {
	for _, w := range []int{80, 40, 20, 10} {
		m := model{state: stateMenu, w: w, workspace: "/home/user/a-rather-long-project-name",
			gitBranch: "feature/some-long-branch-name", gitDirty: true,
			todaySpend: 12.34, todaySpendStatus: "priced", todaySpendLoaded: true,
			backlogTasks: []*v1.BacklogTaskSummary{
				{Id: "0001", Status: "todo", Ready: true},
				{Id: "0002", Status: "blocked"},
			}}
		header := m.menuHeader()
		if strings.Contains(header, "\n") {
			t.Fatalf("w=%d: header spans multiple rows:\n%q", w, header)
		}
		if got := lipgloss.Width(header); got > w {
			t.Fatalf("w=%d: header width %d exceeds terminal width", w, got)
		}
	}
}

// TestMenuContinueLastSession verifies the "ctrl+l continue last session"
// affordance (task 0139): with a lastSession and an empty prompt, ctrl+l reopens
// it; the footer and body advertise the affordance; and a naked "c" always types
// into the prompt.
func TestMenuContinueLastSession(t *testing.T) {
	// No last session: affordance absent.
	f := newFakeClient()
	f.history = []*v1.SessionSummary{
		{SessionId: "s_last", Mode: "work", Title: "wire up the header", Status: "idle", Live: false},
	}
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.w = 200
	if strings.Contains(m.menuView(), "continue last session") {
		t.Fatalf("continue affordance shown with no last session:\n%s", m.menuView())
	}

	// Deliver the recent session via the waiting-sessions message path.
	updated, _ := m.Update(waitingSessionsMsg{recent: f.history[0]})
	m = updated.(model)
	if m.lastSession == nil || m.lastSession.SessionId != "s_last" {
		t.Fatalf("lastSession not set from waitingSessionsMsg: %+v", m.lastSession)
	}
	view := m.menuView()
	if !strings.Contains(view, "continue last session") {
		t.Fatalf("menu missing continue affordance:\n%s", view)
	}
	if !strings.Contains(view, "wire up the header") {
		t.Fatalf("continue affordance missing session title:\n%s", view)
	}
	if !strings.Contains(ansi.Strip(view), "ctrl+l continue last session") {
		t.Fatalf("body missing ctrl+l continue hint:\n%s", view)
	}

	// ctrl+l with an empty prompt reopens the last session.
	m = drive(t, m, "ctrl+l")
	if f.lastReopened != "s_last" {
		t.Fatalf("ctrl+l reopened %q, want s_last", f.lastReopened)
	}
	if m.state != stateSession || m.sessionID != "s_last" {
		t.Fatalf("ctrl+l did not attach to the last session: state=%v id=%q", m.state, m.sessionID)
	}

	// A naked "c" always types into the prompt, even with a last session set.
	typing := model{state: stateMenu, lastSession: f.history[0]}
	typing.prompt = newChatInput("test")
	typing.prompt.Focus()
	typing = typeText(t, typing, "ab")
	updated, _ = typing.updateMenu(keyMsg("c"))
	typing = updated.(model)
	if typing.state != stateMenu {
		t.Fatalf("c hijacked typing: state=%v", typing.state)
	}
	if typing.prompt.Value() != "abc" {
		t.Fatalf("c not typed into prompt, got %q", typing.prompt.Value())
	}
}

// TestBrowseMenuRoutes verifies the browse selector (ctrl+o) routes to the
// backlog and session browsers (spec §18.6/§20.5), and esc dismisses it.
func TestBrowseMenuRoutes(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)

	// ctrl+o opens the browse selector.
	m = drive(t, m, "ctrl+o")
	if !m.browse {
		t.Fatal("ctrl+o should open the browse selector")
	}
	// First entry routes to the backlog browser.
	m = drive(t, m, "enter")
	if m.browse {
		t.Fatal("enter should dismiss the browse selector")
	}
	if !m.backlog {
		t.Fatalf("first browse entry should open the backlog browser")
	}

	// Re-open and route to the plan library browser (second entry).
	m.backlog = false
	m = drive(t, m, "ctrl+o")
	m = drive(t, m, "down")
	m = drive(t, m, "enter")
	if m.browse {
		t.Fatal("enter should dismiss the browse selector")
	}
	if !m.plans {
		t.Fatalf("second browse entry should open the plan library browser")
	}

	// Re-open and route to the sessions browser (third entry).
	m.plans = false
	m = drive(t, m, "ctrl+o")
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	m = drive(t, m, "enter")
	if m.browse {
		t.Fatal("enter should dismiss the browse selector")
	}
	if m.state != stateHistory {
		t.Fatalf("third browse entry should open the session browser (state=%v)", m.state)
	}

	// Esc dismisses the selector without routing.
	m.state = stateMenu
	m = drive(t, m, "ctrl+o")
	m = drive(t, m, "esc")
	if m.browse {
		t.Fatal("esc should dismiss the browse selector")
	}
	if m.state != stateMenu || m.backlog {
		t.Fatalf("esc must not route anywhere (state=%v backlog=%v)", m.state, m.backlog)
	}
}

// TestPreviousSessionsEscReturnsToMenu verifies Esc on the history screen returns
// to the menu rather than opening the settings overlay.
func TestPreviousSessionsEscReturnsToMenu(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = drive(t, m, "ctrl+r")
	if m.state != stateHistory {
		t.Fatalf("state=%v, want stateHistory", m.state)
	}
	m = drive(t, m, "esc")
	if m.state != stateMenu {
		t.Fatalf("after esc state=%v, want stateMenu", m.state)
	}
	if m.overlay {
		t.Fatalf("esc on history opened the settings overlay")
	}
}

func TestWorkLoopMenuStartsAndAttachesExisting(t *testing.T) {
	fc := newFakeClient()
	m := model{client: fc, ctx: context.Background(), state: stateMenu, project: "p", loop: true,
		entries: []menuEntry{{label: "work", mode: "work"}}, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	nm, cmd := m.Update(keyMsg("enter"))
	m = nm.(model)
	if cmd == nil {
		t.Fatal("work (loop) enter did not issue StartWorkLoop")
	}
	msg, ok := cmd().(workLoopMsg)
	if !ok || msg.err != nil {
		t.Fatalf("start result = %#v", msg)
	}
	if fc.startLoopCount != 1 {
		t.Fatalf("StartWorkLoop calls = %d", fc.startLoopCount)
	}

	fc.workLoop = &v1.WorkLoopInfo{State: "running", CurrentSessionId: "existing"}
	fc.startLoopErr = connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("already running"))
	msg, ok = m.startWorkLoop()().(workLoopMsg)
	if !ok || msg.err != nil || !msg.alreadyRunning || msg.info.GetCurrentSessionId() != "existing" {
		t.Fatalf("existing-loop fallback = %#v", msg)
	}
	if fc.getLoopCount != 1 {
		t.Fatalf("GetWorkLoop fallback calls = %d", fc.getLoopCount)
	}
}

// A finished, idle (non-looping) session offers `q` as a clean way back to the
// menu: it flips to stateMenu and stops the still-alive daemon session exactly
// once with the old id (so no orphaned session is left behind).
func TestSessionQReturnsToMenuAndStopsIdle(t *testing.T) {
	m := newSessionTextareaModel(t)
	m.status = "idle"
	m.sessionID = "s1"
	fc := newFakeClient()
	m.client = fc
	m.ctx = context.Background()

	if !m.sessionFinished() {
		t.Fatal("setup: expected an idle non-looping session to be finished")
	}
	updated, cmd := m.Update(keyMsg("q"))
	m = updated.(model)
	if m.state != stateMenu {
		t.Fatalf("q on a finished idle session: state = %v, want stateMenu", m.state)
	}
	if m.sessionID != "" || m.status != "" {
		t.Fatalf("q should clear session id/status, got id=%q status=%q", m.sessionID, m.status)
	}
	runBatch(cmd)
	if fc.stopCount != 1 {
		t.Fatalf("expected StopSession issued exactly once, got %d", fc.stopCount)
	}
	if fc.lastStopped != "s1" {
		t.Fatalf("expected StopSession for s1, got %q", fc.lastStopped)
	}
}

// A finished session whose stream already closed needs no StopSession — the
// session is already gone, so `q` returns to the menu without an RPC.
func TestSessionQReturnsToMenuStreamClosedNoStop(t *testing.T) {
	m := newSessionTextareaModel(t)
	m.status = "stream closed"
	m.sessionID = "s1"
	fc := newFakeClient()
	m.client = fc
	m.ctx = context.Background()

	updated, cmd := m.Update(keyMsg("q"))
	m = updated.(model)
	if m.state != stateMenu {
		t.Fatalf("q on a stream-closed session: state = %v, want stateMenu", m.state)
	}
	runBatch(cmd)
	if fc.stopCount != 0 {
		t.Fatalf("stream-closed session should not issue StopSession, got %d calls", fc.stopCount)
	}
}

// TestQuitGuardMenuImmediate: home menu with no live session → immediate quit.
func TestQuitGuardMenuImmediate(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.state, m.status = stateMenu, "idle"
	if _, cmd := m.Update(keyMsg("ctrl+c")); !isQuit(cmd) {
		t.Fatal("ctrl+c on the home menu with no live session should quit immediately")
	}
}
