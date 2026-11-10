package server

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestParseCloneSource(t *testing.T) {
	ok := map[string]string{
		"whyrusleeping/ycc":                              "https://github.com/whyrusleeping/ycc.git",
		"whyrusleeping/ycc.git":                          "https://github.com/whyrusleeping/ycc.git",
		" https://github.com/o/r ":                       "https://github.com/o/r.git",
		"https://github.com/o/r/tree/main/docs?x=1#frag": "https://github.com/o/r.git",
		"github.com/o/r":                                 "https://github.com/o/r.git",
		"git@github.com:o/r.git":                         "git@github.com:o/r.git",
		"ssh://git@gitlab.com/g/sub/r.git":               "ssh://git@gitlab.com/g/sub/r.git",
		"https://git.example.com/team/r.git":             "https://git.example.com/team/r.git",
		"file:///srv/git/r.git":                          "file:///srv/git/r.git",
	}
	for in, want := range ok {
		src, err := parseCloneSource(in)
		if err != nil || src.url != want {
			t.Errorf("parseCloneSource(%q) = %q, %v; want %q", in, src.url, err, want)
		}
	}
	if src, _ := parseCloneSource("o/r"); !src.github {
		t.Errorf("owner/repo shorthand is not marked GitHub")
	}
	for _, in := range []string{
		"", "repo", "-uhttps://evil", "--upload-pack=touch /tmp/x", "ext::sh -c touch% /tmp/x",
		"ext::sh", "https://github.com/onlyowner", "a b/c", "ftp://host/r", "/srv/git/r.git",
	} {
		if src, err := parseCloneSource(in); err == nil {
			t.Errorf("parseCloneSource(%q) = %q, want error", in, src.url)
		}
	}
}

func TestCloneErrorTextStripsCredentials(t *testing.T) {
	out := []byte("Cloning into '/x'...\nremote: nope\nfatal: unable to access 'https://user:ghp_secret@github.com/o/r.git/': 403\n")
	got := cloneErrorText(out, errors.New("exit status 128"))
	if strings.Contains(got, "ghp_secret") || strings.Contains(got, "user:") {
		t.Fatalf("credentials leaked: %q", got)
	}
	if !strings.Contains(got, "fatal: unable to access 'https://github.com/o/r.git/'") {
		t.Fatalf("fatal line missing: %q", got)
	}
}

// TestAddProjectCreateCloneAndExisting covers the three ways to add a
// project: an existing directory (which must exist), a new folder (mkdir +
// git init), and a clone (into a missing or empty directory only).
func TestAddProjectCreateCloneAndExisting(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	srv := newListDirServer(t)
	ctx := context.Background()
	root := t.TempDir()
	add := func(req *v1.AddProjectRequest) (*v1.ProjectInfo, error) {
		resp, err := srv.AddProject(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return resp.Msg.Project, nil
	}

	// Existing: a missing directory is not registered.
	missing := filepath.Join(root, "missing")
	if _, err := add(&v1.AddProjectRequest{Path: missing}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing dir: err = %v, want not_found", err)
	}

	// New folder: parents are created and the folder is a git repository.
	fresh := filepath.Join(root, "nested", "fresh")
	p, err := add(&v1.AddProjectRequest{Path: fresh, Create: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.Name != "fresh" || p.Path != fresh || !isGitRepo(fresh) {
		t.Fatalf("create: project %+v, git=%v", p, isGitRepo(fresh))
	}
	// Creating over a non-empty directory is refused.
	if _, err := add(&v1.AddProjectRequest{Path: fresh, Create: true}); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("create over non-empty dir: err = %v, want already_exists", err)
	}

	// Clone: from a local repository with one commit.
	upstream := filepath.Join(root, "upstream")
	for _, args := range [][]string{
		{"init", "-q", upstream},
		{"-C", upstream, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	clone := filepath.Join(root, "work", "cloned")
	p, err = add(&v1.AddProjectRequest{Path: clone, CloneUrl: "file://" + upstream, Name: "mine"})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if p.Name != "mine" || !isGitRepo(clone) {
		t.Fatalf("clone: project %+v, git=%v", p, isGitRepo(clone))
	}

	// A failed clone reports git's error and leaves nothing behind.
	bad := filepath.Join(root, "work", "bad")
	_, err = add(&v1.AddProjectRequest{Path: bad, CloneUrl: "file://" + filepath.Join(root, "nope")})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "fatal") {
		t.Fatalf("bad clone: err = %v, want failed_precondition with git's message", err)
	}
	if _, statErr := os.Stat(bad); !os.IsNotExist(statErr) {
		t.Fatalf("bad clone left %s behind (stat err %v)", bad, statErr)
	}
}
