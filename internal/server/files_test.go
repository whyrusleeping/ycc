package server_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// TestFileRPCs covers project-confined ListFiles/ReadFile over real HTTP,
// including session-worktree resolution and the fallback to the project root
// once a workstream's worktree is reclaimed (task 0398).
func TestFileRPCs(t *testing.T) {
	client, mgr, proj, _ := newWorkstreamServer(t)
	ctx := context.Background()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(proj, "internal", "pkg"), 0o755))
	must(os.WriteFile(filepath.Join(proj, "internal", "pkg", "a.go"), []byte("package pkg\n"), 0o644))
	must(os.WriteFile(filepath.Join(proj, "README.md"), []byte("# demo\n"), 0o644))
	outside := t.TempDir()
	must(os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644))
	must(os.Symlink(filepath.Join(outside, "secret"), filepath.Join(proj, "escape")))

	// Root listing: dirs first, dotfiles (.git, .ycc) omitted.
	lr, err := client.ListFiles(ctx, connect.NewRequest(&v1.ListFilesRequest{Project: "demo"}))
	must(err)
	if lr.Msg.Root != proj || lr.Msg.Path != "" || lr.Msg.RootFallback {
		t.Fatalf("root listing meta: %+v", lr.Msg)
	}
	var names []string
	for _, e := range lr.Msg.Entries {
		names = append(names, e.Name)
		if e.Name[0] == '.' {
			t.Fatalf("dotfile listed: %s", e.Name)
		}
	}
	if len(names) < 3 || names[0] != "internal" {
		t.Fatalf("names = %v", names)
	}

	// Empty project resolves when exactly one is registered.
	rr, err := client.ReadFile(ctx, connect.NewRequest(&v1.ReadFileRequest{Path: "internal/pkg/a.go"}))
	must(err)
	if string(rr.Msg.Data) != "package pkg\n" || rr.Msg.Path != "internal/pkg/a.go" || rr.Msg.IsBinary || rr.Msg.Mtime == "" {
		t.Fatalf("ReadFile: %+v", rr.Msg)
	}

	codes := []struct {
		path string
		want connect.Code
	}{
		{"../x", connect.CodeInvalidArgument},
		{"/etc/hosts", connect.CodeInvalidArgument},
		{"internal", connect.CodeInvalidArgument},
		{"missing.go", connect.CodeNotFound},
		{"escape", connect.CodePermissionDenied},
		{".git/HEAD", connect.CodePermissionDenied},
	}
	for _, c := range codes {
		_, err := client.ReadFile(ctx, connect.NewRequest(&v1.ReadFileRequest{Project: "demo", Path: c.path}))
		if connect.CodeOf(err) != c.want {
			t.Errorf("ReadFile(%q) code = %v; want %v (%v)", c.path, connect.CodeOf(err), c.want, err)
		}
	}
	if _, err := client.ListFiles(ctx, connect.NewRequest(&v1.ListFilesRequest{Project: "nope"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown project code = %v", connect.CodeOf(err))
	}
	if _, err := client.ListFiles(ctx, connect.NewRequest(&v1.ListFilesRequest{Project: "demo", Path: "README.md"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("list file code = %v", connect.CodeOf(err))
	}

	// A workstream session resolves against its worktree, where files exist
	// that the base checkout doesn't have.
	sp, err := client.SpawnWorkstream(ctx, connect.NewRequest(&v1.SpawnWorkstreamRequest{Project: "demo"}))
	must(err)
	ws := sp.Msg.GetWorkstream()
	sid := ws.GetSessionId()
	must(os.WriteFile(filepath.Join(ws.GetWorktreePath(), "only-in-ws.txt"), []byte("ws\n"), 0o644))
	read := func(session, path string) (*v1.ReadFileResponse, error) {
		resp, err := client.ReadFile(ctx, connect.NewRequest(&v1.ReadFileRequest{Project: "demo", SessionId: session, Path: path}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	got, err := read(sid, "only-in-ws.txt")
	must(err)
	if got.Root != ws.GetWorktreePath() || got.RootFallback || string(got.Data) != "ws\n" {
		t.Fatalf("live workstream read: %+v", got)
	}
	if _, err := read("", "only-in-ws.txt"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("project-root read of worktree-only file: %v", err)
	}

	// Once the session is no longer live, the registry still maps it to the
	// in-flight worktree.
	must(mgr.Stop(sid))
	got, err = read(sid, "only-in-ws.txt")
	must(err)
	if got.Root != ws.GetWorktreePath() || got.RootFallback {
		t.Fatalf("stopped workstream read: %+v", got)
	}

	// Discarded: the worktree is reclaimed, so the project root answers and
	// says so.
	_, err = client.DiscardWorkstream(ctx, connect.NewRequest(&v1.DiscardWorkstreamRequest{WorkstreamId: ws.GetId()}))
	must(err)
	got, err = read(sid, "README.md")
	must(err)
	if got.Root != proj || !got.RootFallback {
		t.Fatalf("post-discard read: root=%s fallback=%v", got.Root, got.RootFallback)
	}
	lf, err := client.ListFiles(ctx, connect.NewRequest(&v1.ListFilesRequest{Project: "demo", SessionId: sid}))
	must(err)
	if !lf.Msg.RootFallback {
		t.Fatalf("post-discard list: fallback not reported")
	}

	// An unrelated session id is simply the project root, no fallback.
	got, err = read("s_unknown", "README.md")
	must(err)
	if got.Root != proj || got.RootFallback {
		t.Fatalf("unknown-session read: %+v", got)
	}
}
