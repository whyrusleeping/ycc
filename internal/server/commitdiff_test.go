package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"strings"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// TestGetCommitDiff covers the commit-diff drill-in RPC (task 0140): a real
// commit's diff round-trips, an empty sha is InvalidArgument, and an unknown
// project / unknown sha both map to NotFound.
func TestGetCommitDiff(t *testing.T) {
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	ws := t.TempDir()
	repo, err := git.Open(ws)
	if err != nil {
		t.Fatalf("git open: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sha, err := repo.Commit("add hello")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	srv := New(session.NewManager(reg, ws))
	ctx := context.Background()

	// Happy path: default workspace (empty project), real sha.
	resp, err := srv.GetCommitDiff(ctx, connect.NewRequest(&v1.GetCommitDiffRequest{Sha: sha}))
	if err != nil {
		t.Fatalf("GetCommitDiff: %v", err)
	}
	if resp.Msg.GetTruncated() {
		t.Errorf("small diff reported truncated")
	}
	if diff := resp.Msg.GetDiff(); diff == "" ||
		!containsAll(diff, "diff --git", "hello.txt") {
		t.Fatalf("unexpected diff:\n%s", resp.Msg.GetDiff())
	}

	// Empty sha → InvalidArgument.
	if _, err := srv.GetCommitDiff(ctx, connect.NewRequest(&v1.GetCommitDiffRequest{Sha: "  "})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty sha code = %v, want InvalidArgument", connect.CodeOf(err))
	}

	// Unknown project → NotFound.
	if _, err := srv.GetCommitDiff(ctx, connect.NewRequest(&v1.GetCommitDiffRequest{Project: "nope", Sha: sha})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown project code = %v, want NotFound", connect.CodeOf(err))
	}

	// Unknown sha → NotFound (git can't resolve it).
	if _, err := srv.GetCommitDiff(ctx, connect.NewRequest(&v1.GetCommitDiffRequest{Sha: "deadbeef"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown sha code = %v, want NotFound", connect.CodeOf(err))
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
