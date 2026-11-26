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
// knows which other scopes' shells, foreground or background, ran in its
// worktree meanwhile; a changed path nobody definitely claims is then left
// unattributed rather than claimed by whichever finishes first: it is in no
// session's changeset unless a session names it in a commit. Attribution stays
// approximate (a concurrent hand edit can still be picked up), but codegen,
// formatters, and `go mod tidy` output belong to the session that ran them
// instead of whoever commits next.

// shellSnapshotLimit bounds the dirty-set size worth stamping; beyond it the
// command's writes stay unclaimed rather than slowing every command down.
const shellSnapshotLimit = 20000

// shellSnapshotTimeout bounds each git status run.
const shellSnapshotTimeout = 3 * time.Second

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
// returned text is the notice for the command's result: overlaps, unattributed
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
	var unattributed []string
	var overlap workspacelease.ShellOverlap
	// Ending the registration and recording the claims form one step, so a
	// shell ending meanwhile sees either this one running or its claims.
	w.Ownership.AttributeShell(func() {
		overlap = w.Ownership.EndShell(before.run)
		if after != nil && after.top == before.top && after.dirty && before.key != "" {
			overlaps, unattributed = w.claimDirtyChanges(before, after, before.key, scope, overlap)
		}
	})
	if notice := w.dirtyNotices(overlaps, unattributed, overlap); notice != "" {
		notices = append(notices, notice)
	}
	if notice := w.sharedHeadNotices(before, scope); notice != "" {
		notices = append(notices, notice)
	}
	return strings.Join(notices, "\n")
}

// claimDirtyChanges is claimShellChanges' attribution of the dirty set. It
// returns the overlaps to notify and the paths left unattributed. overlap names
// the other scopes whose shells, foreground or background, ran in the worktree
// while the command did. Only ordinary (definite) claims decide a path:
//   - with none from another scope, the path is claimed for this scope when no
//     other scope's shell ran or this scope already claims it definitely, and
//     otherwise left unattributed;
//   - a path another scope claims is left to it with no overlap when any other
//     scope's shell ran meanwhile, since the change may be that shell's write
//     (the claimant's own, or a third scope's) and this command may only have
//     read;
//   - otherwise a path another scope had claimed before the command started
//     is left to that scope, reported as an overlap and recorded as an overlap
//     claim (workspacelease.Service.RecordOverlaps).
func (w *Workspace) claimDirtyChanges(before, after *shellSnapshot, key, scope string, overlap workspacelease.ShellOverlap) ([]workspacelease.Overlap, []string) {
	claims := w.Ownership.ClaimDetails(after.top)
	var changed, overlapped, unattributed []string
	var overlaps []workspacelease.Overlap
	for rel, stamp := range after.entries {
		if prior, ok := before.entries[rel]; ok && prior == stamp {
			continue
		}
		if w.SharedBookkeeping != nil && w.SharedBookkeeping(filepath.Join(after.top, filepath.FromSlash(rel))) {
			continue
		}
		foreign, renewed, mine := false, false, false
		var earlier []string
		for _, claim := range claims[rel] {
			if !claim.Definite() {
				continue
			}
			if claim.Scope == scope {
				mine = true
				continue
			}
			foreign = true
			switch {
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
		case !foreign && (len(overlap.Ran) == 0 || mine):
			changed = append(changed, rel)
		case !foreign:
			unattributed = append(unattributed, rel)
		case len(overlap.Ran) > 0:
			// Another scope's shell ran meanwhile (perhaps the claimant's
			// own): the change is not reliably this command's, and an
			// overlap claim would block the claimant's commit on a guess.
		case len(earlier) > 0:
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
	w.Ownership.RecordUnattributed(after.top, unattributed)
	sort.Slice(overlaps, func(i, j int) bool { return overlaps[i].Path < overlaps[j].Path })
	return overlaps, unattributed
}

// dirtyNotices notifies overlaps (outside the attribution lock) and returns
// the result notice for them and for the paths left unattributed while the
// other sessions in overlap ran shells.
func (w *Workspace) dirtyNotices(overlaps []workspacelease.Overlap, unattributed []string, overlap workspacelease.ShellOverlap) string {
	notices := w.Ownership.NotifyOverlaps(overlaps)
	if len(unattributed) > 0 {
		sort.Strings(unattributed)
		jobs := make(map[string]bool, len(overlap.Jobs))
		for _, scope := range overlap.Jobs {
			jobs[scope] = true
		}
		others := make([]string, 0, len(overlap.Ran))
		for _, scope := range overlap.Ran {
			if jobs[scope] {
				scope += " (background job)"
			}
			others = append(others, scope)
		}
		note := fmt.Sprintf("[shell attribution] Shells of other sessions ran in this worktree while this command did (%s), so these "+
			"changed paths may be either session's work and are left unattributed (%d): %s. They are in no session's changeset and "+
			"block no commit; if one is this session's work, name it in the commit tool's paths. A later file-tool write, or a "+
			"shell write while no other session's shell runs, attributes a path to its writer.",
			strings.Join(others, ", "), len(unattributed), boundedPaths(unattributed, 20))
		notices = strings.TrimPrefix(notices+"\n"+note, "\n")
	}
	return notices
}

// endShell ends before's registration for a command that never ran.
func (w *Workspace) endShell(before *shellSnapshot) {
	if before != nil {
		w.Ownership.AttributeShell(func() { w.Ownership.EndShell(before.run) })
	}
}

// recoveredShell registers a background shell that kept running across a
// daemon restart as a running background job in its worktree, so other
// scopes' shells see it overlap (the usual EndShell rules apply). It takes no dirty snapshot: such a
// shell's writes are never attributed automatically, and endShell only ends
// its registration.
func (w *Workspace) recoveredShell() *shellSnapshot {
	if w.Ownership == nil || w.MutationToken.Scope() == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), shellSnapshotTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", w.Root, "rev-parse", "--show-toplevel")
	cmd.Env = credenv.Scrub(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	key, err := workspacelease.Canonical(strings.TrimSpace(string(out)))
	if err != nil {
		return nil
	}
	return &shellSnapshot{top: strings.TrimSpace(string(out)), key: key,
		run: w.Ownership.BeginShell(key, w.MutationToken, true)}
}

// boundedPaths joins at most limit paths, noting how many more there are.
func boundedPaths(paths []string, limit int) string {
	if len(paths) <= limit {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:limit], ", ") + fmt.Sprintf(", … %d more", len(paths)-limit)
}
