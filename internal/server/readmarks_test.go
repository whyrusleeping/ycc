package server

import (
	"context"
	"net/http/httptest"
	"os"
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

func TestMarkSessionsReadRPC(t *testing.T) {
	ws := t.TempDir()
	for _, id := range []string{"a", "b"} {
		lg, err := event.OpenLog(filepath.Join(ws, ".ycc", "sessions", id, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lg.Record("user", event.UserInput, map[string]any{"text": "hello " + id})
		if err := lg.Close(); err != nil {
			t.Fatal(err)
		}
	}
	mgr := session.NewManager(config.NewRegistry(nil), ws)
	defer mgr.ReclaimAll()
	_, handler := yccv1connect.NewSessionServiceHandler(New(mgr), connect.WithInterceptors(NewAuthInterceptor("secret")))
	httpServer := httptest.NewServer(RequireBearer("secret", handler))
	defer httpServer.Close()
	client := yccv1connect.NewSessionServiceClient(httpServer.Client(), httpServer.URL)
	ctx := context.Background()

	readThrough := func() map[string]string {
		t.Helper()
		req := connect.NewRequest(&v1.ListSessionHistoryRequest{})
		req.Header().Set("Authorization", "Bearer secret")
		res, err := client.ListSessionHistory(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, s := range res.Msg.Sessions {
			out[s.SessionId] = s.ReadThrough
		}
		return out
	}
	mark := func(project string, marks ...*v1.SessionReadMark) (*v1.MarkSessionsReadResponse, error) {
		req := connect.NewRequest(&v1.MarkSessionsReadRequest{Project: project, Marks: marks})
		req.Header().Set("Authorization", "Bearer secret")
		res, err := client.MarkSessionsRead(ctx, req)
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}

	// No read state yet: rows carry no mark (clients treat them as read).
	if got := readThrough(); got["a"] != "" || got["b"] != "" {
		t.Fatalf("before any mark: %v", got)
	}

	// The first mark starts tracking: "a" gets its mark, unmarked "b" reports the baseline.
	before := time.Now().UTC().Truncate(time.Millisecond)
	const stamp = "2026-01-02T03:04:05.678Z"
	res, err := mark("", &v1.SessionReadMark{SessionId: "a", ReadThrough: stamp}, &v1.SessionReadMark{SessionId: "missing", ReadThrough: stamp})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Marks) != 1 || res.Marks[0].SessionId != "a" || res.Marks[0].ReadThrough != stamp {
		t.Fatalf("mark response (unknown id must be skipped): %+v", res.Marks)
	}
	got := readThrough()
	if got["a"] != stamp {
		t.Fatalf("a read_through = %q, want %q", got["a"], stamp)
	}
	baseline, err := time.Parse(time.RFC3339, got["b"])
	if err != nil || baseline.Before(before) || baseline.After(time.Now()) {
		t.Fatalf("b should report the baseline (now): %q %v", got["b"], err)
	}

	// Marks never move back.
	res, err = mark("", &v1.SessionReadMark{SessionId: "a", ReadThrough: "2025-01-01T00:00:00.000Z"})
	if err != nil || res.Marks[0].ReadThrough != stamp {
		t.Fatalf("older mark must be a no-op: %+v %v", res, err)
	}

	// Future stamps clamp to the daemon clock.
	res, err = mark("", &v1.SessionReadMark{SessionId: "b", ReadThrough: "2999-01-01T00:00:00.000Z"})
	if err != nil {
		t.Fatal(err)
	}
	if clamped, _ := time.Parse(time.RFC3339, res.Marks[0].ReadThrough); clamped.After(time.Now()) {
		t.Fatalf("future stamp not clamped: %q", res.Marks[0].ReadThrough)
	}

	// Marks persist across a daemon restart (a fresh manager on the same workspace).
	mgr2 := session.NewManager(config.NewRegistry(nil), ws)
	defer mgr2.ReclaimAll()
	rows, err := mgr2.ListSessionHistory("")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == "a" && row.ReadThrough.Format("2006-01-02T15:04:05.000Z07:00") != stamp {
			t.Fatalf("mark lost across restart: %v", row.ReadThrough)
		}
	}

	if _, err := mark("", &v1.SessionReadMark{SessionId: "a", ReadThrough: "yesterday"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad stamp: %v", err)
	}
	if _, err := mark("missing", &v1.SessionReadMark{SessionId: "a", ReadThrough: stamp}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown project: %v", err)
	}

	// A corrupt file hides marks from the list without failing it, and the
	// next mark replaces it with fresh state.
	path := filepath.Join(ws, ".ycc", "session-read-marks.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readThrough(); got["a"] != "" {
		t.Fatalf("corrupt file should list without marks: %v", got)
	}
	if _, err := mark("", &v1.SessionReadMark{SessionId: "a", ReadThrough: stamp}); err != nil {
		t.Fatalf("mark over corrupt file: %v", err)
	}
	if got := readThrough(); got["a"] != stamp {
		t.Fatalf("after replacing corrupt file: %v", got)
	}
}
