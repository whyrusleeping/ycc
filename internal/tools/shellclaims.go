package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/credenv"
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
	snap := &shellSnapshot{top: top, entries: make(map[string]fileStamp, len(records))}
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
// dirty or changed since before was taken and that no other scope claims.
func (w *Workspace) claimShellChanges(before *shellSnapshot) {
	if before == nil {
		return
	}
	after := w.snapshotShell()
	if after == nil || after.top != before.top {
		return
	}
	scope := w.MutationToken.Scope()
	claims := w.Ownership.Claims(after.top)
	var changed []string
	for rel, stamp := range after.entries {
		if prior, ok := before.entries[rel]; ok && prior == stamp {
			continue
		}
		if w.SharedBookkeeping != nil && w.SharedBookkeeping(filepath.Join(after.top, filepath.FromSlash(rel))) {
			continue
		}
		foreign := false
		for _, owner := range claims[rel] {
			if owner != scope {
				foreign = true
				break
			}
		}
		if !foreign {
			changed = append(changed, rel)
		}
	}
	w.Ownership.RecordWrites(after.top, changed, w.MutationToken)
}
