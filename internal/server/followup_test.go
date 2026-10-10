package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func TestSetSessionFollowUpRPC(t *testing.T) {
	ws := t.TempDir()
	lg, err := event.OpenLog(filepath.Join(ws, ".ycc", "sessions", "session", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lg.Record("user", event.UserInput, map[string]any{"text": "remember this"})
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	mgr := session.NewManager(config.NewRegistry(nil), ws)
	defer mgr.ReclaimAll()
	_, handler := yccv1connect.NewSessionServiceHandler(New(mgr), connect.WithInterceptors(NewAuthInterceptor("secret")))
	httpServer := httptest.NewServer(RequireBearer("secret", handler))
	defer httpServer.Close()
	client := yccv1connect.NewSessionServiceClient(httpServer.Client(), httpServer.URL)
	ctx := context.Background()
	request := connect.NewRequest(&v1.SetSessionFollowUpRequest{SessionId: "session", FollowUp: true})
	if _, err := client.SetSessionFollowUp(ctx, request); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated: %v", err)
	}
	request.Header().Set("Authorization", "Bearer secret")
	set, err := client.SetSessionFollowUp(ctx, request)
	if err != nil || !set.Msg.FollowUp {
		t.Fatalf("set: %+v %v", set, err)
	}
	if _, err := time.Parse(time.RFC3339, set.Msg.FollowUpAt); err != nil {
		t.Fatalf("timestamp: %v", err)
	}
	historyRequest := connect.NewRequest(&v1.ListSessionHistoryRequest{})
	historyRequest.Header().Set("Authorization", "Bearer secret")
	history, err := client.ListSessionHistory(ctx, historyRequest)
	if err != nil || len(history.Msg.Sessions) != 1 || !history.Msg.Sessions[0].FollowUp || history.Msg.Sessions[0].FollowUpAt != set.Msg.FollowUpAt {
		t.Fatalf("summary mapping: %+v %v", history, err)
	}
	request.Msg.FollowUp = false
	cleared, err := client.SetSessionFollowUp(ctx, request)
	if err != nil || cleared.Msg.FollowUp || cleared.Msg.FollowUpAt != "" {
		t.Fatalf("clear: %+v %v", cleared, err)
	}
	history, err = client.ListSessionHistory(ctx, historyRequest)
	if err != nil || history.Msg.Sessions[0].FollowUp || history.Msg.Sessions[0].FollowUpAt != "" {
		t.Fatalf("cleared mapping: %+v %v", history, err)
	}
	request.Msg.SessionId = "missing"
	if _, err := client.SetSessionFollowUp(ctx, request); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
	request.Msg.Project = "missing"
	if _, err := client.SetSessionFollowUp(ctx, request); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown project: %v", err)
	}
}
