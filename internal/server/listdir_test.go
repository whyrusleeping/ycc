package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func newListDirServer(t *testing.T) *Server {
	t.Helper()
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	return New(session.NewManager(reg, t.TempDir()))
}

// TestListDir lists directories only (files and hidden dirs omitted), sorted,
// with git-repo and registered-project annotations (task 0193).
func TestListDir(t *testing.T) {
	srv := newListDirServer(t)
	ctx := context.Background()

	root := t.TempDir()
	mkdir := func(parts ...string) string {
		t.Helper()
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mkdir("alpha", ".git") // git repo
	mkdir("beta")          // plain dir
	mkdir("gamma", ".git") // git repo, registered below
	gamma := filepath.Join(root, "gamma")
	mkdir(".hidden")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.AddProject(ctx, connect.NewRequest(&v1.AddProjectRequest{Path: gamma, Name: "gamma"})); err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	resp, err := srv.ListDir(ctx, connect.NewRequest(&v1.ListDirRequest{Path: root}))
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if resp.Msg.Path != root {
		t.Fatalf("path = %q, want %q", resp.Msg.Path, root)
	}
	if resp.Msg.Parent != filepath.Dir(root) {
		t.Fatalf("parent = %q, want %q", resp.Msg.Parent, filepath.Dir(root))
	}
	var got []string
	for _, e := range resp.Msg.Entries {
		got = append(got, e.Name)
	}
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries = %v, want %v", got, want)
		}
	}
	byName := map[string]*v1.DirEntry{}
	for _, e := range resp.Msg.Entries {
		byName[e.Name] = e
	}
	if !byName["alpha"].IsGitRepo || byName["alpha"].IsRegistered {
		t.Fatalf("alpha = %+v, want git-repo, not registered", byName["alpha"])
	}
	if byName["beta"].IsGitRepo || byName["beta"].IsRegistered {
		t.Fatalf("beta = %+v, want plain dir", byName["beta"])
	}
	if !byName["gamma"].IsGitRepo || !byName["gamma"].IsRegistered {
		t.Fatalf("gamma = %+v, want git-repo AND registered", byName["gamma"])
	}
}

// TestListDirDefaultsToHome resolves an empty path to the daemon user's home.
func TestListDirDefaultsToHome(t *testing.T) {
	srv := newListDirServer(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	resp, err := srv.ListDir(context.Background(), connect.NewRequest(&v1.ListDirRequest{}))
	if err != nil {
		t.Fatalf("ListDir(\"\"): %v", err)
	}
	if resp.Msg.Path != filepath.Clean(home) {
		t.Fatalf("path = %q, want home %q", resp.Msg.Path, home)
	}
}

// TestListDirErrors maps bad inputs to the right connect codes: relative paths
// and non-directories are InvalidArgument, missing paths NotFound.
func TestListDirErrors(t *testing.T) {
	srv := newListDirServer(t)
	ctx := context.Background()

	if _, err := srv.ListDir(ctx, connect.NewRequest(&v1.ListDirRequest{Path: "relative/path"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("relative path: code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := srv.ListDir(ctx, connect.NewRequest(&v1.ListDirRequest{Path: missing})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing path: code = %v, want NotFound", connect.CodeOf(err))
	}
	file := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ListDir(ctx, connect.NewRequest(&v1.ListDirRequest{Path: file})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("file path: code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

// TestListDirSuggestions returns git-repo siblings of registered projects,
// excluding the registered paths themselves and non-repos.
func TestListDirSuggestions(t *testing.T) {
	srv := newListDirServer(t)
	ctx := context.Background()

	code := t.TempDir()
	mk := func(name string, git bool) string {
		t.Helper()
		p := filepath.Join(code, name)
		sub := p
		if git {
			sub = filepath.Join(p, ".git")
		}
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	registered := mk("registered", true)
	other := mk("other", true)
	sibling := mk("sibling", true)
	mk("plain", false)

	for _, p := range []string{registered, other} {
		if _, err := srv.AddProject(ctx, connect.NewRequest(&v1.AddProjectRequest{Path: p})); err != nil {
			t.Fatalf("AddProject: %v", err)
		}
	}

	resp, err := srv.ListDir(ctx, connect.NewRequest(&v1.ListDirRequest{Path: code, Suggest: true}))
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(resp.Msg.Suggestions) != 1 || resp.Msg.Suggestions[0] != sibling {
		t.Fatalf("suggestions = %v, want [%s]", resp.Msg.Suggestions, sibling)
	}

	// Without suggest, the field stays empty.
	resp2, err := srv.ListDir(ctx, connect.NewRequest(&v1.ListDirRequest{Path: code}))
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(resp2.Msg.Suggestions) != 0 {
		t.Fatalf("suggestions without suggest = %v, want empty", resp2.Msg.Suggestions)
	}
	if resp.Msg.DefaultParent != code {
		t.Fatalf("default parent = %q, want %q (it holds the most projects)", resp.Msg.DefaultParent, code)
	}
}

// TestListDirSuggestionsRecentFirst orders suggestions by git activity and
// keeps only the most recent few.
func TestListDirSuggestionsRecentFirst(t *testing.T) {
	srv := newListDirServer(t)
	code := t.TempDir()
	registered := filepath.Join(code, "registered")
	if err := os.MkdirAll(filepath.Join(registered, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.AddProject(context.Background(), connect.NewRequest(&v1.AddProjectRequest{Path: registered})); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	base := time.Now().Add(-time.Hour)
	var want []string
	for i := range maxSuggestions + 3 {
		// Names run opposite to recency so alphabetical order would be wrong.
		p := filepath.Join(code, fmt.Sprintf("repo%02d", maxSuggestions+3-i))
		if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(filepath.Join(p, ".git"), stamp, stamp); err != nil {
			t.Fatal(err)
		}
		want = append([]string{p}, want...)
	}
	// Recent work inside .git (here a fetch) counts, not just the dir's mtime.
	oldest := filepath.Join(code, fmt.Sprintf("repo%02d", maxSuggestions+3))
	if err := os.WriteFile(filepath.Join(oldest, ".git", "FETCH_HEAD"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	want = append([]string{oldest}, want[:len(want)-1]...)[:maxSuggestions]

	resp, err := srv.ListDir(context.Background(), connect.NewRequest(&v1.ListDirRequest{Path: code, Suggest: true}))
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if !slices.Equal(resp.Msg.Suggestions, want) {
		t.Fatalf("suggestions = %v\nwant %v", resp.Msg.Suggestions, want)
	}
}
