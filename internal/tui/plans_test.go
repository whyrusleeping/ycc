package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// A plan proposal is a human-facing Markdown document, not an opaque event
// payload. Its collapsed row summarizes task + first line; expanding it renders
// the plan field itself and never exposes the surrounding JSON envelope.
func TestPlanProposedRendering(t *testing.T) {
	ev := &v1.Event{
		Seq: 12, Type: "plan_proposed", Actor: "coordinator",
		DataJson: `{"task":"0221","plan":"# Implementation\n\n1. Read the renderer.\n2. Render **Markdown**."}`,
	}
	if got := detailLine(ev); !strings.Contains(got, "task 0221") || !strings.Contains(got, "# Implementation") {
		t.Fatalf("detailLine = %q, want task and first plan line", got)
	}

	m := &model{w: 80, bodyCache: map[int]string{}}
	m.makeRenderer()
	body := m.renderBody(ev)
	plain := stripANSI(body)
	if !strings.Contains(plain, "Implementation") || !strings.Contains(plain, "Read the renderer") || !strings.Contains(plain, "Render Markdown") {
		t.Fatalf("expanded plan body missing rendered plan content:\n%s", plain)
	}
	if strings.Contains(plain, `"task"`) || strings.Contains(plain, `"plan"`) || strings.Contains(plain, `0221`) {
		t.Fatalf("expanded plan body leaked JSON envelope:\n%s", plain)
	}

	empty := &v1.Event{Type: "plan_proposed", DataJson: `{"task":"0221"}`}
	if got := m.renderBody(empty); got != "" {
		t.Fatalf("missing plan should have no body, got %q", got)
	}
	malformed := &v1.Event{Type: "plan_proposed", DataJson: `not-json`}
	if got := m.renderBody(malformed); got != "" {
		t.Fatalf("malformed payload should have no body, got %q", got)
	}
}

func TestPlanProposedFoldsProposePlanPlumbing(t *testing.T) {
	m := &model{hiddenCache: map[int]bool{}}
	m.evs = []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"p1","name":"propose_plan","args":"{}"}`},
		{Seq: 2, Type: "plan_proposed", Actor: "coordinator", DataJson: `{"task":"0221","plan":"1. Implement it"}`},
		{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"p1","name":"propose_plan","result":"plan recorded"}`},
	}
	if !m.hiddenRow(0) || !m.hiddenRow(2) || m.hiddenRow(1) {
		t.Fatalf("propose_plan plumbing fold = [%v %v %v], want [true false true]", m.hiddenRow(0), m.hiddenRow(1), m.hiddenRow(2))
	}

	// A failed persistence result remains visible beside the proposal.
	m.evs[2].DataJson = `{"id":"p1","name":"propose_plan","result":"write failed","error":"true"}`
	m.hiddenCache = map[int]bool{}
	if m.hiddenRow(2) {
		t.Fatal("errored propose_plan result must remain visible")
	}
}

// TestPlansBrowser verifies the plan library browser (task 0077): the browse
// selector → plans route lists saved plans, and enter drills into a plan's
// markdown detail; esc/← backs out to the list and esc closes the browser.
func TestPlansBrowser(t *testing.T) {
	f := newFakeClient()
	f.plans = []*v1.PlanSummary{
		{Name: "build-and-test", Title: "Build and test"},
		{Name: "release", Title: "Cut a release"},
	}
	m := initialModel(context.Background(), f, t_tempWorkspace, false)

	// Open the browse selector and route to plans (second entry).
	m = drive(t, m, "ctrl+o")
	m = drive(t, m, "down")
	m = drive(t, m, "enter")
	if !m.plans {
		t.Fatal("plans route should open the plan library browser")
	}
	if len(m.plansList) != 2 {
		t.Fatalf("plansList = %d, want 2", len(m.plansList))
	}
	if got := m.plansView(); !strings.Contains(got, "build-and-test") || !strings.Contains(got, "Cut a release") {
		t.Fatalf("plansView missing entries: %q", got)
	}

	// Enter drills into the first plan's detail.
	m = drive(t, m, "enter")
	if m.planDetail == nil || m.planDetail.Name != "build-and-test" {
		t.Fatalf("enter should load plan detail, got %+v", m.planDetail)
	}
	if got := m.plansView(); !strings.Contains(got, "build-and-test") {
		t.Fatalf("planDetailView missing plan name: %q", got)
	}

	// Esc backs out to the list, then esc closes the browser.
	m = drive(t, m, "esc")
	if m.planDetail != nil {
		t.Fatal("esc should clear plan detail")
	}
	m = drive(t, m, "esc")
	if m.plans {
		t.Fatal("esc should close the plan library browser")
	}
}

// TestPlanDetailScrolls verifies the plan detail view renders through a
// viewport sized to the terminal so long plans scroll instead of overflowing.
func TestPlanDetailScrolls(t *testing.T) {
	m := model{}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(model)
	var body strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&body, "- plan item %d\n", i)
	}
	m.plans = true
	upd2, _ := m.Update(planDetailMsg{plan: &v1.GetPlanResponse{Name: "big", Title: "Big plan", Content: body.String()}})
	m = upd2.(model)

	view := ansi.Strip(m.plansView())
	lines := strings.Split(view, "\n")
	if len(lines) > 12 {
		t.Fatalf("planDetailView produced %d lines for a 12-row terminal:\n%s", len(lines), view)
	}
	if !strings.Contains(view, "plan item 0") {
		t.Errorf("plan detail should start at the top:\n%s", view)
	}
	if strings.Contains(view, "plan item 59") {
		t.Errorf("the tail of a long plan should be off-screen before scrolling:\n%s", view)
	}
	// Scrolling down moves the window: the top line leaves the viewport.
	for i := 0; i < 10; i++ {
		m2, _ := m.updatePlans(tea.KeyPressMsg{Code: tea.KeyPgDown})
		m = m2.(model)
	}
	if view := ansi.Strip(m.plansView()); strings.Contains(view, "plan item 0\n") {
		t.Errorf("pgdown should scroll the plan detail viewport:\n%s", view)
	}
}
