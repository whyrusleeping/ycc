package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func TestLatencyBoundAndAuth(t *testing.T) {
	r := NewLatencyRecorder()
	for i := 0; i < latencyCapacity+3; i++ {
		r.record(LatencyEntry{Procedure: "test", Kind: "unary", DurationMS: float64(i), Outcome: "ok"})
	}
	snap := r.Snapshot()
	if len(snap.Entries) != latencyCapacity || snap.Entries[0].DurationMS != 3 || snap.Aggregates["unary test"].Count != latencyCapacity {
		t.Fatalf("ring or aggregate: %+v", snap.Aggregates)
	}
	h := r.LatencyHandler("secret")
	for _, tc := range []struct {
		auth string
		code int
	}{{"", 401}, {"Bearer wrong", 401}, {"Bearer secret", 200}} {
		req := httptest.NewRequest("GET", "/debug/latency", nil)
		req.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("auth %q: %d", tc.auth, w.Code)
		}
		if tc.code == 200 {
			var got LatencySnapshot
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Entries) != latencyCapacity {
				t.Fatalf("json: %v", err)
			}
		}
	}
}

type fakeLatencyStream struct{ connect.StreamingHandlerConn }

func (fakeLatencyStream) RequestHeader() http.Header {
	return http.Header{requestIDHeader: {"stream-1"}}
}
func (fakeLatencyStream) Spec() connect.Spec { return connect.Spec{Procedure: "/test/Subscribe"} }
func (fakeLatencyStream) Send(any) error     { return nil }

func TestLatencyFirstSend(t *testing.T) {
	r := NewLatencyRecorder()
	wrapped := r.WrapStreamingHandler(func(_ context.Context, conn connect.StreamingHandlerConn) error {
		time.Sleep(time.Millisecond)
		if err := conn.Send("first"); err != nil {
			return err
		}
		return conn.Send("second")
	})
	if err := wrapped(context.Background(), fakeLatencyStream{}); err != nil {
		t.Fatal(err)
	}
	entry := r.Snapshot().Entries[0]
	if entry.Kind != "stream" || entry.FirstSendMS <= 0 || entry.DurationMS < entry.FirstSendMS || entry.MessagesSent != 2 || entry.RequestID != "stream-1" {
		t.Fatalf("entry: %+v", entry)
	}
}

func TestLatencyConnectHeadersAndStream(t *testing.T) {
	r := NewLatencyRecorder()
	mgr := session.NewManager(config.NewRegistry(&config.Config{}), t.TempDir())
	defer mgr.ReclaimAll()
	path, h := yccv1connect.NewSessionServiceHandler(New(mgr), connect.WithInterceptors(NewAuthInterceptor("secret"), r))
	mux := http.NewServeMux()
	mux.Handle(path, h)
	mux.Handle("GET /debug/latency", r.LatencyHandler("secret"))
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := yccv1connect.NewSessionServiceClient(ts.Client(), ts.URL)
	call := func(id string) (*connect.Response[v1.ListProjectsResponse], error) {
		req := connect.NewRequest(&v1.ListProjectsRequest{})
		req.Header().Set("Authorization", "Bearer secret")
		req.Header().Set(requestIDHeader, id)
		return client.ListProjects(context.Background(), req)
	}
	if _, err := client.ListProjects(context.Background(), connect.NewRequest(&v1.ListProjectsRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("auth: %v", err)
	}
	res, err := call("safe-123")
	if err != nil {
		t.Fatal(err)
	}
	if res.Header().Get(requestIDHeader) != "safe-123" || !strings.HasPrefix(res.Header().Get("Server-Timing"), "app;dur=") {
		t.Fatalf("headers: %v", res.Header())
	}
	missing := connect.NewRequest(&v1.GetSessionViewRequest{SessionId: "missing"})
	missing.Header().Set("Authorization", "Bearer secret")
	missing.Header().Set(requestIDHeader, "error-1")
	_, missingErr := client.GetSessionView(context.Background(), missing)
	if connect.CodeOf(missingErr) != connect.CodeNotFound {
		t.Fatalf("missing view: %v", missingErr)
	}
	var connectErr *connect.Error
	if !errors.As(missingErr, &connectErr) || connectErr.Meta().Get(requestIDHeader) != "error-1" || connectErr.Meta().Get("Server-Timing") == "" {
		t.Fatalf("error headers: %v", missingErr)
	}
	res, err = call("bad/value")
	if err == nil && res.Header().Get(requestIDHeader) != "" {
		t.Fatalf("unsafe echo: %v", res.Header())
	}
	// A missing session terminates a stream with an error. It still belongs in
	// stream lifetime stats, not unary handler latency.
	req := connect.NewRequest(&v1.SubscribeRequest{SessionId: "missing"})
	req.Header().Set("Authorization", "Bearer secret")
	stream, err := client.Subscribe(context.Background(), req)
	if err == nil {
		for stream.Receive() {
		}
		err = stream.Err()
		stream.Close()
	}
	if err == nil {
		t.Fatal("expected missing-session error")
	}
	snap := r.Snapshot()
	if len(snap.Entries) != 4 {
		t.Fatalf("authenticated entries = %d", len(snap.Entries))
	}
	if snap.Entries[0].Kind != "unary" || snap.Entries[0].RequestID != "safe-123" || snap.Entries[3].Kind != "stream" {
		t.Fatalf("entries: %+v", snap.Entries)
	}
	raw, _ := json.Marshal(snap)
	for _, secret := range []string{"bad\\nvalue", "Bearer secret", "SessionId", "Authorization"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("diagnostics leak %q", secret)
		}
	}
	if validRequestID(strings.Repeat("a", 65)) != "" || validRequestID("a/b") != "" {
		t.Fatal("unsafe request id")
	}
	// The stream timer is independently sampled even if no message was sent.
	if snap.Entries[3].DurationMS < 0 || snap.Entries[3].Time.After(time.Now()) {
		t.Fatal("invalid stream timer")
	}
}
