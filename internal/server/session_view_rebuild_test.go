package server

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// A subscriber that names its snapshot's index generation is told to reload
// once the index is rebuilt (here by another reader noticing a replaced log),
// whether that happens mid-stream or before it subscribes; an unchanged
// generation streams normally.
func TestSubscribeSessionViewResetsAfterIndexRebuild(t *testing.T) {
	srv, workspace := newImageTestServer(t)
	defer srv.mgr.ReclaimAll()
	srv.viewKeepalive = 30 * time.Millisecond
	ctx := context.Background()
	started, err := srv.StartSession(ctx, connect.NewRequest(&v1.StartSessionRequest{Workspace: workspace, Mode: "chat"}))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	id := started.Msg.SessionId
	defer srv.StopSession(ctx, connect.NewRequest(&v1.StopSessionRequest{SessionId: id})) //nolint:errcheck

	_, handler := yccv1connect.NewSessionServiceHandler(srv)
	httpServer := httptest.NewUnstartedServer(handler)
	httpServer.EnableHTTP2 = true
	httpServer.StartTLS()
	defer httpServer.Close()
	client := yccv1connect.NewSessionServiceClient(httpServer.Client(), httpServer.URL)
	// Cancelled before httpServer.Close, which waits for open streams.
	streamCtx, cancelStreams := context.WithTimeout(ctx, 5*time.Second)
	defer cancelStreams()

	view := func() int64 {
		t.Helper()
		resp, err := client.GetSessionView(ctx, connect.NewRequest(&v1.GetSessionViewRequest{SessionId: id}))
		if err != nil {
			t.Fatalf("GetSessionView: %v", err)
		}
		return resp.Msg.State.IndexGeneration
	}
	subscribe := func(generation int64) *connect.ServerStreamForClient[v1.SessionViewUpdate] {
		t.Helper()
		stream, err := client.SubscribeSessionView(streamCtx, connect.NewRequest(&v1.SubscribeSessionViewRequest{SessionId: id, IndexGeneration: &generation}))
		if err != nil {
			t.Fatalf("SubscribeSessionView: %v", err)
		}
		return stream
	}
	// expectReset reads until the reset; nothing else may claim a rebuild.
	expectReset := func(stream *connect.ServerStreamForClient[v1.SessionViewUpdate]) {
		t.Helper()
		for stream.Receive() {
			if stream.Msg().IndexRebuilt {
				if stream.Receive() {
					t.Fatalf("stream continued after reset: %v", stream.Msg())
				}
				if err := stream.Err(); err != nil {
					t.Fatalf("stream ended with %v after reset", err)
				}
				return
			}
		}
		t.Fatalf("stream ended without reset: %v", stream.Err())
	}
	expectKeepalives := func(stream *connect.ServerStreamForClient[v1.SessionViewUpdate]) {
		t.Helper()
		for keepalives := 0; keepalives < 2; {
			if !stream.Receive() {
				t.Fatalf("stream ended: %v", stream.Err())
			}
			msg := stream.Msg()
			if msg.IndexRebuilt {
				t.Fatal("unexpected reset without a rebuild")
			}
			if msg.State == nil && msg.TransientEvent == nil {
				keepalives++
			}
		}
	}

	if g := view(); g != 0 {
		t.Fatalf("fresh index generation=%d, want 0", g)
	}
	live := subscribe(0)
	expectKeepalives(live)

	// Replace the log with an identical copy on a new inode: the index must be
	// rebuilt from it, and the rebuild must reach the quiet subscriber.
	_, logPath, _, _, err := srv.mgr.SessionViewLogPath("", id)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath+".tmp", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(logPath+".tmp", logPath); err != nil {
		t.Fatal(err)
	}
	rebuilt := view()
	if rebuilt == 0 {
		t.Fatal("replaced log did not start a new index generation")
	}
	expectReset(live)

	// A client still holding the old generation is reset at once; one that
	// reloaded streams normally.
	expectReset(subscribe(0))
	expectKeepalives(subscribe(rebuilt))
}
