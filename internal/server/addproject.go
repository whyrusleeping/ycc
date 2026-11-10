package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/whyrusleeping/ycc/internal/credenv"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/forge"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// cloneTimeout bounds one AddProject clone. The RPC blocks while it runs.
const cloneTimeout = 15 * time.Minute

// AddProject registers a workspace under an optional name. With create it
// first makes the directory a new git repository; with clone_url it first
// clones into it. Otherwise the directory must already exist.
func (s *Server) AddProject(ctx context.Context, req *connect.Request[v1.AddProjectRequest]) (*connect.Response[v1.AddProjectResponse], error) {
	msg := req.Msg
	if msg.Path == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errNoPath)
	}
	switch {
	case msg.Create && msg.CloneUrl != "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("create and clone_url are mutually exclusive"))
	case msg.Create || msg.CloneUrl != "":
		if err := makeWorkspace(ctx, msg.Path, msg.CloneUrl); err != nil {
			return nil, err
		}
	default:
		info, err := os.Stat(msg.Path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no such directory: %s (create it as a new folder instead)", msg.Path))
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if !info.IsDir() {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("not a directory: "+msg.Path))
		}
	}
	p, err := s.mgr.AddProject(msg.Path, msg.Name)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.AddProjectResponse{Project: &v1.ProjectInfo{
		Name: p.Name, Path: p.Path,
		NeedsOnboarding: proto.Bool(docs.NeedsOnboarding(p.Path)),
	}}), nil
}

// makeWorkspace creates path as a new git repository, by cloning when
// cloneURL is set and by `git init` otherwise. path must be absolute and
// either missing or an empty directory. A failure removes a directory this
// call created.
func makeWorkspace(ctx context.Context, path, cloneURL string) error {
	if !filepath.IsAbs(path) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("path must be absolute"))
	}
	path = filepath.Clean(path)
	var src cloneSource
	if cloneURL != "" {
		var err error
		if src, err = parseCloneSource(cloneURL); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	existed, err := checkNewTarget(path)
	if err != nil {
		return err
	}
	if cloneURL != "" {
		err = os.MkdirAll(filepath.Dir(path), 0o755)
		if err == nil {
			err = cloneRepo(ctx, src, path)
		}
	} else {
		err = os.MkdirAll(path, 0o755)
		if err == nil {
			err = runQuiet(ctx, path, "git", "init", "-q")
		}
	}
	if err == nil {
		return nil
	}
	if !existed {
		_ = os.RemoveAll(path)
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	return connect.NewError(connect.CodeInternal, err)
}

// checkNewTarget accepts a missing path or an empty directory, reporting
// whether it already existed.
func checkNewTarget(path string) (existed bool, err error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, connect.NewError(connect.CodeInternal, err)
	}
	if !info.IsDir() {
		return false, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("%s already exists and is not a directory", path))
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, connect.NewError(connect.CodeInternal, err)
	}
	if len(entries) > 0 {
		return false, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("%s already exists and is not empty; add it as an existing folder instead", path))
	}
	return true, nil
}

// cloneSource is a validated clone_url: the URL git clones and, for GitHub
// repositories, the argument handed to `gh repo clone`.
type cloneSource struct {
	url    string
	github bool
	host   string
}

var (
	githubShorthand = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+$`)
	scpRemote       = regexp.MustCompile(`^[A-Za-z0-9_.-]+@[A-Za-z0-9_.-]+:[^\s]+$`)
	cloneSchemes    = []string{"https", "http", "ssh", "git", "file"}
)

// parseCloneSource validates a clone_url: `owner/repo` (GitHub), a URL with a
// git transport scheme, a scheme-less `host/owner/repo`, or an scp-style
// `user@host:path` remote. Anything else (including option-like input and
// git's ext:: transports) is rejected rather than handed to git.
func parseCloneSource(raw string) (cloneSource, error) {
	raw = strings.TrimSpace(raw)
	bad := func() (cloneSource, error) {
		return cloneSource{}, fmt.Errorf("%q is not a repository URL or owner/repo", raw)
	}
	if raw == "" || strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, " \t\r\n") {
		return bad()
	}
	if scpRemote.MatchString(raw) && !strings.Contains(raw, "://") {
		return withForge(cloneSource{url: raw}), nil
	}
	if !strings.Contains(raw, "://") {
		first, _, _ := strings.Cut(raw, "/")
		if githubShorthand.MatchString(raw) && !strings.Contains(first, ".") {
			return cloneSource{url: "https://github.com/" + strings.TrimSuffix(raw, ".git") + ".git", github: true, host: "github.com"}, nil
		}
		if !strings.Contains(first, ".") || !strings.Contains(raw, "/") {
			return bad()
		}
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || !slices.Contains(cloneSchemes, strings.ToLower(u.Scheme)) || (u.Host == "" && u.Scheme != "file") {
		return bad()
	}
	// A GitHub web URL copied from the browser (…/owner/repo/tree/main/dir)
	// names the repository by its first two segments.
	if strings.EqualFold(u.Hostname(), "github.com") && (u.Scheme == "https" || u.Scheme == "http") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return bad()
		}
		u.Path = "/" + parts[0] + "/" + strings.TrimSuffix(parts[1], ".git") + ".git"
		u.RawQuery, u.Fragment = "", ""
	}
	return withForge(cloneSource{url: u.String()}), nil
}

func withForge(src cloneSource) cloneSource {
	if kind, host, err := forge.Detect(src.url); err == nil && kind == forge.GitHub {
		src.github, src.host = true, host
	}
	return src
}

// cloneRepo clones src into dest, through an authenticated gh for GitHub
// repositories (so its login covers private repos) and plain git otherwise.
// Neither may prompt: the daemon has no one to answer.
func cloneRepo(ctx context.Context, src cloneSource, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	name, args := "git", []string{"clone", "--", src.url, dest}
	if src.github && ghCanClone(ctx, src.host) {
		name, args = "gh", []string{"repo", "clone", src.url, dest}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = filepath.Dir(dest)
	cmd.Env = cloneEnv(ctx, cmd.Dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("clone did not finish within %s", cloneTimeout))
	}
	text := cloneErrorText(out, err)
	// A missing repository and one the daemon cannot read look alike: hosts
	// answer both with an authentication challenge that prompting cannot meet.
	if strings.Contains(text, "terminal prompts disabled") || strings.Contains(text, "Permission denied (publickey)") ||
		strings.Contains(text, "Host key verification failed") {
		text += " (the repository may not exist, or it is private and the daemon's machine has no credentials for it: run `gh auth login` there or set up git credentials or an ssh key)"
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s clone failed: %s", name, text))
}

// ghCanClone reports whether gh is installed and logged in to host.
var ghCanClone = func(ctx context.Context, host string) bool {
	st := forge.Probe(ctx, forge.GitHub)
	return st.Ready() == nil && (len(st.Hosts) == 0 || slices.Contains(st.Hosts, host))
}

// cloneEnv is the daemon environment without its credentials and with git,
// gh, and ssh prompting disabled. A user's own ssh command (environment or
// core.sshCommand) is kept rather than replaced by the batch-mode default.
func cloneEnv(ctx context.Context, dir string) []string {
	env := append(credenv.Scrub(os.Environ()), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GH_PROMPT_DISABLED=1")
	if os.Getenv("GIT_SSH_COMMAND") == "" && os.Getenv("GIT_SSH") == "" {
		cmd := exec.CommandContext(ctx, "git", "config", "--get", "core.sshCommand")
		cmd.Dir = dir
		if out, _ := cmd.Output(); strings.TrimSpace(string(out)) == "" {
			env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
		}
	}
	return env
}

var urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@]+@`)

// cloneErrorText is the tail of a failed clone's output (git's fatal line is
// last), with any credentials embedded in URLs removed.
func cloneErrorText(out []byte, err error) string {
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "Cloning into") {
			lines = append(lines, l)
		}
	}
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	text := strings.Join(lines, " ")
	if text == "" {
		text = err.Error()
	}
	text = urlUserinfo.ReplaceAllString(text, "$1")
	if len(text) > 600 {
		text = "…" + text[len(text)-600:]
	}
	return text
}

// runQuiet runs a credential-scrubbed command in dir, returning its output as
// the error on failure.
func runQuiet(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = credenv.Scrub(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
