package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// Typing into an opened session that the daemon no longer holds — its process
// is gone (daemon restart / idle reap) or it was stopped — delivers the message
// and continues the session with no separate resume step.
func TestSendInputContinuesSessionThatIsNotLive(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop bool
	}{
		{name: "idle after restart"},
		{name: "stopped", stop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"continued\"},\"finish_reason\":null}]}\n\n" +
					"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
					"data: [DONE]\n\n"))
			}))
			defer provider.Close()
			t.Setenv("YCC_TEST_SEND_REOPEN_KEY", "test-key")
			reg := config.NewRegistry(&config.Config{
				Models: map[string]config.Model{"a": {Backend: "openai", BaseURL: provider.URL, Model: "m", KeyEnv: "YCC_TEST_SEND_REOPEN_KEY"}},
				Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
			})
			ws := t.TempDir()
			if _, err := git.Open(ws); err != nil {
				t.Fatal(err)
			}
			absWS, _ := filepath.Abs(ws)
			id := "s_send_reopen"
			lg, err := event.OpenLog(filepath.Join(ws, ".ycc", "sessions", id, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			lg.Record("coordinator", event.SessionStarted, map[string]any{"mode": "chat", "workspace": absWS})
			lg.Record("user", event.UserInput, map[string]any{"text": "hi"})
			lg.Record("coordinator", event.ModelTurn, map[string]any{"text": "hello"})
			lg.Record("coordinator", event.SessionIdle, map[string]any{"report": "hello"})
			if err := lg.Close(); err != nil {
				t.Fatal(err)
			}

			mgr := session.NewManager(reg, ws)
			srv := New(mgr)
			ctx := context.Background()
			if tc.stop {
				if _, err := mgr.Reopen("", id); err != nil {
					t.Fatal(err)
				}
				if _, err := srv.StopSession(ctx, connect.NewRequest(&v1.StopSessionRequest{SessionId: id})); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok := mgr.Get(id); ok {
				t.Fatal("session should not be live before input")
			}

			if _, err := srv.SendInput(ctx, connect.NewRequest(&v1.SendInputRequest{SessionId: id, Text: "keep going"})); err != nil {
				t.Fatalf("SendInput: %v", err)
			}
			sess, ok := mgr.Get(id)
			if !ok {
				t.Fatal("SendInput did not bring the session back")
			}
			t.Cleanup(func() { _ = mgr.Stop(id) })

			deadline := time.Now().Add(5 * time.Second)
			for {
				snap := sess.Log().Snapshot()
				inputSeq := 0
				for _, ev := range snap {
					if ev.Type == event.UserInput && ev.Data["text"] == "keep going" {
						inputSeq = ev.Seq
					}
					if inputSeq > 0 && ev.Seq > inputSeq && ev.Type == event.ModelTurn && ev.Data["text"] == "continued" {
						if calls.Load() == 0 {
							t.Fatal("model was never called")
						}
						return
					}
				}
				if time.Now().After(deadline) {
					t.Fatalf("session did not continue after input; events: %+v", snap)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

// Input to a session with no persisted log is still NotFound.
func TestSendInputUnknownSession(t *testing.T) {
	srv, _ := newImageTestServer(t)
	_, err := srv.SendInput(context.Background(), connect.NewRequest(&v1.SendInputRequest{SessionId: "s_missing", Text: "hi"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("SendInput unknown session: %v, want NotFound", err)
	}
}
