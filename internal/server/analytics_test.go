package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/session"
	"github.com/whyrusleeping/ycc/internal/uianalytics"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestUiAnalyticsRPCs(t *testing.T) {
	ctx := context.Background()
	batch := &v1.RecordUiEventsRequest{Client: "web", VisitId: "v", Events: []*v1.UiEvent{
		{TimeMs: time.Now().UnixMilli(), Kind: "view", Name: "backlog"},
		{Kind: "action", Name: "not an identifier"},
	}}

	// Without a store (one-shot daemon) events validate but are discarded.
	srv := New(session.NewManager(testRegistry(), t.TempDir()))
	resp, err := srv.RecordUiEvents(ctx, connect.NewRequest(batch))
	if err != nil || resp.Msg.Stored || resp.Msg.Accepted != 1 || resp.Msg.Dropped != 1 {
		t.Fatalf("no-store RecordUiEvents = %+v, %v", resp, err)
	}
	if rep, err := srv.GetUiAnalytics(ctx, connect.NewRequest(&v1.GetUiAnalyticsRequest{})); err != nil || rep.Msg.Enabled {
		t.Fatalf("no-store GetUiAnalytics = %+v, %v", rep, err)
	}

	store, err := uianalytics.Open(filepath.Join(t.TempDir(), "analytics"))
	if err != nil {
		t.Fatal(err)
	}
	srv.SetAnalytics(store)
	resp, err = srv.RecordUiEvents(ctx, connect.NewRequest(batch))
	if err != nil || !resp.Msg.Stored || resp.Msg.Accepted != 1 {
		t.Fatalf("RecordUiEvents = %+v, %v", resp, err)
	}
	_, err = srv.RecordUiEvents(ctx, connect.NewRequest(&v1.RecordUiEventsRequest{Client: "bad client"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad client: %v", err)
	}
	rep, err := srv.GetUiAnalytics(ctx, connect.NewRequest(&v1.GetUiAnalyticsRequest{Days: 7}))
	if err != nil || !rep.Msg.Enabled || rep.Msg.Days != 7 || len(rep.Msg.Rows) != 1 || rep.Msg.Rows[0].Name != "backlog" {
		t.Fatalf("GetUiAnalytics = %+v, %v", rep, err)
	}
	if _, err := srv.GetUiAnalytics(ctx, connect.NewRequest(&v1.GetUiAnalyticsRequest{Days: -1})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("negative days: %v", err)
	}
}
