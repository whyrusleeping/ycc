package server_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/event"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// Reopen the manager and Connect handler over the same workspace, then check
// persisted replay and a second subscription after a dropped client stream.
func TestSubscribeAfterRestartAndResubscribe(t *testing.T) {
	client, mgr, ws := newSubscribeServer(t)
	id := "sess_replay_restart"
	lg, err := event.OpenLog(filepath.Join(ws, ".ycc", "sessions", id, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first", "second", "third"} {
		lg.Record("user", event.UserInput, map[string]any{"text": text})
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResumeSession(context.Background(), connect.NewRequest(&v1.ResumeSessionRequest{SessionId: id})); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Stop(id); err != nil {
		t.Fatal(err)
	}
	// Simulate a new daemon process: a new manager and server reading the old log.
	restarted, nextMgr := newSubscribeServerAt(t, ws)
	if _, err := restarted.ResumeSession(context.Background(), connect.NewRequest(&v1.ResumeSessionRequest{SessionId: id})); err != nil {
		t.Fatal(err)
	}
	defer nextMgr.Stop(id)
	sess, ok := nextMgr.Get(id)
	if !ok {
		t.Fatal("restarted manager did not load session")
	}
	log := sess.Log()
	from := 1
	expected, _ := log.SnapshotFrom(from)
	if len(expected) < 2 {
		t.Fatalf("missing persisted replay: %v", expected)
	}
	subscribe := func(cursor int) (*connect.ServerStreamForClient[v1.Event], context.CancelFunc) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		stream, err := restarted.Subscribe(ctx, connect.NewRequest(&v1.SubscribeRequest{SessionId: id, FromSeq: int64(cursor)}))
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		return stream, cancel
	}
	stream, cancel := subscribe(from)
	for _, want := range expected {
		if !stream.Receive() {
			t.Fatalf("restart replay ended early: %v", stream.Err())
		}
		got := stream.Msg()
		if got.Seq != int64(want.Seq) || got.Type != string(want.Type) {
			t.Fatalf("restart replay got %d/%s want %d/%s", got.Seq, got.Type, want.Seq, want.Type)
		}
	}
	cursor := expected[len(expected)-1].Seq
	stream.Close()
	cancel() // client dropped; continue from its durable cursor
	next := log.Record("user", event.UserInput, map[string]any{"text": "after disconnect"})
	stream, cancel = subscribe(cursor)
	defer cancel()
	defer stream.Close()
	if !stream.Receive() {
		t.Fatalf("resubscribe ended early: %v", stream.Err())
	}
	if got := stream.Msg(); got.Seq != int64(next.Seq) || got.Type != string(event.UserInput) {
		t.Fatalf("resubscribe got %d/%s want %d/user_input", got.Seq, got.Type, next.Seq)
	}
	// A subsequent live event must follow replay without duplicating the first.
	live := log.Record("user", event.UserInput, map[string]any{"text": "live"})
	if !stream.Receive() {
		t.Fatalf("live subscription ended: %v", stream.Err())
	}
	if got := stream.Msg(); got.Seq != int64(live.Seq) {
		t.Fatalf("live seq %d want %d", got.Seq, live.Seq)
	}
}
