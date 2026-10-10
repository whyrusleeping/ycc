package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestHistoryFollowUpToggleAndFilter(t *testing.T) {
	for _, modal := range []bool{false, true} {
		t.Run(map[bool]string{false: "browser", true: "modal"}[modal], func(t *testing.T) {
			f := newFakeClient()
			f.history = []*v1.SessionSummary{
				{SessionId: "first", Title: "first reply"},
				{SessionId: "second", Title: "second reply"},
			}
			m := initialModel(context.Background(), f, t_tempWorkspace, false)
			m = drive(t, m, "ctrl+r")
			m.project = "project"
			if modal {
				m.state, m.histModal, m.sessionID = stateSession, true, "live"
			}
			// Inspect the optimistic model before executing the RPC.
			updated, cmd := m.Update(keyMsg("f"))
			m = updated.(model)
			if !m.history[0].FollowUp || f.lastFollowUp != nil {
				t.Fatal("flag not optimistic")
			}
			if !strings.Contains(m.historyRows()[0].text, "⚑ ") {
				t.Fatal("flagged row missing marker")
			}
			m = runCmds(t, m, cmd)
			if f.lastFollowUp == nil || f.lastFollowUp.Project != "project" || f.lastFollowUp.SessionId != "first" || !f.lastFollowUp.FollowUp {
				t.Fatalf("RPC: %+v", f.lastFollowUp)
			}
			m = drive(t, m, "F")
			if !m.historyFollowUpOnly || len(m.history) != 1 || m.history[0].SessionId != "first" {
				t.Fatalf("follow-up filter: %+v", m.history)
			}
			view := m.historyView()
			if modal {
				view = m.histModalView()
			}
			if !strings.Contains(view, "sessions flagged for follow-up") {
				t.Fatal("filtered title missing")
			}
			m = drive(t, m, "f")
			if len(m.history) != 0 || f.lastFollowUp.FollowUp {
				t.Fatal("clear did not remove row from follow-up filter")
			}
			m = drive(t, m, "F")
			if m.historyFollowUpOnly || len(m.history) != 2 || m.history[0].FollowUp {
				t.Fatal("unfilter lost rows or retained cleared flag")
			}
			if modal && (m.state != stateSession || m.sessionID != "live") {
				t.Fatal("bookmark disturbed live session")
			}
		})
	}
}

func TestHistoryFollowUpRollback(t *testing.T) {
	f := newFakeClient()
	f.history = []*v1.SessionSummary{{SessionId: "first", FollowUp: true, FollowUpAt: "2026-01-01T00:00:00.000Z"}}
	f.followUpErr = errors.New("write failed")
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = drive(t, m, "ctrl+r")
	m = drive(t, m, "F")
	updated, cmd := m.Update(keyMsg("f"))
	m = updated.(model)
	if len(m.history) != 0 {
		t.Fatal("optimistic clear did not filter out row")
	}
	// Feed the RPC error without running the self-clearing notice timer.
	updated, _ = m.Update(cmd())
	m = updated.(model)
	if len(m.history) != 1 || !m.history[0].FollowUp || m.history[0].FollowUpAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("failed clear did not restore bookmark: %+v", m.history)
	}
	if !strings.Contains(m.historyView(), "write failed") {
		t.Fatal("rollback error is not visible in browser")
	}
}
