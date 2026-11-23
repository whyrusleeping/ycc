package tools

import (
	"context"
	"fmt"
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
// Running shells are registered with the ownership service, so a command
// knows which other scopes' shells ran in its worktree meanwhile; a changed
// path nobody definitely claims is then ambiguous for all of them rather than
// claimed by whichever finishes first. Attribution stays approximate (a
// concurrent hand edit can still be picked up), but codegen, formatters, and
// `go mod tidy` output belong to the session that ran them instead of whoever
// commits next.

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
	key     string // canonical worktree of top
	run     *workspacelease.ShellRun
	entries map[string]fileStamp
	dirty   bool   // entries hold the dirty set (false when git status failed or it is oversized)
	gen     uint64 // claim generation when the snapshot was taken
	heads   []treeHead
}

// snapshotShell captures, before command runs, the worktree's dirty set and
// the HEAD state of the worktrees command may rewrite (sharedhead.go), or nil
// without scoped ownership. Outside a git worktree, after a git failure, or
// for an oversized dirty set, the dirty set is left out (dirty false), but
// HEAD state is still captured.
func (w *Workspace) snapshotShell(command string) *shellSnapshot {
	if w.Ownership == nil || w.MutationToken.Scope() == "" {
		return nil
	}
	scope := w.MutationToken.Scope()
	// The command is registered before its first snapshot, so another scope's
	// shell that finishes after it sees it running.
	snap, gitDir := w.snapshotDirty(func(snap *shellSnapshot) {
		if key, err := workspacelease.Canonical(snap.top); err == nil {
			snap.key = key
			snap.run = w.Ownership.BeginShell(key, scope)
		}
	})
	if snap == nil {
		// Outside a git worktree only the worktrees command targets are watched.
		snap = &shellSnapshot{}
	}
	snap.heads = w.watchHeads(snap.top, gitDir, command)
	return snap
}

// snapshotDirty captures the worktree's dirty set and returns its git
// directory, or nil when attribution is unavailable. located, when set, runs
// once the worktree is known and before git status.
func (w *Workspace) snapshotDirty(located func(*shellSnapshot)) (*shellSnapshot, string) {
	if w.Ownership == nil || w.MutationToken.Scope() == "" {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), shellSnapshotTimeout)
	defer cancel()
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", w.Root}, args...)...)
		cmd.Env = credenv.Scrub(os.Environ())
		out, err := cmd.Output()
		return string(out), err
	}
	out, err := git("rev-parse", "--show-toplevel", "--absolute-git-dir")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err != nil || len(lines) != 2 {
		return nil, ""
	}
	snap := &shellSnapshot{top: lines[0], gen: w.Ownership.Generation()}
	gitDir := lines[1]
	if located != nil {
		located(snap)
	}
	out, err = git("status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return snap, gitDir
	}
	records := strings.Split(out, "\x00")
	if len(records) > shellSnapshotLimit {
		return snap, gitDir
	}
	snap.dirty = true
	snap.entries = make(map[string]fileStamp, len(records))
	for _, record := range records {
		if len(record) <= 3 {
			continue
		}
		rel := record[3:]
		var stamp fileStamp
		if info, err := os.Lstat(filepath.Join(snap.top, filepath.FromSlash(rel))); err == nil {
			stamp = fileStamp{exists: true, size: info.Size(), mtime: info.ModTime().UnixNano(), mode: info.Mode()}
		}
		snap.entries[rel] = stamp
	}
	return snap, gitDir
}

// claimShellChanges attributes the paths that became dirty or changed since
// before was taken (claimDirtyChanges) and ends the command's registration. The
// returned text is the notice for the command's result: overlaps, ambiguous
// paths, and a rewrite or stash of a HEAD other live sessions share
// (sharedHeadNotices).
func (w *Workspace) claimShellChanges(before *shellSnapshot) string {
	if before == nil {
		return ""
	}
	scope := w.MutationToken.Scope()
	var notices []string
	var after *shellSnapshot
	if before.dirty {
		after, _ = w.snapshotDirty(nil)
	}
	concurrent := w.Ownership.EndShell(before.run)
	if after != nil && after.top == before.top && after.dirty && before.key != "" {
		if notice := w.claimDirtyChanges(before, after, before.key, scope, concurrent); notice != "" {
			notices = append(notices, notice)
		}
	}
	if notice := w.sharedHeadNotices(before, scope); notice != "" {
		notices = append(notices, notice)
	}
	return strings.Join(notices, "\n")
}

// claimDirtyChanges is claimShellChanges' attribution of the dirty set.
// concurrent names the other scopes whose shells ran in the worktree while the
// command did. Only ordinary (definite) claims decide a path:
//   - with none from another scope, the path is claimed for this scope when no
//     other shell ran or this scope already claims it definitely, and
//     otherwise recorded as ambiguous for this scope and every concurrent one;
//   - a path a concurrent scope claims is left to it with no overlap, since
//     the change is as likely that scope's own shell write;
//   - a path another scope had claimed before the command started is left to
//     that scope, reported as an overlap and recorded as an overlap claim
//     (workspacelease.Service.RecordOverlaps).
func (w *Workspace) claimDirtyChanges(before, after *shellSnapshot, key, scope string, concurrent []string) string {
	claims := w.Ownership.ClaimDetails(after.top)
	running := make(map[string]bool, len(concurrent))
	for _, other := range concurrent {
		running[other] = true
	}
	var changed, overlapped, ambiguous []string
	var overlaps []workspacelease.Overlap
	for rel, stamp := range after.entries {
		if prior, ok := before.entries[rel]; ok && prior == stamp {
			continue
		}
		if w.SharedBookkeeping != nil && w.SharedBookkeeping(filepath.Join(after.top, filepath.FromSlash(rel))) {
			continue
		}
		foreign, renewed, mine, mineAmbiguous, claimantRan := false, false, false, false, false
		var earlier []string
		for _, claim := range claims[rel] {
			if claim.Scope == scope && claim.Ambiguous {
				mineAmbiguous = true
			}
			if !claim.Definite() {
				continue
			}
			if claim.Scope == scope {
				mine = true
				continue
			}
			foreign = true
			switch {
			case running[claim.Scope]:
				claimantRan = true
			case claim.Gen <= before.gen:
				earlier = append(earlier, claim.Scope)
			default:
				// A claim renewed while the command ran may be that scope's
				// own concurrent write, so only older claims count as an
				// overlap.
				renewed = true
			}
		}
		switch {
		case !foreign && (len(concurrent) == 0 || mine):
			changed = append(changed, rel)
		case !foreign:
			ambiguous = append(ambiguous, rel)
		case claimantRan:
			// The claimant's own shell ran meanwhile: the change is most
			// likely its own, and this command may have only read.
		case len(earlier) > 0:
			switch {
			case renewed:
			case mineAmbiguous && len(concurrent) == 0:
				// The path is already in this scope's changeset; with no
				// other shell running, this write makes its claim definite
				// beside the other scope's.
				changed = append(changed, rel)
			default:
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
	w.Ownership.RecordAmbiguous(after.top, ambiguous, append([]string{scope}, concurrent...))
	sort.Slice(overlaps, func(i, j int) bool { return overlaps[i].Path < overlaps[j].Path })
	notices := w.Ownership.NotifyOverlaps(overlaps)
	if len(ambiguous) > 0 {
		sort.Strings(ambiguous)
		note := fmt.Sprintf("[shell attribution] Another session's shell ran in this worktree while this command did, so these changed paths "+
			"may be either session's work and are recorded as ambiguous for both (%d): %s. A file-tool write, a shell write while no "+
			"other session's shell runs, or a commit makes a claim definite; if a path is not this session's work, its coordinator "+
			"can release it with release_claim.", len(ambiguous), boundedPaths(ambiguous, 20))
		notices = strings.TrimPrefix(notices+"\n"+note, "\n")
	}
	return notices
}

// endShell ends before's registration for a command that never ran.
func (w *Workspace) endShell(before *shellSnapshot) {
	if before != nil {
		w.Ownership.EndShell(before.run)
	}
}

// boundedPaths joins at most limit paths, noting how many more there are.
func boundedPaths(paths []string, limit int) string {
	if len(paths) <= limit {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:limit], ", ") + fmt.Sprintf(", … %d more", len(paths)-limit)
}
