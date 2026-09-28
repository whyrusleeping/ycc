package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// A quiet SubscribeSessionView stream must still carry periodic empty updates
// so client transport idle timers (iOS URLSession, tunnels) don't reap it.
func TestSubscribeSessionViewSendsKeepaliveWhenQuiet(t *testing.T) {
	srv, workspace := newImageTestServer(t)
	defer srv.mgr.ReclaimAll()
	srv.viewKeepalive = 30 * time.Millisecond
	ctx := context.Background()
	started, err := srv.StartSession(ctx, connect.NewRequest(&v1.StartSessionRequest{Workspace: workspace, Mode: "chat"}))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer srv.StopSession(ctx, connect.NewRequest(&v1.StopSessionRequest{SessionId: started.Msg.SessionId})) //nolint:errcheck

	_, handler := yccv1connect.NewSessionServiceHandler(srv)
	httpServer := httptest.NewUnstartedServer(handler)
	httpServer.EnableHTTP2 = true
	httpServer.StartTLS()
	defer httpServer.Close()
	client := yccv1connect.NewSessionServiceClient(httpServer.Client(), httpServer.URL)

	streamCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stream, err := client.SubscribeSessionView(streamCtx, connect.NewRequest(&v1.SubscribeSessionViewRequest{SessionId: started.Msg.SessionId}))
	if err != nil {
		t.Fatalf("SubscribeSessionView: %v", err)
	}
	defer stream.Close()
	keepalives := 0
	for keepalives < 2 && stream.Receive() {
		msg := stream.Msg()
		if msg.State == nil && msg.TransientEvent == nil && len(msg.UpsertedRows) == 0 && len(msg.DeletedRowIds) == 0 {
			keepalives++
		}
	}
	if keepalives < 2 {
		t.Fatalf("got %d keepalives before stream ended: %v", keepalives, stream.Err())
	}
}
