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
// claimed by whichever finishes first. The narrowest window wins: another
// scope's background job does not count against a foreground command shorter
// than shortShellLimit, nor such a command against a job, so a job running for
// hours does not leave every write ambiguous. Attribution stays approximate (a
// concurrent hand edit can still be picked up), but codegen, formatters, and
// `go mod tidy` output belong to the session that ran them instead of whoever
// commits next.

// shellSnapshotLimit bounds the dirty-set size worth stamping; beyond it the
// command's writes stay unclaimed rather than slowing every command down.
const shellSnapshotLimit = 20000

// shellSnapshotTimeout bounds each git status run.
const shellSnapshotTimeout = 3 * time.Second

// shortShellLimit is the longest foreground command that another scope's
// background job does not count as concurrent with (workspacelease.EndShell).
const shortShellLimit = time.Minute

type fileStamp = workspacelease.PathStamp

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
// without scoped ownership. background marks a background job. Outside a git
// worktree, after a git failure, or for an oversized dirty set, the dirty set
// is left out (dirty false), but HEAD state is still captured. Call it before
// the command starts.
func (w *Workspace) snapshotShell(command string, background bool) *shellSnapshot {
	if w.Ownership == nil || w.MutationToken.Scope() == "" {
		return nil
	}
	// The command is registered before its first snapshot, so another scope's
	// shell that finishes after it sees it running.
	snap, gitDir := w.snapshotDirty(func(snap *shellSnapshot) {
		if key, err := workspacelease.Canonical(snap.top); err == nil {
			snap.key = key
			snap.run = w.Ownership.BeginShell(key, w.MutationToken, background)
		}
	})
	if snap == nil {
		// Outside a git worktree only the worktrees command targets are watched.
		snap = &shellSnapshot{}
	}
	if snap.run != nil && snap.dirty {
		// A background job ending while this command runs compares against it.
		w.Ownership.SetShellSnapshot(snap.run, snap.entries)
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
			stamp = fileStamp{Exists: true, Size: info.Size(), Mtime: info.ModTime().UnixNano(), Mode: info.Mode()}
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
	var overlaps []workspacelease.Overlap
	var ambiguous []string
	// Ending the registration and recording the claims form one step, so a
	// shell ending meanwhile sees either this one running or its claims.
	w.Ownership.AttributeShell(func() {
		overlap := w.Ownership.EndShell(before.run, shortShellLimit)
		if after != nil && after.top == before.top && after.dirty && before.key != "" {
			overlaps, ambiguous = w.claimDirtyChanges(before, after, before.key, scope, overlap)
		}
	})
	if notice := w.dirtyNotices(overlaps, ambiguous); notice != "" {
		notices = append(notices, notice)
	}
	if notice := w.sharedHeadNotices(before, scope); notice != "" {
		notices = append(notices, notice)
	}
	return strings.Join(notices, "\n")
}

// claimDirtyChanges is claimShellChanges' attribution of the dirty set. It
// returns the overlaps to notify and the paths left ambiguous. overlap names the other scopes whose shells ran in the worktree while the
// command did (Ran), and those of them that count as concurrent with it
// (Concurrent: not a background job against a short foreground command, or the
// reverse). Only ordinary (definite) claims decide a path:
//   - a background job ending while another scope's short foreground command
//     still runs skips paths whose state differs from that command's starting
//     snapshot; the command attributes them when it ends;
//   - with none from another scope, the path is claimed for this scope when no
//     concurrent shell ran or this scope already claims it definitely, and
//     otherwise recorded as ambiguous for this scope and every concurrent one
//     whose session has not ended;
//   - a path claimed by a scope whose shell ran meanwhile is left to it with
//     no overlap, since the change is as likely that scope's own shell write;
//   - a path another scope had claimed before the command started is left to
//     that scope, reported as an overlap and recorded as an overlap claim
//     (workspacelease.Service.RecordOverlaps).
func (w *Workspace) claimDirtyChanges(before, after *shellSnapshot, key, scope string, overlap workspacelease.ShellOverlap) ([]workspacelease.Overlap, []string) {
	claims := w.Ownership.ClaimDetails(after.top)
	concurrent := overlap.Concurrent
	running := make(map[string]bool, len(overlap.Ran))
	for _, other := range overlap.Ran {
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
		if overlap.InShortWindow(rel, stamp) {
			// A background job ending while another session's short command
			// runs leaves what changed in that command's window to it.
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
	// Sessions that ended, or ended and reopened, since their shell began get
	// no ambiguous claim.
	w.Ownership.RecordShellAmbiguous(after.top, ambiguous, overlap.Claimants)
	sort.Slice(overlaps, func(i, j int) bool { return overlaps[i].Path < overlaps[j].Path })
	return overlaps, ambiguous
}

// dirtyNotices notifies overlaps (outside the attribution lock) and returns
// the result notice for them and for the paths left ambiguous.
func (w *Workspace) dirtyNotices(overlaps []workspacelease.Overlap, ambiguous []string) string {
	notices := w.Ownership.NotifyOverlaps(overlaps)
	if len(ambiguous) > 0 {
		sort.Strings(ambiguous)
		note := fmt.Sprintf("[shell attribution] Another session's shell ran in this worktree while this command did, so these changed paths "+
			"may be either session's work and are recorded as ambiguous for each (%d): %s. Another session's background job does not count "+
			"against a foreground command shorter than %s, nor such a command against a job. A file-tool write, a shell write while no "+
			"concurrent shell of another session runs, or a commit makes a claim definite, and a session's ambiguous claims are dropped "+
			"when it ends; if a path is not this session's work, its coordinator can release it with release_claim.",
			len(ambiguous), boundedPaths(ambiguous, 20), shortShellLimit)
		notices = strings.TrimPrefix(notices+"\n"+note, "\n")
	}
	return notices
}

// endShell ends before's registration for a command that never ran.
func (w *Workspace) endShell(before *shellSnapshot) {
	if before != nil {
		w.Ownership.AttributeShell(func() { w.Ownership.EndShell(before.run, shortShellLimit) })
	}
}

// boundedPaths joins at most limit paths, noting how many more there are.
func boundedPaths(paths []string, limit int) string {
	if len(paths) <= limit {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:limit], ", ") + fmt.Sprintf(", … %d more", len(paths)-limit)
}
