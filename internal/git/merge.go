package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// TrialMergeResult reports the outcome of a non-mutating trial merge.
type TrialMergeResult struct {
	// Clean is true when branch merges into the selected base with no conflicts.
	Clean bool
	// Conflicts lists the paths that conflicted (empty when Clean).
	Conflicts []string
}

// RebaseResult reports the outcome of rebasing a worktree onto a base branch.
type RebaseResult struct {
	Clean     bool
	Conflicts []string
}

// ErrBaseTreeDirty is returned when advancing a checked-out base branch would
// disturb uncommitted changes in that branch's worktree.
var ErrBaseTreeDirty = errors.New("base tree dirty")

// MergeStrategy selects how Merge integrates a branch.
type MergeStrategy int

const (
	// MergeNoFF always creates a merge commit (`git merge --no-ff`). Default.
	MergeNoFF MergeStrategy = iota
	// MergeFFOnly only integrates when a fast-forward is possible
	// (`git merge --ff-only`); otherwise the merge fails.
	MergeFFOnly
)

// MergeResult reports the outcome of Merge.
type MergeResult struct {
	// Clean is true when the merge succeeded without conflicts.
	Clean bool
	// Commit is the resulting commit's short sha (set when Clean).
	Commit string
	// Conflicts lists conflicted paths when the merge could not complete; in
	// that case the base tree/HEAD has been restored (merge aborted).
	Conflicts []string
}

// runAllow runs git and returns stdout, stderr and the error (nil on success).
// Unlike run it does not wrap the error, so callers can inspect the outcome of
// commands that exit non-zero on expected conditions (e.g. merge conflicts).
func (r *Repo) runAllow(dir string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command("git", args...)
	if dir == "" {
		dir = r.Dir
	}
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

// conflictedPaths returns the unmerged paths in the worktree at dir.
func (r *Repo) conflictedPaths(dir string) []string {
	out, _, _ := r.runAllow(dir, "diff", "--name-only", "--diff-filter=U")
	var paths []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			paths = append(paths, l)
		}
	}
	return paths
}

// TrialMerge detects whether branch merges cleanly into base without mutating
// either branch or any existing working tree. Because the host git may predate
// `git merge-tree --write-tree` (added in 2.38), it performs the trial inside a
// throwaway detached worktree checked out at base, runs a no-commit merge,
// collects conflicts, and tears the throwaway worktree down.
func (r *Repo) TrialMerge(base, branch string) (TrialMergeResult, error) {
	tmp, err := os.MkdirTemp("", "ycc-trialmerge-*")
	if err != nil {
		return TrialMergeResult{}, fmt.Errorf("trial merge tempdir: %w", err)
	}
	// The temp dir must not exist when `git worktree add` runs, otherwise git
	// refuses; remove it and let git recreate it.
	if err := os.Remove(tmp); err != nil {
		return TrialMergeResult{}, fmt.Errorf("trial merge tempdir: %w", err)
	}
	if _, err := r.run("worktree", "add", "--detach", tmp, base); err != nil {
		os.RemoveAll(tmp)
		return TrialMergeResult{}, fmt.Errorf("trial merge worktree at %s: %w", base, err)
	}
	// Ensure the throwaway worktree never leaks, even on error paths.
	defer func() {
		r.run("worktree", "remove", "--force", tmp)
		os.RemoveAll(tmp)
		r.run("worktree", "prune")
	}()

	_, _, mergeErr := r.runAllow(tmp, "merge", "--no-commit", "--no-ff", branch)
	if mergeErr == nil {
		// Clean merge; abort to leave nothing staged (harmless in throwaway).
		r.runAllow(tmp, "merge", "--abort")
		return TrialMergeResult{Clean: true}, nil
	}
	conflicts := r.conflictedPaths(tmp)
	// Best-effort abort so the throwaway tree is not left mid-merge.
	r.runAllow(tmp, "merge", "--abort")
	if len(conflicts) == 0 {
		// Non-zero exit without conflicted paths means the merge failed for a
		// reason other than a content conflict (e.g. unknown branch).
		return TrialMergeResult{}, fmt.Errorf("trial merge %s failed: %v", branch, mergeErr)
	}
	return TrialMergeResult{Clean: false, Conflicts: conflicts}, nil
}

// DefaultBranch resolves the repository's default local integration branch. A
// configured origin/HEAD wins; repositories without one fall back to local main,
// local master, and finally the currently checked-out branch.
func (r *Repo) DefaultBranch() (string, error) {
	if out, _, err := r.runAllow("", "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		ref := strings.TrimSpace(out)
		if name := strings.TrimPrefix(ref, "refs/remotes/origin/"); name != "" && name != ref {
			return name, nil
		}
	}
	for _, name := range []string{"main", "master"} {
		if _, ok, err := r.LocalBranch(name); err != nil {
			return "", err
		} else if ok {
			return name, nil
		}
	}
	if out, _, err := r.runAllow("", "symbolic-ref", "--short", "HEAD"); err == nil {
		if name := strings.TrimSpace(out); name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("resolve default branch: origin/HEAD is unset, no local main or master exists, and HEAD is detached")
}

// LocalBranch reports whether ref names a local branch and returns its canonical
// short name. Both "main" and "refs/heads/main" are accepted.
func (r *Repo) LocalBranch(ref string) (string, bool, error) {
	name := strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
	if name == "" {
		return "", false, nil
	}
	_, stderr, err := r.runAllow("", "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	if err == nil {
		return name, true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "", false, nil
	}
	return "", false, fmt.Errorf("check local branch %q: %v: %s", name, err, strings.TrimSpace(stderr))
}

// IsAncestor reports whether a is an ancestor of b.
func (r *Repo) IsAncestor(a, b string) (bool, error) {
	_, stderr, err := r.runAllow("", "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %v: %s", a, b, err, strings.TrimSpace(stderr))
}

// RebaseOnto rebases the branch checked out at dir onto onto. Content conflicts
// are reported after a best-effort abort, leaving the worktree restored and
// available for continued work.
func (r *Repo) RebaseOnto(dir, onto string) (RebaseResult, error) {
	_, stderr, err := r.runAllow(dir, "rebase", onto)
	if err == nil {
		return RebaseResult{Clean: true}, nil
	}
	conflicts := r.conflictedPaths(dir)
	r.runAllow(dir, "rebase", "--abort")
	if len(conflicts) == 0 {
		return RebaseResult{}, fmt.Errorf("git rebase %s: %v: %s", onto, err, strings.TrimSpace(stderr))
	}
	return RebaseResult{Clean: false, Conflicts: conflicts}, nil
}

// checkBaseClean returns the worktree where base is checked out, if any, after
// verifying that tree has no staged, unstaged, or untracked changes.
func (r *Repo) checkBaseClean(base string) (string, error) {
	base = strings.TrimPrefix(base, "refs/heads/")
	trees, err := r.ListWorktrees()
	if err != nil {
		return "", err
	}
	baseRef := "refs/heads/" + base
	for _, wt := range trees {
		if wt.Branch != baseRef {
			continue
		}
		status, _, statusErr := r.runAllow(wt.Path, "status", "--porcelain")
		if statusErr != nil {
			return "", fmt.Errorf("check base tree %s: %w", wt.Path, statusErr)
		}
		if strings.TrimSpace(status) != "" {
			return "", fmt.Errorf("%w: %s", ErrBaseTreeDirty, wt.Path)
		}
		return wt.Path, nil
	}
	return "", nil
}

// CheckBaseClean preflights whether a checked-out base branch can be advanced
// without disturbing uncommitted work. A base not checked out anywhere is safe.
func (r *Repo) CheckBaseClean(base string) error {
	_, err := r.checkBaseClean(base)
	return err
}

// AdvanceBranch advances base to branch with fast-forward-only semantics without
// assuming anything about the primary checkout. If base is checked out, git runs
// in that worktree after a clean-tree check; otherwise fetch updates the ref
// directly without touching any worktree.
func (r *Repo) AdvanceBranch(base, branch string) (string, error) {
	base = strings.TrimPrefix(base, "refs/heads/")
	ok, err := r.IsAncestor(base, branch)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("cannot advance base branch %q to %q: not a fast-forward", base, branch)
	}

	// Repeat the cleanliness check here even when a caller preflighted before a
	// rebase: the base worktree may have become dirty in the meantime.
	checkedOutDir, err := r.checkBaseClean(base)
	if err != nil {
		return "", err
	}
	if checkedOutDir != "" {
		if _, stderr, mergeErr := r.runAllow(checkedOutDir, "merge", "--ff-only", branch); mergeErr != nil {
			return "", fmt.Errorf("advance base branch %q in %s: %v: %s", base, checkedOutDir, mergeErr, strings.TrimSpace(stderr))
		}
	} else {
		if _, stderr, fetchErr := r.runAllow("", "fetch", ".", branch+":"+base); fetchErr != nil {
			return "", fmt.Errorf("advance base branch %q: %v: %s", base, fetchErr, strings.TrimSpace(stderr))
		}
	}
	sha, err := r.run("rev-parse", "--short", "refs/heads/"+base)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// Merge integrates branch into the repo's current branch (design §6). On a
// content conflict it runs `git merge --abort` so the base tree/HEAD is never
// left in a conflicted state, and returns a MergeResult listing the conflicted
// paths. On success it returns the resulting commit's short sha.
func (r *Repo) Merge(branch string, strategy MergeStrategy) (MergeResult, error) {
	var args []string
	switch strategy {
	case MergeFFOnly:
		args = []string{"merge", "--ff-only", branch}
	default:
		args = []string{"merge", "--no-ff", "-m", fmt.Sprintf("ycc: merge %s", branch), branch}
	}
	_, _, mergeErr := r.runAllow(r.Dir, args...)
	if mergeErr != nil {
		conflicts := r.conflictedPaths(r.Dir)
		// Restore base: abort the in-progress merge (best effort). --ff-only
		// failures leave nothing to abort, so ignore that error.
		r.runAllow(r.Dir, "merge", "--abort")
		if len(conflicts) == 0 {
			return MergeResult{}, fmt.Errorf("git merge %s: %v", branch, mergeErr)
		}
		return MergeResult{Clean: false, Conflicts: conflicts}, nil
	}
	sha, err := r.run("rev-parse", "--short", "HEAD")
	if err != nil {
		return MergeResult{}, err
	}
	return MergeResult{Clean: true, Commit: strings.TrimSpace(sha)}, nil
}

// DiffMergeBase returns the integrated diff branch would introduce relative to
// base (`git diff base...branch`, i.e. changes on branch since their merge base).
// It is the read-only review preview surfaced by the explicit accept gate.
func (r *Repo) DiffMergeBase(base, branch string) (string, error) {
	return r.run("diff", base+"..."+branch)
}
