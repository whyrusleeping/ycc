package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/whyrusleeping/ycc/internal/clientconfig"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// cycle walks the thinking-level list in both directions and wraps around at the
// ends — the behavior the overlay's ←/→ keys rely on.
func TestCycleThinkLevels(t *testing.T) {
	if got := cycle(thinkLevels, "high", 1); got != "xhigh" {
		t.Fatalf("high +1 = %q, want xhigh", got)
	}
	if got := cycle(thinkLevels, "high", -1); got != "medium" {
		t.Fatalf("high -1 = %q, want medium", got)
	}
	if got := cycle(thinkLevels, "max", 1); got != "off" {
		t.Fatalf("max +1 = %q, want off (wrap)", got)
	}
	if got := cycle(thinkLevels, "off", -1); got != "max" {
		t.Fatalf("off -1 = %q, want max (wrap)", got)
	}
	// thinkLevels covers exactly the levels the session layer accepts.
	want := []string{"off", "low", "medium", "high", "xhigh", "max"}
	if strings.Join(thinkLevels, ",") != strings.Join(want, ",") {
		t.Fatalf("thinkLevels = %v, want %v", thinkLevels, want)
	}
}

// The session view must fit exactly within the terminal: every rendered line must
// be no wider than the terminal (so nothing wraps to a second physical row) and
// the total number of lines must equal the terminal height. A wrapping footer or
// header pushes the frame down a row, which is what hides the agent's last output
// line behind the input box (task 0052).
// TestOverlayRendersAsCard checks that modal overlays (settings, backlog) render
// as bordered, centered cards: the rendered View contains rounded-border glyphs
// and no physical line exceeds the terminal width (task 0061).
func TestOverlayRendersAsCard(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*model)
	}{
		{"settings", func(m *model) { m.openOverlay() }},
		{"backlog", func(m *model) {
			m.backlog = true
			m.backlogTasks = []*v1.BacklogTaskSummary{
				{Id: "0001", Status: "todo", Priority: 1, Title: "do a thing", Ready: true},
				{Id: "0002", Status: "doing", Priority: 2, Title: "another task", Ready: false, BlockedBy: []string{"0001"}},
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := model{
				state: stateMenu, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
				thinkLevels: map[string]string{"coordinator": "high", "implementer": "high", "reviewers": "high"},
			}
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			m = updated.(model)
			tc.setup(&m)

			view := m.render()
			if !strings.ContainsAny(view, "╭╰│╮╯") {
				t.Fatalf("%s overlay does not render a rounded border:\n%s", tc.name, view)
			}
			for i, ln := range strings.Split(view, "\n") {
				if w := lipgloss.Width(ln); w > 80 {
					t.Fatalf("%s overlay line %d width %d exceeds terminal width 80: %q", tc.name, i, w, ln)
				}
			}
		})
	}
}

// TestOverlayCoordinatorAppliesImmediately covers the fix for the "role change
// didn't stick" bug: cycling the coordinator with →  in the settings overlay must
// issue SetRoleConfig right away (no separate "apply" step), so the daemon
// persists it. It works even with no active session (empty session_id).
func TestOverlayCoordinatorAppliesImmediately(t *testing.T) {
	f := newFakeClient(
		&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-x"},
		&v1.ModelConfig{Name: "fable", Backend: "anthropic", Model: "claude-fable-5"},
	)
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = runCmds(t, m, m.fetchModels) // populate the model list + seed roles
	m.openOverlay()
	// Move the cursor to the coordinator row and cycle it.
	if m.ovCursor != ovCoord {
		t.Fatalf("cursor = %d, want ovCoord(%d)", m.ovCursor, ovCoord)
	}
	before := m.roleCoord
	m = drive(t, m, "right")
	if m.roleCoord == before {
		t.Fatalf("coordinator did not change from %q", before)
	}
	if f.lastRoleReq == nil {
		t.Fatal("cycling coordinator did not issue SetRoleConfig")
	}
	if f.lastRoleReq.Coordinator != m.roleCoord {
		t.Fatalf("SetRoleConfig coordinator = %q, want %q", f.lastRoleReq.Coordinator, m.roleCoord)
	}
}

// TestOverlayReviewerSubCursorMoves verifies that ←/→ on the reviewers row moves
// the visible sub-cursor with wraparound and does not change the reviewer set.
func TestOverlayReviewerSubCursorMoves(t *testing.T) {
	m := overlayToReviewers(t)
	if m.reviewerSub != 0 {
		t.Fatalf("initial reviewerSub = %d, want 0", m.reviewerSub)
	}
	before := reviewerNames(m)
	m = drive(t, m, "right")
	if m.reviewerSub != 1 {
		t.Fatalf("after right reviewerSub = %d, want 1", m.reviewerSub)
	}
	m = drive(t, m, "right")
	if m.reviewerSub != 2 {
		t.Fatalf("after right reviewerSub = %d, want 2", m.reviewerSub)
	}
	m = drive(t, m, "right") // wrap 2 -> 0
	if m.reviewerSub != 0 {
		t.Fatalf("after wrap reviewerSub = %d, want 0", m.reviewerSub)
	}
	m = drive(t, m, "left") // wrap 0 -> 2
	if m.reviewerSub != 2 {
		t.Fatalf("after left wrap reviewerSub = %d, want 2", m.reviewerSub)
	}
	if got := reviewerNames(m); !reflect.DeepEqual(got, before) {
		t.Fatalf("moving the sub-cursor changed reviewers: %v -> %v", before, got)
	}
}

// TestOverlayReviewerSpaceToggles verifies that space toggles exactly the
// highlighted model, the highlight does not advance, and the change persists.
func TestOverlayReviewerSpaceToggles(t *testing.T) {
	m := overlayToReviewers(t)
	f := m.client.(*fakeClient)
	// Default reviewer set is the first model ("claude") via openOverlay.
	if !m.contains("claude") {
		t.Fatalf("expected default reviewers to contain claude, got %v", m.roleReviewrs)
	}
	// Highlight the second model and add it.
	m = drive(t, m, "right") // sub -> 1 (fable)
	m = drive(t, m, "space")
	if m.reviewerSub != 1 {
		t.Fatalf("highlight advanced after toggle: reviewerSub = %d, want 1", m.reviewerSub)
	}
	if !m.contains("fable") {
		t.Fatalf("space did not add fable: %v", m.roleReviewrs)
	}
	if f.lastRoleReq == nil {
		t.Fatal("toggling reviewer did not persist via SetRoleConfig")
	}
	if !reflect.DeepEqual(f.lastRoleReq.Reviewers, m.roleReviewrs) {
		t.Fatalf("persisted reviewers = %v, want %v", f.lastRoleReq.Reviewers, m.roleReviewrs)
	}
	// Toggle it off again — highlight stays on fable.
	m = drive(t, m, "space")
	if m.contains("fable") {
		t.Fatalf("second space did not remove fable: %v", m.roleReviewrs)
	}
	if m.reviewerSub != 1 {
		t.Fatalf("highlight moved: reviewerSub = %d, want 1", m.reviewerSub)
	}
}

// TestOverlayReviewerInvariant verifies untoggling the last reviewer restores a
// model so a session never points at zero reviewers.
func TestOverlayReviewerInvariant(t *testing.T) {
	m := overlayToReviewers(t)
	f := m.client.(*fakeClient)
	// Default set is just ["claude"], highlighted at index 0. Untoggle it.
	m = drive(t, m, "space")
	if len(m.roleReviewrs) == 0 {
		t.Fatal("non-empty reviewer invariant violated: reviewers is empty")
	}
	if f.lastRoleReq == nil || len(f.lastRoleReq.Reviewers) == 0 {
		t.Fatalf("invariant restore did not persist a non-empty set: %+v", f.lastRoleReq)
	}
}

// TestOverlayReviewerEnterPersists is a regression test: enter on the reviewers
// row must both toggle and persist (previously it toggled without persisting).
func TestOverlayReviewerEnterPersists(t *testing.T) {
	m := overlayToReviewers(t)
	f := m.client.(*fakeClient)
	m = drive(t, m, "right") // highlight fable
	m = drive(t, m, "enter")
	if !m.contains("fable") {
		t.Fatalf("enter did not toggle fable: %v", m.roleReviewrs)
	}
	if f.lastRoleReq == nil {
		t.Fatal("enter on reviewers row did not persist via SetRoleConfig")
	}
	if !reflect.DeepEqual(f.lastRoleReq.Reviewers, m.roleReviewrs) {
		t.Fatalf("persisted reviewers = %v, want %v", f.lastRoleReq.Reviewers, m.roleReviewrs)
	}
}

// TestOverlayReviewerHighlightVisible verifies overlayView highlights the chip
// the next toggle affects when the cursor is on the reviewers row, and renders
// the chips plain when it is not.
func TestOverlayReviewerHighlightVisible(t *testing.T) {
	m := overlayToReviewers(t)
	m = drive(t, m, "right") // highlight fable (index 1)
	// Distinct styling means the raw view differs from the ANSI-stripped view
	// around the highlighted chip.
	view := m.overlayView()
	if !strings.Contains(stripANSI(view), "[ ] fable") {
		t.Fatalf("reviewers row missing fable chip:\n%s", stripANSI(view))
	}
	styled := selStyle.Render("[ ] fable")
	if !strings.Contains(view, styled) {
		t.Fatalf("highlighted chip not styled with selStyle when cursor on reviewers row:\n%s", view)
	}
	// Move the cursor off the reviewers row: the chip should no longer be styled.
	m = drive(t, m, "up") // reviewers -> impl
	view = m.overlayView()
	if strings.Contains(view, styled) {
		t.Fatalf("chip still highlighted when cursor is off the reviewers row:\n%s", view)
	}
}

// TestSettingsOverlayFitsShortTerminal verifies the settings card windows its
// rows around the cursor on terminals shorter than the row list.
func TestSettingsOverlayFitsShortTerminal(t *testing.T) {
	m := model{overlay: true, thinkLevels: map[string]string{}, prefs: clientconfig.Prefs{Theme: "dark"}}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	m = updated.(model)
	m.ovCursor = ovQuit // last row

	view := m.overlayView()
	lines := strings.Split(view, "\n")
	if len(lines) > 10 {
		t.Fatalf("overlayView produced %d lines for a 10-row terminal:\n%s", len(lines), view)
	}
	if !strings.Contains(view, "quit") {
		t.Errorf("cursor row (quit) should stay visible in the window:\n%s", view)
	}
}

// TestEventExpandedDefaults verifies default expansion logic with and without
// the auto-expand-agent-logs preference, and that manual per-row overrides win
// in both directions.
func TestEventExpandedDefaults(t *testing.T) {
	m := &model{expanded: map[int]bool{}}

	// Auto-expand off: normal events collapsed, auto-expand types expanded.
	m.prefs.AutoExpandLogs = false
	if m.eventExpanded(1, "tool_call") {
		t.Fatalf("expected normal event collapsed by default with auto-expand off")
	}
	if !m.eventExpanded(2, "session_idle") {
		t.Fatalf("expected session_idle auto-expanded regardless of preference")
	}

	// Auto-expand on: normal events expanded by default.
	m.prefs.AutoExpandLogs = true
	if !m.eventExpanded(3, "tool_call") {
		t.Fatalf("expected normal event expanded by default with auto-expand on")
	}

	// Manual override beats default: collapse with auto-expand on.
	m.expanded[3] = false
	if m.eventExpanded(3, "tool_call") {
		t.Fatalf("expected manual collapse override to win over auto-expand on")
	}

	// Manual override beats default: expand with auto-expand off.
	m.prefs.AutoExpandLogs = false
	m.expanded[4] = true
	if !m.eventExpanded(4, "tool_call") {
		t.Fatalf("expected manual expand override to win over auto-expand off")
	}
}

// TestToggleWithAutoExpand verifies that toggling a row whose effective state is
// expanded-by-default (auto-expand on) records an explicit collapse override,
// and toggling again re-expands it.
func TestToggleWithAutoExpand(t *testing.T) {
	m := &model{w: 100, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	m.prefs.AutoExpandLogs = true
	m.evs = []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read","args":"{}"}`},
	}
	m.rebuild()

	if !m.eventExpanded(1, "tool_call") {
		t.Fatalf("precondition: event should be expanded by default with auto-expand on")
	}
	m.toggle(0)
	if m.eventExpanded(1, "tool_call") {
		t.Fatalf("expected toggle to collapse an auto-expanded row")
	}
	m.toggle(0)
	if !m.eventExpanded(1, "tool_call") {
		t.Fatalf("expected second toggle to re-expand the row")
	}
}

// TestQuitGuardOverlayRow: the settings-overlay Quit row uses the same guard.
func TestQuitGuardOverlayRow(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.w, m.h = 80, 24
	m.state, m.status, m.sessionID = stateSession, "running", "sess-1"
	m.openOverlay()
	// Point the cursor at the Quit row.
	m.ovCursor = ovQuit

	updated, cmd := m.Update(keyMsg("enter"))
	m = updated.(model)
	if isQuit(cmd) {
		t.Fatal("first activation of the overlay Quit row should NOT quit while running")
	}
	if !m.quitArmed {
		t.Fatal("first overlay Quit activation should arm the guard")
	}

	_, cmd = m.Update(keyMsg("enter"))
	if !isQuit(cmd) {
		t.Fatal("second overlay Quit activation should quit")
	}
}
