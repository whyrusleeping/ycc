package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func TestListSessionHistoryRPCPaging(t *testing.T) {
	ws := t.TempDir()
	for i := 0; i < 3; i++ {
		lg, err := event.OpenLog(filepath.Join(ws, ".ycc", "sessions", fmt.Sprintf("s_%d", i), "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lg.Record("user", event.UserInput, map[string]any{"text": fmt.Sprintf("title %d", i)})
		if err := lg.Close(); err != nil {
			t.Fatal(err)
		}
	}
	mgr := session.NewManager(config.NewRegistry(nil), ws)
	defer mgr.ReclaimAll()
	_, handler := yccv1connect.NewSessionServiceHandler(New(mgr))
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	client := yccv1connect.NewSessionServiceClient(httpServer.Client(), httpServer.URL)
	ctx := context.Background()
	first, err := client.ListSessionHistory(ctx, connect.NewRequest(&v1.ListSessionHistoryRequest{Limit: 1}))
	if err != nil || len(first.Msg.Sessions) != 1 || first.Msg.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := client.ListSessionHistory(ctx, connect.NewRequest(&v1.ListSessionHistoryRequest{Limit: 1, Cursor: first.Msg.NextCursor}))
	if err != nil || len(second.Msg.Sessions) != 1 || second.Msg.Sessions[0].SessionId == first.Msg.Sessions[0].SessionId {
		t.Fatalf("second page: %+v %v", second, err)
	}
	_, err = client.ListSessionHistory(ctx, connect.NewRequest(&v1.ListSessionHistoryRequest{Limit: 1, Cursor: "bad"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad cursor: %v", err)
	}
	full, err := client.ListSessionHistory(ctx, connect.NewRequest(&v1.ListSessionHistoryRequest{}))
	if err != nil || len(full.Msg.Sessions) != 3 || full.Msg.NextCursor != "" {
		t.Fatalf("unbounded: %+v %v", full, err)
	}
}
