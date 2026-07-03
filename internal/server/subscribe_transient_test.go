package server_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/server"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// newSubscribeServer stands up a real Connect handler over an h2c httptest server
// backed by a manager rooted at a temp workspace, returning the client and manager.
func newSubscribeServer(t *testing.T) (yccv1connect.SessionServiceClient, *session.Manager, string) {
	t.Helper()
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	ws := t.TempDir()
	mgr := session.NewManager(reg, ws)

	path, handler := yccv1connect.NewSessionServiceHandler(server.New(mgr))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)

	httpClient := &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	client := yccv1connect.NewSessionServiceClient(httpClient, srv.URL)
	return client, mgr, ws
}

// A transient event broadcast on a session's log is carried unchanged over the
// Subscribe RPC stream (transient=true, seq=0), and it never advances the
// from_seq resume cursor — a reconnect resumes strictly from persisted seqs.
func TestSubscribeCarriesTransientAndResumeUnaffected(t *testing.T) {
	client, mgr, ws := newSubscribeServer(t)
	ctx := context.Background()

	// Persist a session log with two events, then reopen it live so it is
	// registered in the manager and its Log is broadcastable.
	id := "sess_transient"
	logPath := filepath.Join(ws, ".ycc", "sessions", id, "events.jsonl")
	lg, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	lg.Record("user", event.UserInput, map[string]any{"text": "hi"})
	lg.Record("coordinator", event.ModelTurn, map[string]any{"text": "hello"})
	lg.Close()

	if _, err := client.ResumeSession(ctx, connect.NewRequest(&v1.ResumeSessionRequest{SessionId: id})); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	defer mgr.Stop(id)
	sess, ok := mgr.Get(id)
	if !ok {
		t.Fatal("session not registered after ResumeSession")
	}
	log := sess.Log()
	lastSeq := log.LastSeq()

	// Subscribe from the last persisted seq: no replay expected, just live events.
	subCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Emit concurrently: broadcast two transients then record one persisted event.
	// Doing this from a goroutine avoids coupling to when the stream's response
	// headers flush, and mirrors real live traffic. The transient must not perturb
	// the persisted seq sequence.
	next := lastSeq + 1
	go func() {
		time.Sleep(100 * time.Millisecond)
		log.Broadcast("coordinator", event.TurnDelta, map[string]any{"text": "par"})
		log.Broadcast("coordinator", event.TurnDelta, map[string]any{"text": "tial"})
		if ev := log.Record("coordinator", event.ModelTurn, map[string]any{"text": "done"}); ev.Seq != next {
			t.Errorf("recorded seq %d, want %d", ev.Seq, next)
		}
	}()

	stream, err := client.Subscribe(subCtx, connect.NewRequest(&v1.SubscribeRequest{SessionId: id, FromSeq: int64(lastSeq)}))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stream.Close()

	sawTransient := false
	sawPersisted := false
	for stream.Receive() {
		ev := stream.Msg()
		if ev.GetTransient() {
			sawTransient = true
			if ev.GetSeq() != 0 {
				t.Fatalf("transient event carried seq %d, want 0", ev.GetSeq())
			}
			if ev.GetType() != string(event.TurnDelta) {
				t.Fatalf("transient type = %q, want turn_delta", ev.GetType())
			}
			if sawTransient && sawPersisted {
				break
			}
			continue
		}
		// The only persisted event we should observe is the new one at lastSeq+1.
		sawPersisted = true
		if ev.GetSeq() != int64(next) {
			t.Fatalf("persisted event seq = %d, want %d (from_seq resume must skip replayed events)", ev.GetSeq(), next)
		}
		if ev.GetType() != string(event.ModelTurn) {
			t.Fatalf("persisted type = %q, want model_turn", ev.GetType())
		}
		if sawTransient && sawPersisted {
			break
		}
	}
	if !sawTransient {
		t.Fatal("stream never delivered the transient event")
	}
	if !sawPersisted {
		t.Fatal("stream never delivered the persisted event after the transient")
	}
}
