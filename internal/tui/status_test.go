package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// TestSessionUsageSummation checks that model_turn usage blocks accumulate per
// model and price correctly: a priced + unpriced mix is "partial" (cost from the
// priced model only), an all-priced mix is "priced", and an all-unpriced mix is
// "unpriced" with no invented cost.
func TestSessionUsageSummation(t *testing.T) {
	newModel := func(pricing map[string]config.Pricing) *model {
		return &model{
			expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
			usageByModel: map[string]event.Usage{}, pricing: pricing,
		}
	}

	// 1) partial: "claude" priced, "local" unpriced.
	priced := config.Pricing{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75, Configured: true}
	m := newModel(map[string]config.Pricing{"claude": priced})
	m.appendEvent(turnEvent(1, "claude", event.Usage{Input: 1000, Output: 500, Total: 1500}))
	m.appendEvent(turnEvent(2, "claude", event.Usage{Input: 2000, Output: 1000, Total: 3000}))
	m.appendEvent(turnEvent(3, "local", event.Usage{Input: 4000, Output: 0, Total: 4000}))
	tokens, cost, status := m.sessionUsage()
	if tokens != 1500+3000+4000 {
		t.Fatalf("tokens = %d, want %d", tokens, 1500+3000+4000)
	}
	// claude: input 3000 * $3/Mtok + output 1500 * $15/Mtok = 0.009 + 0.0225 = 0.0315
	wantCost := (3000*3 + 1500*15) / 1e6
	if d := cost - wantCost; d < -1e-9 || d > 1e-9 {
		t.Fatalf("cost = %v, want %v", cost, wantCost)
	}
	if status != "partial" {
		t.Fatalf("status = %q, want partial", status)
	}

	// 2) fully priced.
	m = newModel(map[string]config.Pricing{"claude": priced})
	m.appendEvent(turnEvent(1, "claude", event.Usage{Input: 1000, Output: 1000, Total: 2000}))
	tokens, cost, status = m.sessionUsage()
	if tokens != 2000 || status != "priced" {
		t.Fatalf("priced case: tokens=%d status=%q", tokens, status)
	}
	if want := (1000*3 + 1000*15) / 1e6; cost != want {
		t.Fatalf("priced cost = %v, want %v", cost, want)
	}

	// 3) fully unpriced: tokens surface but cost stays 0 and status unpriced.
	m = newModel(map[string]config.Pricing{})
	m.appendEvent(turnEvent(1, "local", event.Usage{Input: 500, Output: 500, Total: 1000}))
	tokens, cost, status = m.sessionUsage()
	if tokens != 1000 || cost != 0 || status != "unpriced" {
		t.Fatalf("unpriced case: tokens=%d cost=%v status=%q", tokens, cost, status)
	}

	// 4) empty: no usage at all.
	m = newModel(map[string]config.Pricing{})
	if tk, c, st := m.sessionUsage(); tk != 0 || c != 0 || st != "unpriced" {
		t.Fatalf("empty case: %d %v %q", tk, c, st)
	}
}

// TestStatusBarSegments renders the status bar with a fully-populated session and
// asserts the distinct segments (mode, model, thinking, token readout) appear in
// the intended order and verifies the removed policy segment stays gone. It also
// verifies the bar stays exactly one physical row at a narrow width (no wrap).
func TestStatusBarSegments(t *testing.T) {
	m := model{
		state: stateSession, status: "running", mode: "implement",
		sessionID: "sess12345678", w: 120, roleCoord: "claude-opus",
		thinkLevels:  map[string]string{"coordinator": "high"},
		usageByModel: map[string]event.Usage{"claude": {Input: 12000, Output: 6000, Total: 18000}},
		pricing:      map[string]config.Pricing{"claude": {Input: 3, Output: 15, Configured: true}},
	}
	bar := m.statusBar()
	for _, want := range []string{"implement", "claude-opus", "high", "18.0k", "$"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("status bar missing %q:\n%s", want, bar)
		}
	}
	if strings.Contains(bar, "lvl ") {
		t.Fatalf("status bar must not show the removed policy segment:\n%s", bar)
	}
	if modelAt, reasoningAt := strings.Index(bar, "claude-opus"), strings.Index(bar, "high"); modelAt < 0 || reasoningAt < 0 || modelAt > reasoningAt {
		t.Fatalf("coordinator model must appear before reasoning level:\n%s", bar)
	}

	// A recorded coordinator turn is authoritative and updates the bar on live
	// delivery and replay; non-coordinator turns must not replace it.
	m.expanded, m.bodyCache, m.blockCache, m.hiddenCache = map[int]bool{}, map[int]string{}, map[int]string{}, map[int]bool{}
	m.appendEvent(&v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"model_name":"gpt-5.6-sol","text":"hello"}`})
	if got := m.roleCoord; got != "gpt-5.6-sol" {
		t.Fatalf("coordinator turn set roleCoord=%q, want gpt-5.6-sol", got)
	}
	m.appendEvent(&v1.Event{Seq: 2, Type: "model_turn", Actor: "implementer", DataJson: `{"model_name":"claude-sonnet","text":"done"}`})
	if got := m.roleCoord; got != "gpt-5.6-sol" {
		t.Fatalf("implementer turn changed roleCoord=%q", got)
	}

	// Single physical row at a narrow width: no newline and width within bound.
	m.w = 40
	bar = m.statusBar()
	if strings.Contains(bar, "\n") {
		t.Fatalf("status bar wrapped to multiple rows: %q", bar)
	}
	if w := lipgloss.Width(bar); w > 40 {
		t.Fatalf("status bar width %d exceeds 40: %q", w, bar)
	}

	// Unpriced session: tokens shown, never a bogus cost.
	m.w = 120
	m.pricing = map[string]config.Pricing{}
	bar = m.statusBar()
	if strings.Contains(bar, "$") {
		t.Fatalf("unpriced bar must not show a cost: %s", bar)
	}
	if !strings.Contains(bar, "18.0k") {
		t.Fatalf("unpriced bar should still show tokens: %s", bar)
	}
}

// TestStatusBarShowsFocusedTask: a task_focus event surfaces which backlog task
// the session is working on in the header — id plus (truncated) title when the
// event carries one — and a later focus replaces the readout.
func TestStatusBarShowsFocusedTask(t *testing.T) {
	m := model{
		state: stateSession, status: "running", mode: "work", w: 120,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	m.appendEvent(&v1.Event{Seq: 1, Type: "task_focus", Actor: "coordinator", DataJson: `{"task":"0007","title":"Fix the frobnicator"}`})
	bar := m.statusBar()
	for _, want := range []string{"task", "0007", "Fix the frobnicator"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("status bar missing %q after task_focus:\n%s", want, bar)
		}
	}

	// A new focus replaces the old one; a title-less event still shows the id.
	m.appendEvent(&v1.Event{Seq: 2, Type: "task_focus", Actor: "coordinator", DataJson: `{"task":"0009"}`})
	bar = m.statusBar()
	if !strings.Contains(bar, "0009") || strings.Contains(bar, "0007") {
		t.Fatalf("status bar should show the new focus 0009 only:\n%s", bar)
	}
}

// The status bar shows a visually distinct budget segment: a warn readout once a
// session crosses ~80% of its cap, escalating to "budget reached" on breach
// (task 0137, spec §20.6).
func TestStatusBarBudgetSegment(t *testing.T) {
	m := model{state: stateSession, status: "running", mode: "work", w: 160, budgetPct: 0.85}
	bar := m.statusBar()
	if !strings.Contains(bar, "budget 85%") {
		t.Fatalf("status bar missing budget warning:\n%s", bar)
	}
	if strings.Contains(bar, "budget reached") {
		t.Fatalf("warn state should not show 'budget reached':\n%s", bar)
	}

	m.budgetExceeded = true
	bar = m.statusBar()
	if !strings.Contains(bar, "budget reached") {
		t.Fatalf("status bar missing budget breach:\n%s", bar)
	}
}
