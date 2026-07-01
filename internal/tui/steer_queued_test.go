package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// A queued mid-run user_input renders "(queued)" until its matching
// user_input_delivered event arrives, at which point the suffix disappears and
// the delivery marker itself renders no row (spec §18.7).
func TestQueuedUserInputRendering(t *testing.T) {
	m := model{
		state: stateSession, status: "running", follow: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		deliveredSeqs: map[int64]bool{},
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(model)

	queued := &v1.Event{Seq: 5, Type: "user_input", Actor: "user", DataJson: `{"text":"wrong file","queued":true}`}
	m.appendEvent(queued)

	// Before delivery: the detail line carries a "(queued)" marker.
	if got := stripANSI(m.detailLineFor(queued)); !strings.Contains(got, "(queued)") {
		t.Fatalf("queued detail = %q, want a (queued) marker", got)
	}

	// The delivery event upgrades the echo and is itself a hidden row.
	m.appendEvent(&v1.Event{Seq: 8, Type: "user_input_delivered", Actor: "user", DataJson: `{"seq":5,"text":"wrong file"}`})
	m.rebuild()

	if !m.deliveredSeqs[5] {
		t.Fatal("delivered set did not record seq 5")
	}
	if got := stripANSI(m.detailLineFor(queued)); strings.Contains(got, "(queued)") {
		t.Fatalf("delivered detail = %q, should not carry (queued)", got)
	}
	// The delivery marker (index 1) is hidden: shares the previous row's start.
	if !m.hiddenRow(1) {
		t.Fatal("user_input_delivered should be a hidden row")
	}
}
