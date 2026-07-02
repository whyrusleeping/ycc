package git

import (
	"strings"
)

// Worktree is one entry from `git worktree list` (the primary tree plus any
// linked worktrees). See docs/design/parallel-workstreams.md §5.
type Worktree struct {
	// Path is the absolute path of the worktree's root directory.
	Path string
	// Head is the commit sha the worktree is checked out at.
	Head string
	// Branch is the checked-out branch ref (e.g. "refs/heads/ycc/ws/…"),
	// empty when the worktree is detached.
	Branch string
	// Detached is true when the worktree is on a detached HEAD.
	Detached bool
	// Prunable is true when git considers the admin entry stale/removable.
	Prunable bool
}

// AddWorktree creates a new linked worktree at dir, checking out a fresh branch
// created from baseRef (design §5 step 1: `git worktree add <dir> -b <branch>
// <base-commit>`). dir should live outside the primary tree.
func (r *Repo) AddWorktree(dir, branch, baseRef string) error {
	_, err := r.run("worktree", "add", dir, "-b", branch, baseRef)
	return err
}

// RemoveWorktree removes the linked worktree at dir (design §5 step 4). It uses
// --force so a worktree with a dirty/uncommitted tree can still be discarded;
// callers that want to preserve such changes must check beforehand.
func (r *Repo) RemoveWorktree(dir string) error {
	_, err := r.run("worktree", "remove", "--force", dir)
	return err
}

// PruneWorktrees reaps stale worktree admin entries left behind by removed or
// crashed worktrees (design §5 step 4: `git worktree prune`).
func (r *Repo) PruneWorktrees() error {
	_, err := r.run("worktree", "prune")
	return err
}

// DeleteBranch deletes a local branch. When force is true it uses `git branch
// -D` (discard even if unmerged); otherwise `git branch -d` (refuse to drop
// unmerged work). Used to clean up a workstream's branch after its worktree is
// removed (design §5 step 4).
func (r *Repo) DeleteBranch(name string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := r.run("branch", flag, name)
	return err
}

// ListWorktrees returns all worktrees known to the repo, including the primary
// tree, by parsing `git worktree list --porcelain`.
func (r *Repo) ListWorktrees() ([]Worktree, error) {
	out, err := r.run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var (
		trees []Worktree
		cur   *Worktree
	)
	flush := func() {
		if cur != nil {
			trees = append(trees, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			// Blank line terminates a worktree record.
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &Worktree{Path: val}
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = val
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
			}
		}
	}
	flush()
	return trees, nil
}
