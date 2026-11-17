package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// TestListCommits covers history paging (stable against new commits), branch
// selection and validation, shortstat, and session/task links by exact sha and
// by the rebased-commit subject fallback.
func TestListCommits(t *testing.T) {
	ws := t.TempDir()
	gitRun := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = ws
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ann", "GIT_AUTHOR_EMAIL=ann@example.com",
			"GIT_COMMITTER_NAME=Ann", "GIT_COMMITTER_EMAIL=ann@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitRun("init", "-q", "-b", "main")
	commit := func(file, content, msg string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ws, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun("add", file)
		gitRun("commit", "-q", "-m", msg)
		return gitRun("rev-parse", "HEAD")
	}
	commit("a.txt", "a\n", "first")
	second := commit("b.txt", "b\nb2\n", "second\n\nbody")
	gitRun("branch", "feature")
	rebased := commit("c.txt", "c\n", "rebased work")
	fourth := commit("a.txt", "a changed\n", "fourth")

	// A session log recording `second` by sha and a pre-rebase copy of
	// "rebased work" under a sha that no longer exists.
	logDir := filepath.Join(ws, ".ycc", "sessions", "s_test")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var lines []string
	for i, ev := range []map[string]any{
		{"type": "session_started", "data": map[string]any{"mode": "chat", "workspace": ws}},
		{"type": "user_input", "data": map[string]any{"text": "do the things"}},
		{"type": "commit_made", "data": map[string]any{"sha": second, "task": "0042", "message": "second\n\nbody"}},
		{"type": "commit_made", "data": map[string]any{"sha": strings.Repeat("ab", 20), "message": "rebased work"}},
	} {
		ev["seq"], ev["ts"], ev["actor"] = i+1, now.Format(time.RFC3339Nano), "coordinator"
		b, _ := json.Marshal(ev)
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(filepath.Join(logDir, "events.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	srv := New(session.NewManager(reg, ws))
	ctx := context.Background()
	list := func(req *v1.ListCommitsRequest) *v1.ListCommitsResponse {
		t.Helper()
		resp, err := srv.ListCommits(ctx, connect.NewRequest(req))
		if err != nil {
			t.Fatalf("ListCommits(%v): %v", req, err)
		}
		return resp.Msg
	}

	first := list(&v1.ListCommitsRequest{Limit: 2})
	if first.Branch != "main" || len(first.Commits) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = branch %q, %d commits, cursor %q", first.Branch, len(first.Commits), first.NextCursor)
	}
	if got := first.Commits[0]; got.Sha != fourth || got.Subject != "fourth" || got.AuthorName != "Ann" ||
		got.FilesChanged != 1 || got.Additions != 1 || got.Deletions != 1 || len(got.Parents) != 1 || got.SessionId != "" {
		t.Fatalf("head commit = %+v", got)
	}
	if got := first.Commits[1]; got.Sha != rebased || got.SessionId != "s_test" || got.TaskId != "" {
		t.Fatalf("rebased fallback link = %+v", got)
	}
	if len(first.Branches) != 2 || first.Branches[0].Name != "main" || !first.Branches[0].Current || first.Branches[1].Name != "feature" {
		t.Fatalf("branches = %v", first.Branches)
	}

	// A new commit does not shift the next page.
	commit("d.txt", "d\n", "fifth")
	next := list(&v1.ListCommitsRequest{Limit: 2, Cursor: first.NextCursor, Branch: "main"})
	if len(next.Commits) != 2 || next.Commits[0].Sha != second || next.NextCursor != "" {
		t.Fatalf("second page = %+v", next)
	}
	if got := next.Commits[0]; got.SessionId != "s_test" || got.SessionTitle != "do the things" || got.TaskId != "0042" ||
		got.Subject != "second" || got.Additions != 2 {
		t.Fatalf("linked commit = %+v", got)
	}

	feature := list(&v1.ListCommitsRequest{Branch: "feature"})
	if feature.Branch != "feature" || len(feature.Commits) != 2 || feature.Commits[0].Sha != second {
		t.Fatalf("feature = %+v", feature)
	}

	for _, req := range []*v1.ListCommitsRequest{
		{Branch: "--all"},
		{Branch: "nope"},
		{Cursor: "garbage"},
		{Cursor: "HEAD:0"},
		{Cursor: strings.Repeat("cd", 20) + ":0"},
		{Project: "nope"},
	} {
		if _, err := srv.ListCommits(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("ListCommits(%v) code = %v, want InvalidArgument", req, connect.CodeOf(err))
		}
	}
}
