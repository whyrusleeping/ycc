package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// TrialMergeResult reports the outcome of a non-mutating trial merge.
type TrialMergeResult struct {
	// Clean is true when branch merges into the current base with no conflicts.
	Clean bool
	// Conflicts lists the paths that conflicted (empty when Clean).
	Conflicts []string
}

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

// TrialMerge detects whether branch merges cleanly into the repo's current HEAD
// without mutating the base branch or working tree (design §6 step 1). Because
// the host git may predate `git merge-tree --write-tree` (added in 2.38), it
// performs the trial inside a throwaway detached worktree checked out at HEAD,
// runs `git merge --no-commit --no-ff`, collects any conflicts, and tears the
// throwaway worktree down.
func (r *Repo) TrialMerge(branch string) (TrialMergeResult, error) {
	tmp, err := os.MkdirTemp("", "ycc-trialmerge-*")
	if err != nil {
		return TrialMergeResult{}, fmt.Errorf("trial merge tempdir: %w", err)
	}
	// The temp dir must not exist when `git worktree add` runs, otherwise git
	// refuses; remove it and let git recreate it.
	if err := os.Remove(tmp); err != nil {
		return TrialMergeResult{}, fmt.Errorf("trial merge tempdir: %w", err)
	}
	if _, err := r.run("worktree", "add", "--detach", tmp, "HEAD"); err != nil {
		os.RemoveAll(tmp)
		return TrialMergeResult{}, fmt.Errorf("trial merge worktree: %w", err)
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
// the repo's current HEAD (`git diff HEAD...branch`, i.e. the changes on branch
// since its merge base with HEAD). It is the review preview surfaced by the
// accept gate under interactive/judgement levels (design §6). Read-only.
func (r *Repo) DiffMergeBase(branch string) (string, error) {
	return r.run("diff", "HEAD..."+branch)
}
