package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/credenv"
	"github.com/whyrusleeping/ycc/internal/workspacelease"
)

// Shell write attribution. File tools claim the paths they write; shells
// cannot be intercepted, so a shell command is bracketed by two cheap
// snapshots of the worktree's dirty set (git status plus size/mtime/mode of
// each dirty path). Paths that became dirty or changed while the command ran
// are claimed for the shell's scope — unless another scope already claims
// them, since a concurrent session's writes can land in the same window.
// Attribution stays approximate (a concurrent unclaimed write can still be
// picked up), but codegen, formatters, and `go mod tidy` output now belong to
// the session that ran them instead of whoever commits next.

// shellSnapshotLimit bounds the dirty-set size worth stamping; beyond it the
// command's writes stay unclaimed rather than slowing every command down.
const shellSnapshotLimit = 20000

// shellSnapshotTimeout bounds each git status run.
const shellSnapshotTimeout = 3 * time.Second

type fileStamp struct {
	exists bool
	size   int64
	mtime  int64
	mode   os.FileMode
}

type shellSnapshot struct {
	top     string
	entries map[string]fileStamp
	gen     uint64 // claim generation when the snapshot was taken
}

// snapshotShell captures the worktree's dirty set for shell attribution, or
// nil when attribution is unavailable (no scoped ownership, not a git worktree,
// an oversized dirty set, or git failure).
func (w *Workspace) snapshotShell() *shellSnapshot {
	if w.Ownership == nil || w.MutationToken.Scope() == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), shellSnapshotTimeout)
	defer cancel()
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", w.Root}, args...)...)
		cmd.Env = credenv.Scrub(os.Environ())
		out, err := cmd.Output()
		return string(out), err
	}
	top, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return nil
	}
	top = strings.TrimSpace(top)
	out, err := git("status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return nil
	}
	records := strings.Split(out, "\x00")
	if len(records) > shellSnapshotLimit {
		return nil
	}
	snap := &shellSnapshot{top: top, entries: make(map[string]fileStamp, len(records)), gen: w.Ownership.Generation()}
	for _, record := range records {
		if len(record) <= 3 {
			continue
		}
		rel := record[3:]
		var stamp fileStamp
		if info, err := os.Lstat(filepath.Join(top, filepath.FromSlash(rel))); err == nil {
			stamp = fileStamp{exists: true, size: info.Size(), mtime: info.ModTime().UnixNano(), mode: info.Mode()}
		}
		snap.entries[rel] = stamp
	}
	return snap
}

// claimShellChanges claims, for this workspace's scope, every path that became
// dirty or changed since before was taken and that no other scope claims. A
// changed path that another scope had claimed before the command started is
// left to that scope, reported as an overlap and recorded as an overlap claim
// (workspacelease.Service.RecordOverlaps); the returned text is the overlap
// notice for the command's result.
func (w *Workspace) claimShellChanges(before *shellSnapshot) string {
	if before == nil {
		return ""
	}
	after := w.snapshotShell()
	if after == nil || after.top != before.top {
		return ""
	}
	scope := w.MutationToken.Scope()
	key, err := workspacelease.Canonical(after.top)
	if err != nil {
		return ""
	}
	claims := w.Ownership.ClaimDetails(after.top)
	var changed, overlapped []string
	var overlaps []workspacelease.Overlap
	for rel, stamp := range after.entries {
		if prior, ok := before.entries[rel]; ok && prior == stamp {
			continue
		}
		if w.SharedBookkeeping != nil && w.SharedBookkeeping(filepath.Join(after.top, filepath.FromSlash(rel))) {
			continue
		}
		foreign, renewed := false, false
		var earlier []string
		for _, claim := range claims[rel] {
			if claim.Scope == scope {
				continue
			}
			foreign = true
			// A claim renewed while the command ran may be that scope's own
			// concurrent write, so only older claims count as an overlap.
			if claim.Gen <= before.gen {
				earlier = append(earlier, claim.Scope)
			} else {
				renewed = true
			}
		}
		if !foreign {
			changed = append(changed, rel)
		} else if len(earlier) > 0 {
			if !renewed {
				// With no other write recorded during the command, the change
				// is most likely this command's.
				overlapped = append(overlapped, rel)
			}
			overlaps = append(overlaps, workspacelease.Overlap{Worktree: key, Path: rel, Writer: scope, Others: earlier})
		}
	}
	w.Ownership.RecordWrites(after.top, changed, w.MutationToken)
	// The change may still be another writer's, so it is only an overlap
	// claim: the path stays out of this session's changeset, but the earlier
	// claimant's commit sees that a live session also changed it.
	w.Ownership.RecordOverlaps(after.top, overlapped, w.MutationToken)
	sort.Slice(overlaps, func(i, j int) bool { return overlaps[i].Path < overlaps[j].Path })
	return w.Ownership.NotifyOverlaps(overlaps)
}
