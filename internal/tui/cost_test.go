package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// TestCostViewRoute verifies the browse selector (ctrl+o) routes to the cost view
// and that driving the fetch populates the rows (spec §20.5, task 0039).
func TestSubscriptionUsageTUI(t *testing.T) {
	got := subscriptionUsageTUI([]*v1.SubscriptionUsageAccount{{
		Provider: "anthropic", Models: []string{"claude"}, State: "fresh",
		Windows: []*v1.SubscriptionUsageWindow{{Label: "5 hour", UsedPercent: 72.5, ResetsAtUnix: 1784548800}},
	}})
	for _, want := range []string{"Subscription allowance", "anthropic", "claude", "5 hour", "72.5%", "resets"} {
		if !strings.Contains(got, want) {
			t.Fatalf("subscription usage view missing %q:\n%s", want, got)
		}
	}
}

func TestCostViewRoute(t *testing.T) {
	f := newCostFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)

	m = drive(t, m, "ctrl+o")
	// Cost is the fourth browse entry.
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	m = drive(t, m, "enter")
	if m.browse {
		t.Fatal("enter should dismiss the browse selector")
	}
	if !m.cost {
		t.Fatal("fourth browse entry should open the cost view")
	}
	if len(m.costRows) != 2 {
		t.Fatalf("cost rows not populated: got %d, want 2", len(m.costRows))
	}
	if got := m.costGroupBy; len(got) != 1 || got[0] != "task" {
		t.Fatalf("default group-by = %v, want [task]", got)
	}
}

// TestCostView exercises navigation, the group-by cycle, esc, and rendering.
func TestCostView(t *testing.T) {
	f := newCostFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = drive(t, m, "ctrl+o")
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	m = drive(t, m, "enter")

	// "g" cycles task -> model and re-fetches with the new dimension.
	m = drive(t, m, "g")
	if got := m.costGroupBy; len(got) != 1 || got[0] != "model" {
		t.Fatalf("after g, group-by = %v, want [model]", got)
	}
	if got := f.lastGroupBy; len(got) != 1 || got[0] != "model" {
		t.Fatalf("g should re-fetch with [model], lastGroupBy = %v", got)
	}

	// Down then up should clamp within bounds (2 rows -> max cursor 1).
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	if m.costCursor != 1 {
		t.Fatalf("cursor should clamp at 1, got %d", m.costCursor)
	}
	m = drive(t, m, "up")
	m = drive(t, m, "up")
	if m.costCursor != 0 {
		t.Fatalf("cursor should clamp at 0, got %d", m.costCursor)
	}

	// Render: priced cell, unpriced marker, and TOTAL line present.
	out := m.costView()
	if !strings.Contains(out, "$0.1234") {
		t.Errorf("costView should show a priced $ cell:\n%s", out)
	}
	if !strings.Contains(out, "—") {
		t.Errorf("costView should show the unpriced — marker:\n%s", out)
	}
	if !strings.Contains(out, "TOTAL") {
		t.Errorf("costView should show a TOTAL line:\n%s", out)
	}

	// Esc closes the cost view.
	m = drive(t, m, "esc")
	if m.cost {
		t.Fatal("esc should close the cost view")
	}
}

// TestCostViewTaskDrilldown covers the task-filtered agent breakdown, scoped
// grouping cycle, and two-level back navigation (task 0174).
func TestCostViewTaskDrilldown(t *testing.T) {
	f := newCostFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.cost = true
	m.costGroupBy = []string{"task"}
	m.costRows = f.usageRows
	m.costTotal = f.usageTotal
	m.costWorkspace = f.usageWksp
	m.costCursor = 1

	m = drive(t, m, "enter")
	if m.costTask != "0001" || f.lastUsageTask != "0001" {
		t.Fatalf("enter task filter: model=%q request=%q, want 0001", m.costTask, f.lastUsageTask)
	}
	if got := m.costGroupBy; len(got) != 1 || got[0] != "agent" {
		t.Fatalf("drill-down group-by = %v, want [agent]", got)
	}
	view := m.costView()
	for _, want := range []string{"task 0001", "coordinator", "implementer", "reviewer", "TOTAL", "esc back"} {
		if !strings.Contains(view, want) {
			t.Errorf("drill-down view missing %q:\n%s", want, view)
		}
	}

	// Grouping cycles within the task scope and never returns to task.
	m = drive(t, m, "g")
	if got := m.costGroupBy; len(got) != 1 || got[0] != "model" {
		t.Fatalf("drill-down g group-by = %v, want [model]", got)
	}
	if f.lastUsageTask != "0001" {
		t.Fatalf("drill-down g lost task filter: %q", f.lastUsageTask)
	}

	// First esc restores the task table and its cursor; the second closes it.
	m = drive(t, m, "esc")
	if !m.cost || m.costTask != "" || f.lastUsageTask != "" {
		t.Fatalf("esc should return to unfiltered cost table: open=%v task=%q request=%q", m.cost, m.costTask, f.lastUsageTask)
	}
	if got := m.costGroupBy; len(got) != 1 || got[0] != "task" {
		t.Fatalf("restored group-by = %v, want [task]", got)
	}
	if m.costCursor != 1 {
		t.Fatalf("restored cursor = %d, want 1", m.costCursor)
	}
	m = drive(t, m, "esc")
	if m.cost {
		t.Fatal("second esc should close cost view")
	}
}

// TestCostViewIgnoresStaleUsageResponses simulates the filtered drill-down RPC
// completing after the newer parent-table RPC and guards against adopting its
// rows under the restored task heading.
func TestCostViewIgnoresStaleUsageResponses(t *testing.T) {
	f := newCostFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.cost = true
	m.costGroupBy = []string{"task"}
	m.costRows = f.usageRows
	m.costTotal = f.usageTotal
	m.costCursor = 1

	updated, drillCmd := m.Update(keyMsg("enter"))
	drilled := updated.(model)
	if drillCmd == nil {
		t.Fatal("enter should issue the filtered usage request")
	}
	staleFiltered := drillCmd()
	staleGen := staleFiltered.(usageMsg).gen

	updated, parentCmd := drilled.Update(keyMsg("esc"))
	parent := updated.(model)
	if parentCmd == nil {
		t.Fatal("esc should issue the parent-table usage request")
	}
	if parent.costGen <= staleGen {
		t.Fatalf("parent generation = %d, want newer than stale %d", parent.costGen, staleGen)
	}

	// The current parent response applies normally.
	updated, _ = parent.Update(parentCmd())
	parent = updated.(model)
	if len(parent.costRows) != 2 || parent.costRows[1].Task != "0001" || parent.costGroupBy[0] != "task" {
		t.Fatalf("current parent response was not applied: group=%v rows=%+v", parent.costGroupBy, parent.costRows)
	}

	// The older filtered response arriving afterward must change nothing.
	updated, _ = parent.Update(staleFiltered)
	got := updated.(model)
	if got.costTask != "" || len(got.costGroupBy) != 1 || got.costGroupBy[0] != "task" {
		t.Fatalf("stale response changed parent state: task=%q group=%v", got.costTask, got.costGroupBy)
	}
	if len(got.costRows) != 2 || got.costRows[0].Task != "" || got.costRows[1].Task != "0001" {
		t.Fatalf("stale response replaced task-level rows: %+v", got.costRows)
	}
}

// TestCostViewUnattributedNotDrillable makes the empty task row an explicit,
// discoverably unavailable drill-down rather than an accidental unfiltered fetch.
func TestCostViewUnattributedNotDrillable(t *testing.T) {
	f := newCostFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.cost = true
	m.costGroupBy = []string{"task"}
	m.costRows = f.usageRows
	m.costTotal = f.usageTotal
	m.costCursor = 0

	view := m.costView()
	if !strings.Contains(view, "enter n/a for (unattributed)") {
		t.Fatalf("unattributed hint should explain that drill-down is unavailable:\n%s", view)
	}
	m = drive(t, m, "enter")
	if m.costTask != "" || f.lastUsageTask != "" {
		t.Fatalf("unattributed row drilled unexpectedly: model=%q request=%q", m.costTask, f.lastUsageTask)
	}
	if m.costCursor != 0 {
		t.Fatalf("unattributed enter moved cursor: got %d, want 0", m.costCursor)
	}
}

// TestCostViewScrollsWithinTerminal guards the cost-view overflow regression:
// with more usage rows than the terminal is tall, the table must window around
// the cursor (keeping it visible) instead of overrunning the screen, with the
// header and TOTAL rows pinned and a position indicator in the hint.
func TestCostViewScrollsWithinTerminal(t *testing.T) {
	m := model{cost: true, costGroupBy: []string{"task"}}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 14})
	m = updated.(model)
	for i := 0; i < 40; i++ {
		m.costRows = append(m.costRows, &v1.UsageRow{
			Task: fmt.Sprintf("task-%04d", i), Input: 10, Output: 5, Total: 15,
			Cost: 0.01, PriceStatus: "priced",
		})
	}
	m.costTotal = &v1.UsageRow{Input: 400, Output: 200, Total: 600, Cost: 0.4, PriceStatus: "priced"}
	m.costCursor = len(m.costRows) - 1

	view := m.costView()
	lines := strings.Split(view, "\n")
	if len(lines) > 14 {
		t.Fatalf("costView produced %d lines for a 14-row terminal:\n%s", len(lines), view)
	}
	if !strings.Contains(view, "task-0039") {
		t.Errorf("cursor row (last) should stay visible in the window:\n%s", view)
	}
	if !strings.Contains(view, "TOTAL") {
		t.Errorf("TOTAL row should stay pinned when scrolled:\n%s", view)
	}
	if !strings.Contains(view, "/40") {
		t.Errorf("hint should show the scroll position indicator:\n%s", view)
	}
	// Scrolling back to the top brings the first row into view.
	m.costCursor = 0
	if view := m.costView(); !strings.Contains(view, "task-0000") {
		t.Errorf("first row should be visible with the cursor at 0:\n%s", view)
	}
}
