package tui

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// TestSpawnRequiresProject covers the daemon-registry guard: with no project the
// spawn is refused with an explanatory notice and no RPC fires (task 0085).
func TestSpawnRequiresProject(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.project = "" // one-shot: no registered project
	m.backlog = true
	m.backlogTasks = []*v1.BacklogTaskSummary{{Id: "0001", Status: "todo", Title: "alpha"}}

	m = drive(t, m, "space")
	m = drive(t, m, "P")
	if len(f.spawnReqs) != 0 {
		t.Fatalf("SpawnWorkstream should not fire without a project, got %d", len(f.spawnReqs))
	}
	if !strings.Contains(m.backlogNotice, "project") {
		t.Fatalf("notice = %q, want an explanation about needing a project", m.backlogNotice)
	}
}

// TestWorkstreamsPanelConflictRow proves a conflict is a visually distinct row
// state (design §8): a workstream with a locally-known conflict renders loudly,
// never a silent normal status.
func TestWorkstreamsPanelConflictRow(t *testing.T) {
	m := model{
		ws: true,
		wsList: []*v1.WorkstreamInfo{
			{Id: "ws_1", TaskId: "0001", Branch: "ycc/ws/ws_1", CommitCount: 3, SessionStatus: "running", Status: "active"},
			{Id: "ws_2", TaskId: "0002", Branch: "ycc/ws/ws_2", CommitCount: 1, SessionStatus: "idle", Status: "active"},
		},
		wsLocal: map[string]string{"ws_2": "conflict"},
	}

	// wsRowStatus precedence: normal running for ws_1, conflict for ws_2.
	if s, conflict := m.wsRowStatus(m.wsList[0]); conflict || s != "running" {
		t.Fatalf("ws_1 status = (%q,%v), want (running,false)", s, conflict)
	}
	if s, conflict := m.wsRowStatus(m.wsList[1]); !conflict || s != "conflict" {
		t.Fatalf("ws_2 status = (%q,%v), want (conflict,true)", s, conflict)
	}

	view := m.workstreamsView()
	if !strings.Contains(view, "conflict") {
		t.Fatalf("panel view missing a conflict row:\n%s", view)
	}
	if !strings.Contains(view, "running") {
		t.Fatalf("panel view missing the running row:\n%s", view)
	}
}

// TestWorkstreamMergeFlow covers preview → accept → merged (task 0085, design §6).
func TestWorkstreamCompletionRows(t *testing.T) {
	m := model{wsLocal: map[string]string{}}
	if status, loud := m.wsRowStatus(&v1.WorkstreamInfo{Status: "ready", SessionStatus: "idle"}); status != "ready" || loud {
		t.Fatalf("ready row = %q, %v", status, loud)
	}
	if status, loud := m.wsRowStatus(&v1.WorkstreamInfo{Status: "needs_attention", SessionStatus: "stopped"}); status != "⚠ needs attention" || !loud {
		t.Fatalf("attention row = %q, %v", status, loud)
	}
}

func TestWorkstreamMergeFlow(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.ws = true
	m.wsList = []*v1.WorkstreamInfo{{Id: "ws_1", Branch: "ycc/ws/ws_1", SessionId: "s1", Status: "active"}}

	// m opens the merge overlay via PreviewMerge (clean by default).
	m = drive(t, m, "m")
	if f.lastPreviewID != "ws_1" {
		t.Fatalf("PreviewMerge id = %q, want ws_1", f.lastPreviewID)
	}
	if m.wsMerge == nil || !m.wsMerge.GetClean() {
		t.Fatalf("expected a clean merge overlay, got %+v", m.wsMerge)
	}

	// enter accepts the clean merge; the row merges with a commit sha.
	m = drive(t, m, "enter")
	if f.lastMergeID != "ws_1" {
		t.Fatalf("MergeWorkstream id = %q, want ws_1", f.lastMergeID)
	}
	if m.wsMerge != nil {
		t.Fatal("merge overlay should close after a successful merge")
	}
	if !strings.Contains(m.wsNotice, "merged") {
		t.Fatalf("notice = %q, want a merged confirmation", m.wsNotice)
	}
}

// TestWorkstreamMergeConflict proves a conflicted preview cannot be silently
// merged: the overlay stays, the row is marked conflict, and enter is refused.
func TestWorkstreamMergeConflict(t *testing.T) {
	f := newFakeClient()
	f.previewResp = &v1.PreviewMergeResponse{Clean: false, Conflicts: []string{"shared.txt"}}
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.ws = true
	m.wsList = []*v1.WorkstreamInfo{{Id: "ws_1", Branch: "ycc/ws/ws_1", SessionId: "s1", Status: "active"}}

	m = drive(t, m, "m")
	if m.wsMerge == nil || m.wsMerge.GetClean() {
		t.Fatalf("expected a conflicted overlay, got %+v", m.wsMerge)
	}
	if m.wsLocal["ws_1"] != "conflict" {
		t.Fatalf("wsLocal[ws_1] = %q, want conflict", m.wsLocal["ws_1"])
	}

	// enter must not merge a conflicted preview.
	m = drive(t, m, "enter")
	if f.lastMergeID != "" {
		t.Fatalf("MergeWorkstream fired on a conflicted preview (id=%q)", f.lastMergeID)
	}
}

// TestWorkstreamDiscardConfirm covers the two-step discard confirm (task 0085).
func TestWorkstreamDiscardConfirm(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.ws = true
	m.wsList = []*v1.WorkstreamInfo{{Id: "ws_1", Branch: "ycc/ws/ws_1", SessionId: "s1", Status: "active"}}

	// d arms the confirm; no RPC yet.
	m = drive(t, m, "d")
	if m.wsDiscardID != "ws_1" {
		t.Fatalf("wsDiscardID = %q, want ws_1", m.wsDiscardID)
	}
	if f.lastDiscardID != "" {
		t.Fatal("discard fired before confirmation")
	}

	// y confirms the discard.
	m = drive(t, m, "y")
	if f.lastDiscardID != "ws_1" {
		t.Fatalf("DiscardWorkstream id = %q, want ws_1", f.lastDiscardID)
	}
	if m.wsDiscardID != "" {
		t.Fatal("discard confirm should clear after firing")
	}
}

// TestWorkstreamDiscardCancel proves any non-y key cancels the discard confirm.
func TestWorkstreamDiscardCancel(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.ws = true
	m.wsList = []*v1.WorkstreamInfo{{Id: "ws_1", Branch: "ycc/ws/ws_1", SessionId: "s1", Status: "active"}}

	m = drive(t, m, "d")
	m = drive(t, m, "n")
	if f.lastDiscardID != "" {
		t.Fatal("discard should not fire when cancelled")
	}
	if m.wsDiscardID != "" {
		t.Fatal("discard confirm should clear on cancel")
	}
}

// TestWorkstreamDrillIntoSession proves enter on a row attaches to its session
// via ResumeSession (task 0085, design §8).
func TestWorkstreamDrillIntoSession(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.ws = true
	m.wsList = []*v1.WorkstreamInfo{{Id: "ws_1", Branch: "ycc/ws/ws_1", SessionId: "s-ws-1", Status: "active"}}

	updated, cmd := m.updateWorkstreams(keyMsg("enter"))
	m = updated.(model)
	_ = runCmds(t, m, cmd)
	if f.lastReopened != "s-ws-1" {
		t.Fatalf("ResumeSession id = %q, want s-ws-1", f.lastReopened)
	}
}

// TestBrowseWorkstreamsRoute proves the browse selector routes to the panel.
func TestBrowseWorkstreamsRoute(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = drive(t, m, "ctrl+o")
	if !m.browse {
		t.Fatal("ctrl+o should open the browse selector")
	}
	// Navigate to the "workstreams" route.
	idx := -1
	for i, t := range browseTargets {
		if t.label == "workstreams" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("workstreams route missing from browseTargets")
	}
	for i := 0; i < idx; i++ {
		m = drive(t, m, "down")
	}
	m = drive(t, m, "enter")
	if !m.ws {
		t.Fatal("workstreams route should open the Workstreams panel")
	}
}
