package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/whyrusleeping/ycc/internal/credenv"
)

// Attribution narrows a changeset to one scope's work in a worktree shared
// with other sessions or agents. Paths are repository-relative (slash
// separated). A nil *Attribution keeps the strict single-owner rules: every
// change since the baseline belongs to the caller and a baseline-dirty path that
// changed again is refused as ambiguous.
//
// With an Attribution:
//   - a path that only other scopes wrote (Foreign and not Mine) is excluded;
//   - a path both wrote is included and reported as Shared;
//   - a baseline-dirty path this scope edited, or shared bookkeeping, is
//     adopted (Preexisting);
//   - a baseline-dirty path another scope is writing is left out (Deferred)
//     instead of failing the whole changeset (an unclaimed change to one is
//     still refused as ambiguous);
//   - unclaimed changes (shell output, structured docs writes) remain
//     attributed to whichever scope inspects them, as before.
type Attribution struct {
	Mine    map[string]bool
	Foreign map[string]bool
	// Bookkeeping, when set, reports shared bookkeeping paths (backlog task
	// files, memory) that every session writes. A baseline-dirty bookkeeping
	// path that changed since the baseline is adopted like the session's own
	// edit, so bookkeeping travels with whichever session commits next instead
	// of blocking or being stranded.
	Bookkeeping func(path string) bool
}

// Baseline returns the (possibly rebased) baseline this changeset was computed
// against.
func (c *Changeset) Baseline() *Baseline {
	if c == nil {
		return nil
	}
	return c.baseline
}

// rebaseBaseline moves b onto head, a descendant of b.head. A path changed by
// the intervening commits takes its committed content in both the baseline
// index and worktree snapshots — so it counts as clean pre-task state and work
// another scope already committed is never re-attributed — when it was clean at
// the baseline, or when the commit took exactly its baseline-dirty content. A
// baseline-dirty path committed only in part (e.g. a user committing staged
// hunks) keeps its original baseline entries, so its leftover changes remain
// pre-existing rather than becoming this session's work. Results are cached per
// HEAD on the original baseline.
func (r *Repo) rebaseBaseline(b *Baseline, head string) (*Baseline, error) {
	b.rebaseMu.Lock()
	defer b.rebaseMu.Unlock()
	if b.rebased != nil && b.rebasedHead == head {
		return b.rebased, nil
	}
	out, err := r.run("diff-tree", "-r", "-z", "--raw", "--no-commit-id", "--no-renames", b.head, head)
	if err != nil {
		return nil, fmt.Errorf("list committed paths: %w", err)
	}
	type entry struct{ mode, oid string }
	type change struct{ before, after entry }
	committed := make(map[string]change)
	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) < 5 {
			continue
		}
		committed[fields[i+1]] = change{before: entry{meta[0], meta[2]}, after: entry{meta[1], meta[3]}}
	}
	absent := func(e entry) bool { return strings.Trim(e.mode, "0") == "" }

	// Baseline-dirty committed paths need their baseline snapshot entries: the
	// worktree snapshot is overlaid only when HEAD now holds exactly that
	// content, and the index snapshot only when it was unstaged (equal to the
	// old HEAD) or staged exactly what was committed.
	var dirtyCommitted []string
	for path := range committed {
		if _, dirty := b.dirtyPaths[path]; dirty {
			dirtyCommitted = append(dirtyCommitted, path)
		}
	}
	snapshotEntries := func(tree string) (map[string]entry, error) {
		entries := make(map[string]entry, len(dirtyCommitted))
		for start := 0; start < len(dirtyCommitted); start += 256 {
			chunk := dirtyCommitted[start:min(start+256, len(dirtyCommitted))]
			args := append([]string{"ls-tree", "-z", "--full-tree", tree, "--"}, topLiteralPathspecs(chunk)...)
			listing, err := r.run(args...)
			if err != nil {
				return nil, err
			}
			for _, record := range strings.Split(listing, "\x00") {
				tab := strings.IndexByte(record, '\t')
				if tab < 0 {
					continue
				}
				if meta := strings.Fields(record[:tab]); len(meta) == 3 {
					entries[record[tab+1:]] = entry{mode: meta[0], oid: meta[2]}
				}
			}
		}
		return entries, nil
	}
	baseWorktree, err := snapshotEntries(b.worktreeTree)
	if err != nil {
		return nil, err
	}
	baseIndex, err := snapshotEntries(b.indexTree)
	if err != nil {
		return nil, err
	}
	same := func(snapshot map[string]entry, path string, want entry) bool {
		got, present := snapshot[path]
		if !present {
			return absent(want)
		}
		return got == want
	}

	var indexInfo, worktreeInfo bytes.Buffer
	for path, c := range committed {
		// Format "<mode> <oid>\t<path>"; mode 0 with a null oid removes the path.
		record := fmt.Sprintf("%s %s\t%s\x00", c.after.mode, c.after.oid, path)
		if _, dirty := b.dirtyPaths[path]; !dirty {
			indexInfo.WriteString(record)
			worktreeInfo.WriteString(record)
			continue
		}
		if same(baseIndex, path, c.before) || same(baseIndex, path, c.after) {
			indexInfo.WriteString(record)
		}
		if same(baseWorktree, path, c.after) {
			worktreeInfo.WriteString(record)
		}
	}
	indexTree, worktreeTree := b.indexTree, b.worktreeTree
	if indexInfo.Len() > 0 {
		if indexTree, err = r.overlayTree(b.indexTree, indexInfo.Bytes()); err != nil {
			return nil, err
		}
	}
	if worktreeInfo.Len() > 0 {
		if worktreeTree, err = r.overlayTree(b.worktreeTree, worktreeInfo.Bytes()); err != nil {
			return nil, err
		}
	}
	rebased, err := r.baseline(head, indexTree, worktreeTree)
	if err != nil {
		return nil, err
	}
	rebased.origin = b.Origin()
	b.rebasedHead, b.rebased = head, rebased
	return rebased, nil
}

// overlayTree applies NUL-terminated `update-index --index-info` records to
// base in a temporary index and returns the resulting tree.
func (r *Repo) overlayTree(base string, info []byte) (string, error) {
	dir, err := os.MkdirTemp("", "ycc-overlay-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}
	if _, err := r.runEnv(env, "read-tree", base); err != nil {
		return "", err
	}
	if err := r.runStdin(env, info, "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	out, err := r.runEnv(env, "write-tree")
	return strings.TrimSpace(out), err
}

func (r *Repo) runStdin(env []string, stdin []byte, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(credenv.Scrub(os.Environ()), env...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// CleanAgainstHEAD returns the subset of repository-relative paths whose index
// and worktree both match HEAD (no staged, unstaged, or untracked change).
func (r *Repo) CleanAgainstHEAD(paths []string) (map[string]bool, error) {
	clean := make(map[string]bool, len(paths))
	for _, path := range paths {
		clean[path] = true
	}
	for start := 0; start < len(paths); start += 256 {
		chunk := paths[start:min(start+256, len(paths))]
		args := append([]string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames", "--"}, topLiteralPathspecs(chunk)...)
		out, err := r.run(args...)
		if err != nil {
			return nil, err
		}
		for _, record := range strings.Split(out, "\x00") {
			if len(record) > 3 {
				delete(clean, record[3:])
			}
		}
	}
	return clean, nil
}
