package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/notify"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func testRegistry() *config.Registry {
	return config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"c": {Backend: "ollama", Model: "m"}},
		Roles:  config.Roles{Coordinator: "c", Implementer: "c", Reviewers: []string{"c"}},
	})
}

func TestNotifyRPCDelivered(t *testing.T) {
	got := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		select {
		case got <- string(b):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	mgr := session.NewManager(testRegistry(), t.TempDir())
	n := notify.New(config.Notify{URL: ts.URL})
	mgr.SetNotifier(n)
	srv := New(mgr)

	resp, err := srv.Notify(context.Background(), connect.NewRequest(&v1.NotifyRequest{
		Kind: "digest", Line: "work loop finished: 3 completed", Project: "p", SessionId: "s",
	}))
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !resp.Msg.Delivered {
		t.Fatal("delivered = false, want true with a notifier configured")
	}
	n.Flush()
	select {
	case body := <-got:
		if body == "" {
			t.Error("empty webhook body")
		}
	default:
		t.Error("no webhook received")
	}
}

func TestNotifyRPCDisabled(t *testing.T) {
	mgr := session.NewManager(testRegistry(), t.TempDir())
	// No notifier configured.
	srv := New(mgr)
	resp, err := srv.Notify(context.Background(), connect.NewRequest(&v1.NotifyRequest{
		Kind: "digest", Line: "x", Project: "p",
	}))
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if resp.Msg.Delivered {
		t.Fatal("delivered = true, want false with no notifier")
	}
}

func TestNotifyRPCInvalidKind(t *testing.T) {
	mgr := session.NewManager(testRegistry(), t.TempDir())
	srv := New(mgr)
	if _, err := srv.Notify(context.Background(), connect.NewRequest(&v1.NotifyRequest{
		Kind: "bogus", Line: "x",
	})); err == nil {
		t.Fatal("Notify with invalid kind succeeded, want error")
	}
}
