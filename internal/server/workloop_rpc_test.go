package server

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestStartWorkLoopUnknownProject(t *testing.T) {
	srv := New(session.NewManager(testRegistry(), t.TempDir()))
	_, err := srv.StartWorkLoop(context.Background(), connect.NewRequest(&v1.StartWorkLoopRequest{Project: "nope"}))
	if err == nil {
		t.Fatal("expected error for unknown project")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
}

func TestGetWorkLoopNoneReturnsEmpty(t *testing.T) {
	srv := New(session.NewManager(testRegistry(), t.TempDir()))
	resp, err := srv.GetWorkLoop(context.Background(), connect.NewRequest(&v1.GetWorkLoopRequest{}))
	if err != nil {
		t.Fatalf("GetWorkLoop: %v", err)
	}
	if resp.Msg.Loop != nil {
		t.Fatalf("expected nil loop, got %+v", resp.Msg.Loop)
	}
}

func TestStopWorkLoopNoneReturnsEmpty(t *testing.T) {
	srv := New(session.NewManager(testRegistry(), t.TempDir()))
	resp, err := srv.StopWorkLoop(context.Background(), connect.NewRequest(&v1.StopWorkLoopRequest{}))
	if err != nil {
		t.Fatalf("StopWorkLoop: %v", err)
	}
	if resp.Msg.Loop != nil {
		t.Fatalf("expected nil loop, got %+v", resp.Msg.Loop)
	}
}

func TestWorkLoopToProto(t *testing.T) {
	if workLoopToProto(nil) != nil {
		t.Fatal("expected nil for nil loop")
	}
	started := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	wl := &session.WorkLoop{
		LoopID:           "loop_abcd",
		Project:          "demo",
		State:            "finished",
		CurrentSessionID: "s1",
		Outcome:          "loop complete: no ready tasks remain",
		StartedAt:        started,
		SessionsRun:      2,
		Sessions: []session.WorkLoopSession{
			{SessionID: "s1", Focus: "0001", Tokens: 100, Cost: 0.01, PriceStatus: "priced"},
		},
		Completed: []session.WorkLoopDigestTask{
			{ID: "0001", Title: "First", Status: "done", SHA: "abc", VerdictTally: "approve×2", Tokens: 100, Cost: 0.01, PriceStatus: "priced"},
		},
		Blocked: []session.WorkLoopDigestTask{
			{ID: "0002", Title: "Second", Status: "blocked", Reason: "blocked: needs input"},
		},
		TotalTokens: 100,
		TotalCost:   0.01,
		CostStatus:  "priced",
	}
	info := workLoopToProto(wl)
	if info.LoopId != "loop_abcd" || info.State != "finished" || info.CurrentSessionId != "s1" {
		t.Fatalf("scalar fields wrong: %+v", info)
	}
	if info.StartedAt != "2026-07-15T10:00:00Z" {
		t.Fatalf("started_at = %q", info.StartedAt)
	}
	if info.SessionsRun != 2 || len(info.Sessions) != 1 || info.Sessions[0].SessionId != "s1" {
		t.Fatalf("sessions wrong: %+v", info.Sessions)
	}
	if len(info.Completed) != 1 || info.Completed[0].VerdictTally != "approve×2" {
		t.Fatalf("completed wrong: %+v", info.Completed)
	}
	if len(info.Blocked) != 1 || info.Blocked[0].Reason != "blocked: needs input" {
		t.Fatalf("blocked wrong: %+v", info.Blocked)
	}
	if info.TotalTokens != 100 || info.TotalCost != 0.01 || info.CostStatus != "priced" {
		t.Fatalf("totals wrong: %+v", info)
	}
}
