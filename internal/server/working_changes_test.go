package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestGetWorkingChanges(t *testing.T) {
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	task, err := docs.NewStore(ws).Create("review task", "## Work log\n", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.CaptureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PersistBaseline("s_test", b); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(ws, ".ycc", "sessions", "s_test", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(log), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte("{\"seq\":1,\"type\":\"session_started\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "large.txt"), []byte(strings.Repeat("large line\n", 160000)), 0644); err != nil {
		t.Fatal(err)
	}
	reg := config.NewRegistry(&config.Config{Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "a"}}, Roles: config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}}})
	srv := New(session.NewManager(reg, ws))
	get := func(known string) *v1.GetWorkingChangesResponse {
		t.Helper()
		response, err := srv.GetWorkingChanges(context.Background(), connect.NewRequest(&v1.GetWorkingChangesRequest{SessionId: "s_test", KnownSnapshotId: known}))
		if err != nil {
			t.Fatal(err)
		}
		return response.Msg
	}
	first := get("")
	if !first.Truncated || len(first.Diff) > maxCommitDiffBytes || !strings.HasSuffix(first.Diff, "\n") || first.DiffBytes <= maxCommitDiffBytes || first.PathsTotal != 1 || first.ExcludedDirtyPaths != 1 || first.ChangedSinceKnown {
		t.Fatalf("unexpected bounded response: %+v", first)
	}
	if got := get(first.SnapshotId); got.ChangedSinceKnown {
		t.Fatal("unchanged snapshot marked changed")
	}
	adopted, err := srv.GetWorkingChanges(context.Background(), connect.NewRequest(&v1.GetWorkingChangesRequest{SessionId: "s_test", TaskId: task.ID, KnownSnapshotId: first.SnapshotId}))
	if err != nil {
		t.Fatal(err)
	}
	if !adopted.Msg.ChangedSinceKnown || adopted.Msg.ExcludedDirtyPaths != 0 || adopted.Msg.PathsTotal != 2 || !strings.Contains(adopted.Msg.Scope, "doc adopted") {
		t.Fatalf("task adoption scope: %+v", adopted.Msg)
	}
	if err := os.WriteFile(filepath.Join(ws, "new.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := get(first.SnapshotId); !got.ChangedSinceKnown || got.SnapshotId == first.SnapshotId {
		t.Fatal("edit not detected")
	}
	for _, tc := range []struct {
		req  *v1.GetWorkingChangesRequest
		code connect.Code
	}{
		{&v1.GetWorkingChangesRequest{SessionId: "missing"}, connect.CodeNotFound},
		{&v1.GetWorkingChangesRequest{Project: "unknown", SessionId: "s_test"}, connect.CodeInvalidArgument},
	} {
		_, err := srv.GetWorkingChanges(context.Background(), connect.NewRequest(tc.req))
		if connect.CodeOf(err) != tc.code {
			t.Fatalf("code %v, want %v: %v", connect.CodeOf(err), tc.code, err)
		}
	}
}
