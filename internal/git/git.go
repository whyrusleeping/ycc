// Package git is a thin wrapper over the git CLI for the operations the
// coordinator needs: ensure a repo exists, capture the implementer's changes as
// a diff for review, and commit accepted work.
package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// shaRe matches a bare hex commit sha (short or full). Show validates against it
// so a caller can never smuggle a flag (e.g. "--all") or a ref/pathspec into the
// `git show` invocation.
var shaRe = regexp.MustCompile(`^[0-9a-fA-F]{4,40}$`)

// Repo is a git working tree at Dir.
type Repo struct {
	Dir string

	openBaseline    *Baseline
	openBaselineErr error
}

// Open returns a Repo for dir, initializing one (with an initial empty commit) if
// dir is not already inside a git work tree. The initial commit gives diffs and
// HEAD a stable base. Open also attempts to capture the baseline used by session
// callers before they can expose mutation tools. Existing repositories remain
// open for conversation if capture is unavailable; OpenBaselineError records why
// ownership-sensitive operations must refuse.
func Open(dir string) (*Repo, error) {
	r := &Repo{Dir: dir}
	if out, err := r.run("rev-parse", "--is-inside-work-tree"); err == nil && strings.TrimSpace(out) == "true" {
		// An existing repository may have no HEAD yet, or snapshot inspection may
		// otherwise be unavailable. Keep it usable for conversation, but retain the
		// failure so session callers can disable review and commit rather than
		// recapturing after mutation or auto-committing the user's dirty files.
		r.openBaseline, r.openBaselineErr = r.CaptureBaseline()
		return r, nil
	}
	if _, err := r.run("init"); err != nil {
		return nil, fmt.Errorf("git init: %w", err)
	}
	// Ensure identity so commits work in fresh/CI environments.
	r.run("config", "user.email", "ycc@localhost")
	r.run("config", "user.name", "ycc")
	// Keep session state out of git unless the workspace already says otherwise.
	gitignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitignore); os.IsNotExist(err) {
		os.WriteFile(gitignore, []byte(".ycc/\n"), 0o644)
	}
	r.run("add", "-A")
	if _, err := r.run("commit", "--allow-empty", "-m", "ycc: initialize workspace"); err != nil {
		return nil, fmt.Errorf("git initial commit: %w", err)
	}
	baseline, err := r.CaptureBaseline()
	if err != nil {
		return nil, err
	}
	r.openBaseline = baseline
	return r, nil
}

// OpenExisting returns a Repo for an existing working tree without capturing a
// new baseline. Session reopen uses it so task edits present after a restart are
// never even provisionally represented as the session's pre-task state.
func OpenExisting(dir string) (*Repo, error) {
	r := &Repo{Dir: dir}
	if out, err := r.run("rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		if err == nil {
			err = fmt.Errorf("not inside a git work tree")
		}
		return nil, fmt.Errorf("open existing git workspace: %w", err)
	}
	return r, nil
}

// OpenBaseline returns the immutable repository snapshot captured by Open.
// It is useful to wire callers that construct their dependency graph after
// opening the repository but before any task mutation.
func (r *Repo) OpenBaseline() *Baseline { return r.openBaseline }

// OpenBaselineError reports why Open could not capture a baseline for an
// existing repository. Open itself still succeeds so read-only/conversational
// use remains available; ownership-sensitive review and commit must refuse.
func (r *Repo) OpenBaselineError() error { return r.openBaselineErr }

// RevParse resolves a ref (branch, tag, or commit-ish) to its full commit sha.
func (r *Repo) RevParse(ref string) (string, error) {
	out, err := r.run("rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Show returns the full `git show` output (stat + patch) for a commit, for the
// transcript commit-diff drill-in. sha must be a bare hex commit id
// (short or full) — anything else is rejected so a flag/ref/pathspec can never be
// smuggled into the git invocation. --end-of-options additionally guards the
// positional argument.
func (r *Repo) Show(sha string) (string, error) {
	if !shaRe.MatchString(sha) {
		return "", fmt.Errorf("invalid commit sha %q", sha)
	}
	return r.run("show", "--no-color", "--stat", "--patch", "--end-of-options", sha)
}

// SyncStatus is a cheap, local snapshot of how the working tree relates to its
// upstream tracking branch. It reads only refs already present locally (it does
// NOT contact the remote — call Fetch first to refresh them), so it is safe to
// call frequently and never blocks on the network or triggers auth prompts.
type SyncStatus struct {
	Branch      string // current branch name; "" when detached HEAD
	HasUpstream bool   // whether the branch has a configured upstream
	Ahead       int    // commits on HEAD not on upstream
	Behind      int    // commits on upstream not on HEAD
	Dirty       bool   // uncommitted changes (staged, unstaged, or untracked)
}

// Status returns a cheap, local SyncStatus (see the type doc). Ahead/Behind are
// zero when there is no upstream tracking branch (HasUpstream false).
func (r *Repo) Status() (SyncStatus, error) {
	var s SyncStatus
	// Current branch (empty/"HEAD" when detached).
	if out, err := r.run("rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		if b := strings.TrimSpace(out); b != "HEAD" {
			s.Branch = b
		}
	}
	// Dirty check — porcelain lists staged, unstaged, and untracked changes.
	if out, err := r.run("status", "--porcelain"); err == nil {
		s.Dirty = strings.TrimSpace(out) != ""
	}
	// Ahead/behind vs. upstream. `git rev-list --count --left-right @{u}...HEAD`
	// prints "<behind>\t<ahead>". A missing upstream is a normal, non-fatal
	// state (detached HEAD, no tracking branch), so we swallow the error.
	if out, err := r.run("rev-list", "--count", "--left-right", "@{upstream}...HEAD"); err == nil {
		fields := strings.Fields(strings.TrimSpace(out))
		if len(fields) == 2 {
			s.HasUpstream = true
			fmt.Sscanf(fields[0], "%d", &s.Behind)
			fmt.Sscanf(fields[1], "%d", &s.Ahead)
		}
	}
	return s, nil
}

// Fetch updates remote-tracking refs from the branch's upstream remote (or
// origin) WITHOUT modifying the working tree. It performs network I/O and may
// be slow or fail (offline, auth) — callers should treat failure as non-fatal
// and fall back to the last cached Status. Returns an error on failure.
// Credential prompts are disabled: Fetch runs from background pollers where an
// interactive prompt would hang the daemon or scribble over the TUI, so a
// remote needing interactive auth fails fast instead.
func (r *Repo) Fetch() error {
	cmd := exec.Command("git", "fetch", "--quiet")
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git fetch: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (r *Repo) run(args ...string) (string, error) {
	return r.runWithEnv(nil, args...)
}

func (r *Repo) runWithEnv(env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
