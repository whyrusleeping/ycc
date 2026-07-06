// Package git is a thin wrapper over the git CLI for the operations the
// coordinator needs: ensure a repo exists, capture the implementer's changes as
// a diff for review, and commit accepted work (spec §10).
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
}

// Open returns a Repo for dir, initializing one (with an initial empty commit) if
// dir is not already inside a git work tree. The initial commit gives diffs and
// HEAD a stable base.
func Open(dir string) (*Repo, error) {
	r := &Repo{Dir: dir}
	if out, err := r.run("rev-parse", "--is-inside-work-tree"); err == nil && strings.TrimSpace(out) == "true" {
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
	return r, nil
}

// Diff stages all changes and returns the staged diff — a stable snapshot of the
// implementer's work for reviewers. Returns "" when there are no changes.
func (r *Repo) Diff() (string, error) {
	if _, err := r.run("add", "-A"); err != nil {
		return "", err
	}
	return r.run("diff", "--cached")
}

// Commit stages all changes and commits them, returning the new commit's short
// sha. Returns an error if there is nothing to commit.
func (r *Repo) Commit(message string) (string, error) {
	if _, err := r.run("add", "-A"); err != nil {
		return "", err
	}
	if out, err := r.run("status", "--porcelain"); err == nil && strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("nothing to commit")
	}
	if _, err := r.run("commit", "-m", message); err != nil {
		return "", err
	}
	sha, err := r.run("rev-parse", "--short", "HEAD")
	return strings.TrimSpace(sha), err
}

// RevParse resolves a ref (branch, tag, or commit-ish) to its full commit sha.
func (r *Repo) RevParse(ref string) (string, error) {
	out, err := r.run("rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Show returns the full `git show` output (stat + patch) for a commit, for the
// transcript commit-diff drill-in (task 0140). sha must be a bare hex commit id
// (short or full) — anything else is rejected so a flag/ref/pathspec can never be
// smuggled into the git invocation. --end-of-options additionally guards the
// positional argument.
func (r *Repo) Show(sha string) (string, error) {
	if !shaRe.MatchString(sha) {
		return "", fmt.Errorf("invalid commit sha %q", sha)
	}
	return r.run("show", "--no-color", "--stat", "--patch", "--end-of-options", sha)
}

func (r *Repo) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
