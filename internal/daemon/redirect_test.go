package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestDaemonClientRefusesCredentialRedirect(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	// Trust the test certificate without changing DialClient's redirect policy.
	old := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = old })
	client := DialClient(server.URL, "daemon-token-secret")
	if _, err := client.ListModes(context.Background(), connect.NewRequest(&v1.ListModesRequest{})); err == nil {
		t.Fatal("redirect succeeded")
	}
	if received.Load() != 0 {
		t.Fatalf("redirect target received %d requests", received.Load())
	}
}
