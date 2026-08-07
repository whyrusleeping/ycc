package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestDigestFromWorkLoop(t *testing.T) {
	started := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	d := digestFromWorkLoop(&v1.WorkLoopInfo{
		Outcome: "loop complete", StartedAt: started, TotalTokens: 30, TotalCost: 1.25, CostStatus: "partial",
		Sessions:  []*v1.WorkLoopSession{{SessionId: "s1", Focus: "0001", Tokens: 30, Cost: .5, PriceStatus: "priced"}},
		Completed: []*v1.WorkLoopDigestTask{{Id: "0001", Title: "done", Status: "done", Sha: "abcdef123", VerdictTally: "approve×1", Tokens: 20, Cost: .4, PriceStatus: "priced"}},
		Blocked:   []*v1.WorkLoopDigestTask{{Id: "0002", Title: "blocked", Status: "blocked", Reason: "needs key", Tokens: 10, PriceStatus: "unpriced"}},
		InReview:  []*v1.WorkLoopDigestTask{{Id: "0003"}}, Created: []*v1.WorkLoopDigestTask{{Id: "0004"}},
	})
	if d == nil || d.outcome != "loop complete" || d.totalTokens != 30 || d.totalCost != 1.25 || d.costStatus != "partial" {
		t.Fatalf("bad totals/outcome: %+v", d)
	}
	if len(d.sessions) != 1 || d.sessions[0].id != "s1" || d.sessions[0].cost != .5 {
		t.Fatalf("bad sessions: %+v", d.sessions)
	}
	if len(d.completed) != 1 || d.completed[0].sha != "abcdef123" || d.completed[0].verdictTally != "approve×1" || d.completed[0].priceStatus != "priced" {
		t.Fatalf("bad completed mapping: %+v", d.completed)
	}
	if len(d.blocked) != 1 || d.blocked[0].reason != "needs key" || len(d.inReview) != 1 || len(d.created) != 1 {
		t.Fatalf("bad sections: blocked=%+v review=%+v created=%+v", d.blocked, d.inReview, d.created)
	}
}

func TestWorkLoopMessagesAndTick(t *testing.T) {
	fc := newFakeClient()
	m := model{client: fc, ctx: context.Background(), state: stateMenu, project: "p", loopSeq: 4,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}

	nm, cmd := m.Update(workLoopMsg{info: &v1.WorkLoopInfo{State: "running", CurrentSessionId: "s-loop"}, seq: 4})
	m = nm.(model)
	if !m.looping || cmd == nil {
		t.Fatalf("running snapshot did not attach/poll: looping=%v cmd=%v", m.looping, cmd)
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("running snapshot command = %T, want batch", cmd())
	}
	for _, c := range batch {
		if c != nil {
			if started, ok := c().(startedMsg); ok && started.id != "s-loop" {
				t.Fatalf("attached session = %q", started.id)
			}
		}
	}
	if fc.lastReopened != "s-loop" {
		t.Fatalf("did not reopen current loop session: %q", fc.lastReopened)
	}

	m.sessionID = "s-loop"
	nm, _ = m.Update(workLoopMsg{info: &v1.WorkLoopInfo{State: "running", CurrentSessionId: "s-loop"}, seq: m.loopSeq})
	m = nm.(model)
	if fc.lastReopened != "s-loop" { // unchanged; no second call to distinguish below
		t.Fatal("unexpected reopen state")
	}

	seq := m.loopSeq
	nm, stale := m.Update(loopTickMsg{seq: seq - 1})
	m = nm.(model)
	if stale != nil {
		t.Fatal("stale loop tick was not ignored")
	}
	_, fresh := m.Update(loopTickMsg{seq: seq})
	if fresh == nil {
		t.Fatal("current loop tick did not fetch")
	}

	nm, _ = m.Update(workLoopMsg{info: &v1.WorkLoopInfo{State: "finished", Outcome: "loop complete", Completed: []*v1.WorkLoopDigestTask{{Id: "1"}}}, seq: m.loopSeq})
	m = nm.(model)
	if m.looping || !m.digest || m.state != stateMenu || m.status != "loop complete" || m.loopDigest == nil {
		t.Fatalf("finished snapshot not surfaced: looping=%v digest=%v state=%v status=%q", m.looping, m.digest, m.state, m.status)
	}
}

func TestWorkLoopDigestReadStaysModalOverLiveSession(t *testing.T) {
	for _, looping := range []bool{false, true} {
		t.Run(fmt.Sprintf("looping=%v", looping), func(t *testing.T) {
			fc := newFakeClient()
			fc.workLoop = &v1.WorkLoopInfo{State: "finished", Outcome: "old loop complete", TotalTokens: 42}
			m := model{client: fc, ctx: context.Background(), state: stateSession, status: "running",
				looping: looping, loopSeq: 9, browse: true,
				expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
			for i, target := range browseTargets {
				if target.label == "digest" {
					m.browseCursor = i
				}
			}

			nm, cmd := m.updateBrowse(keyMsg("enter"))
			m = nm.(model)
			if cmd == nil {
				t.Fatal("Browse → digest did not issue GetWorkLoop")
			}
			nm, follow := m.Update(cmd())
			m = nm.(model)
			if follow != nil || !m.digest || m.loopDigest == nil {
				t.Fatalf("digest read did not open modal: digest=%v cmd=%v", m.digest, follow)
			}
			if m.state != stateSession || m.status != "running" || m.looping != looping || m.loopSeq != 9 {
				t.Fatalf("digest read changed lifecycle: state=%v status=%q looping=%v seq=%d", m.state, m.status, m.looping, m.loopSeq)
			}
		})
	}
}

func TestWorkLoopFinishedStartResponseSurfacesDigest(t *testing.T) {
	fc := newFakeClient()
	m := model{client: fc, ctx: context.Background(), state: stateMenu, project: "p", status: "loop starting…", loopSeq: 7,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	info := &v1.WorkLoopInfo{State: "finished", Outcome: "loop complete: no ready tasks remain", TotalTokens: 12}
	nm, cmd := m.Update(workLoopMsg{info: info, initiated: true})
	m = nm.(model)
	if m.looping || !m.digest || m.loopDigest == nil || m.status != info.Outcome || m.state != stateMenu {
		t.Fatalf("finished Start response not surfaced: looping=%v digest=%v status=%q state=%v", m.looping, m.digest, m.status, m.state)
	}
	if cmd == nil {
		t.Fatal("finished Start response did not refresh the menu")
	}
	if m.loopSeq != 8 {
		t.Fatalf("initiated response generation = %d, want 8", m.loopSeq)
	}
}

func TestWorkLoopStalePollCannotDetachOrMultiplyTimers(t *testing.T) {
	fc := newFakeClient()
	m := model{client: fc, ctx: context.Background(), state: stateMenu, project: "p", loopSeq: 3,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}

	// A newer user Start response advances the generation and attaches the loop.
	nm, startCmd := m.Update(workLoopMsg{info: &v1.WorkLoopInfo{State: "running", CurrentSessionId: "s-new"}, initiated: true})
	m = nm.(model)
	if !m.looping || m.loopSeq != 4 || startCmd == nil {
		t.Fatalf("start did not establish generation: looping=%v seq=%d cmd=%v", m.looping, m.loopSeq, startCmd)
	}

	// A GetWorkLoop issued under generation 3 must not clear the newer attachment,
	// regardless of whether it reports nil or an old finished loop.
	nm, staleCmd := m.Update(workLoopMsg{seq: 3})
	m = nm.(model)
	if !m.looping || m.loopSeq != 4 || staleCmd != nil {
		t.Fatalf("stale nil poll detached loop: looping=%v seq=%d cmd=%v", m.looping, m.loopSeq, staleCmd)
	}
	nm, staleCmd = m.Update(workLoopMsg{seq: 3, info: &v1.WorkLoopInfo{State: "finished", Outcome: "old"}})
	m = nm.(model)
	if !m.looping || m.loopSeq != 4 || staleCmd != nil || m.status == "old" {
		t.Fatalf("stale finished poll applied: looping=%v seq=%d status=%q cmd=%v", m.looping, m.loopSeq, m.status, staleCmd)
	}

	// Neither a stale timer nor a stale running response may arm another chain.
	_, staleTickCmd := m.Update(loopTickMsg{seq: 3})
	if staleTickCmd != nil {
		t.Fatal("stale tick armed a poll")
	}
	_, staleRunningCmd := m.Update(workLoopMsg{seq: 3, info: &v1.WorkLoopInfo{State: "running"}})
	if staleRunningCmd != nil {
		t.Fatal("stale running poll armed a second timer chain")
	}
}

func TestWorkLoopErrorKeepsPolling(t *testing.T) {
	m := model{looping: true, loopSeq: 2, connected: true}
	nm, cmd := m.Update(workLoopMsg{err: fmt.Errorf("temporary"), seq: 2, continuePoll: true})
	m = nm.(model)
	if !m.looping || cmd == nil || m.loopSeq != 2 {
		t.Fatalf("error dropped polling: looping=%v cmd=%v seq=%d", m.looping, cmd, m.loopSeq)
	}
}

func TestWorkLoopShiftTabArmStop(t *testing.T) {
	fc := newFakeClient()
	m := newSessionTextareaModel(t)
	m.client, m.ctx, m.project, m.mode = fc, context.Background(), "p", "work"

	nm, cmd := m.Update(keyMsg("shift+tab"))
	m = nm.(model)
	if !m.loopArmed || cmd != nil {
		t.Fatalf("plain work session did not arm: armed=%v cmd=%v", m.loopArmed, cmd)
	}
	nm, _ = m.Update(keyMsg("shift+tab"))
	m = nm.(model)
	if m.loopArmed {
		t.Fatal("second shift+tab did not disarm")
	}
	m.loopArmed = true
	nm, cmd = m.Update(streamClosedMsg{})
	m = nm.(model)
	if cmd == nil || m.loopArmed {
		t.Fatal("armed stream close did not start loop")
	}
	_ = cmd()
	if fc.startLoopCount != 1 {
		t.Fatalf("StartWorkLoop calls = %d", fc.startLoopCount)
	}

	m.looping = true
	nm, cmd = m.Update(keyMsg("shift+tab"))
	m = nm.(model)
	if cmd == nil || !strings.Contains(m.status, "stopping") {
		t.Fatal("running loop did not request graceful stop")
	}
	_ = cmd()
	if fc.stopLoopCount != 1 {
		t.Fatalf("StopWorkLoop calls = %d", fc.stopLoopCount)
	}
}
