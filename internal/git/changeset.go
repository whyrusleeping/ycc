package git

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Baseline is an immutable snapshot of HEAD, the index, and the visible
// worktree (including untracked files) captured before a task can mutate the
// repository. Dirty baseline paths are never assigned to the task.
type Baseline struct {
	ID string

	head         string
	indexTree    string
	worktreeTree string
	dirtyPaths   map[string]struct{}
}

type baselineRecord struct {
	Version      int    `json:"version"`
	ID           string `json:"id"`
	Head         string `json:"head"`
	IndexTree    string `json:"index_tree"`
	WorktreeTree string `json:"worktree_tree"`
}

const baselineRecordVersion = 1

// Changeset is the current task-owned work relative to a Baseline. Paths is an
// explicit, sorted scope and Diff is the exact patch represented by ID.
type Changeset struct {
	ID         string
	BaselineID string
	BaseCommit string
	Tree       string
	Paths      []string
	Diff       string

	baseline *Baseline
}

// CaptureBaseline records HEAD, staged state, unstaged state, and untracked
// files without changing the real index or worktree.
func (r *Repo) CaptureBaseline() (*Baseline, error) {
	head, err := r.RevParse("HEAD")
	if err != nil {
		return nil, fmt.Errorf("capture baseline HEAD: %w", err)
	}
	indexTree, err := r.writeIndexTree()
	if err != nil {
		return nil, fmt.Errorf("capture baseline index: %w", err)
	}
	worktreeTree, err := r.writeWorktreeTree(head, nil)
	if err != nil {
		return nil, fmt.Errorf("capture baseline worktree: %w", err)
	}
	return r.baseline(head, indexTree, worktreeTree)
}

func (r *Repo) baseline(head, indexTree, worktreeTree string) (*Baseline, error) {
	dirty, err := r.pathUnion(head, indexTree, worktreeTree)
	if err != nil {
		return nil, fmt.Errorf("capture baseline paths: %w", err)
	}
	return &Baseline{
		ID:           snapshotID(head, indexTree, worktreeTree),
		head:         head,
		indexTree:    indexTree,
		worktreeTree: worktreeTree,
		dirtyPaths:   dirty,
	}, nil
}

// PersistBaseline records a session's original baseline outside the worktree and
// pins every referenced object under refs/ycc so git gc cannot prune a stopped
// session's snapshots before it is reopened.
func (r *Repo) PersistBaseline(sessionID string, b *Baseline) error {
	if b == nil {
		return fmt.Errorf("persist baseline: baseline is required")
	}
	path, err := r.sessionBaselinePath(sessionID)
	if err != nil {
		return err
	}
	if err := r.retainBaseline(b); err != nil {
		return fmt.Errorf("retain baseline %s: %w", b.ID, err)
	}
	record := baselineRecord{
		Version: baselineRecordVersion, ID: b.ID, Head: b.head,
		IndexTree: b.indexTree, WorktreeTree: b.worktreeTree,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode baseline: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create baseline directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".baseline-*")
	if err != nil {
		return fmt.Errorf("create baseline record: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure baseline record: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write baseline record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write baseline record: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install baseline record: %w", err)
	}
	return nil
}

// LoadBaseline restores the immutable baseline captured for sessionID. It never
// substitutes current repository state when the durable record is absent or
// invalid: callers must refuse scoped review and commit in that case.
func (r *Repo) LoadBaseline(sessionID string) (*Baseline, error) {
	path, err := r.sessionBaselinePath(sessionID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read persisted baseline: %w", err)
	}
	var record baselineRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("decode persisted baseline: %w", err)
	}
	if record.Version != baselineRecordVersion {
		return nil, fmt.Errorf("unsupported persisted baseline version %d", record.Version)
	}
	if err := r.requireObject(record.Head, "commit"); err != nil {
		return nil, fmt.Errorf("validate baseline HEAD: %w", err)
	}
	if err := r.requireObject(record.IndexTree, "tree"); err != nil {
		return nil, fmt.Errorf("validate baseline index: %w", err)
	}
	if err := r.requireObject(record.WorktreeTree, "tree"); err != nil {
		return nil, fmt.Errorf("validate baseline worktree: %w", err)
	}
	b, err := r.baseline(record.Head, record.IndexTree, record.WorktreeTree)
	if err != nil {
		return nil, err
	}
	if record.ID != b.ID {
		return nil, fmt.Errorf("persisted baseline identity mismatch: record %q, computed %q", record.ID, b.ID)
	}
	if err := r.retainBaseline(b); err != nil {
		return nil, fmt.Errorf("retain restored baseline %s: %w", b.ID, err)
	}
	return b, nil
}

func (r *Repo) sessionBaselinePath(sessionID string) (string, error) {
	if sessionID == "" || strings.ContainsAny(sessionID, "/\\") || sessionID == "." || sessionID == ".." {
		return "", fmt.Errorf("invalid session id %q for persisted baseline", sessionID)
	}
	out, err := r.run("rev-parse", "--git-path", "ycc/session-baselines/"+sessionID+".json")
	if err != nil {
		return "", fmt.Errorf("locate baseline record: %w", err)
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Dir, path)
	}
	return filepath.Clean(path), nil
}

func (r *Repo) retainBaseline(b *Baseline) error {
	prefix := "refs/ycc/baselines/" + b.ID + "/"
	for name, object := range map[string]string{
		"head": b.head, "index": b.indexTree, "worktree": b.worktreeTree,
	} {
		if _, err := r.run("update-ref", prefix+name, object); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) requireObject(object, wantType string) error {
	if len(object) != 40 && len(object) != 64 {
		return fmt.Errorf("invalid object id %q", object)
	}
	if _, err := hex.DecodeString(object); err != nil {
		return fmt.Errorf("invalid object id %q", object)
	}
	got, err := r.run("cat-file", "-t", object)
	if err != nil {
		return err
	}
	if got = strings.TrimSpace(got); got != wantType {
		return fmt.Errorf("object %s is %s, want %s", object, got, wantType)
	}
	return nil
}

// Changes returns the exact task-owned snapshot relative to baseline. Any path
// that was dirty at baseline is excluded while unchanged; if either its index
// or worktree state changed, ownership is ambiguous and Changes refuses it.
func (r *Repo) Changes(b *Baseline) (*Changeset, error) {
	if b == nil {
		return nil, fmt.Errorf("changeset baseline is required; capture it before task mutation")
	}
	head, err := r.RevParse("HEAD")
	if err != nil {
		return nil, fmt.Errorf("inspect changeset HEAD: %w", err)
	}
	if head != b.head {
		return nil, fmt.Errorf("changeset baseline %s is stale: HEAD moved from %s to %s; start a new task/session baseline before committing", b.ID, shortSHA(b.head), shortSHA(head))
	}
	indexTree, err := r.writeIndexTree()
	if err != nil {
		return nil, fmt.Errorf("inspect changeset index: %w", err)
	}
	worktreeTree, err := r.writeWorktreeTree(head, nil)
	if err != nil {
		return nil, fmt.Errorf("inspect changeset worktree: %w", err)
	}

	indexChanged, err := r.changedPaths(b.indexTree, indexTree)
	if err != nil {
		return nil, err
	}
	worktreeChanged, err := r.changedPaths(b.worktreeTree, worktreeTree)
	if err != nil {
		return nil, err
	}
	var overlaps []string
	for path := range b.dirtyPaths {
		if _, ok := indexChanged[path]; ok {
			overlaps = append(overlaps, path)
			continue
		}
		if _, ok := worktreeChanged[path]; ok {
			overlaps = append(overlaps, path)
		}
	}
	if len(overlaps) > 0 {
		sort.Strings(overlaps)
		return nil, fmt.Errorf("task changes overlap paths that were already dirty at baseline %s: %s; restore those paths to their captured staged/worktree state, move the task to a clean worktree, or start a new baseline after resolving ownership", b.ID, strings.Join(overlaps, ", "))
	}

	currentPaths, err := r.changedPaths(head, worktreeTree)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(currentPaths))
	for path := range currentPaths {
		if _, preexisting := b.dirtyPaths[path]; !preexisting {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	tree, err := r.writeWorktreeTree(head, paths)
	if err != nil {
		return nil, fmt.Errorf("build scoped changeset: %w", err)
	}
	diff, err := r.diffTrees(head, tree)
	if err != nil {
		return nil, fmt.Errorf("render scoped changeset: %w", err)
	}
	id := snapshotID(b.ID, tree, strings.Join(paths, "\x00"))
	if _, err := r.run("update-ref", "refs/ycc/changesets/"+id, tree); err != nil {
		return nil, fmt.Errorf("retain changeset snapshot %s: %w", id, err)
	}
	return &Changeset{ID: id, BaselineID: b.ID, BaseCommit: head, Tree: tree, Paths: paths, Diff: diff, baseline: b}, nil
}

// Commit commits exactly the immutable scoped snapshot. It refuses if the
// snapshot is stale, runs the normal commit validation hooks against an isolated
// index, and refuses any hook that changes the reviewed tree. After success,
// only selected paths are advanced in the real index; unrelated staged/unstaged
// state is preserved.
func (r *Repo) Commit(c *Changeset, message string) (string, error) {
	if c == nil || c.baseline == nil {
		return "", fmt.Errorf("explicit changeset is required; whole-tree commits are unsafe")
	}
	current, err := r.Changes(c.baseline)
	if err != nil {
		return "", err
	}
	if current.ID != c.ID {
		return "", fmt.Errorf("changeset %s is stale (current snapshot is %s); inspect/review the current scoped diff before committing", c.ID, current.ID)
	}
	// Use the freshly validated representation below; exported evidence fields on
	// the caller's value are descriptive and must not influence git mutation.
	c = current
	if strings.TrimSpace(c.Diff) == "" {
		return "", fmt.Errorf("nothing to commit in changeset %s", c.ID)
	}

	indexPath, err := r.indexPath()
	if err != nil {
		return "", err
	}
	indexDir := filepath.Dir(indexPath)
	postIndex, err := os.CreateTemp(indexDir, "ycc-post-index-*")
	if err != nil {
		return "", fmt.Errorf("prepare scoped index: %w", err)
	}
	postPath := postIndex.Name()
	if err := postIndex.Close(); err != nil {
		os.Remove(postPath)
		return "", fmt.Errorf("prepare scoped index: %w", err)
	}
	defer os.Remove(postPath)
	if err := copyFile(indexPath, postPath); err != nil {
		return "", fmt.Errorf("preserve index: %w", err)
	}
	resetArgs := []string{"reset", "-q", c.Tree, "--"}
	resetArgs = append(resetArgs, topLiteralPathspecs(c.Paths)...)
	if _, err := r.runEnv([]string{"GIT_INDEX_FILE=" + postPath}, resetArgs...); err != nil {
		return "", fmt.Errorf("prepare post-commit index: %w", err)
	}

	messagePath, err := r.validateCommit(c, message, indexDir)
	if err != nil {
		return "", err
	}
	defer os.Remove(messagePath)
	commitArgs := []string{"commit-tree", c.Tree, "-p", c.BaseCommit}
	sign, err := r.commitSigningEnabled()
	if err != nil {
		return "", fmt.Errorf("read commit signing policy: %w", err)
	}
	if sign {
		commitArgs = append(commitArgs, "-S")
	}
	commitArgs = append(commitArgs, "-F", messagePath)
	commit, err := r.run(commitArgs...)
	if err != nil {
		return "", fmt.Errorf("create exact changeset commit: %w", err)
	}
	commit = strings.TrimSpace(commit)
	// Updating HEAD with the reviewed parent as the expected old value is the
	// compare-and-swap boundary. A concurrent HEAD move leaves both HEAD and the
	// user's index unchanged.
	if _, err := r.run("update-ref", "HEAD", commit, c.BaseCommit); err != nil {
		return "", fmt.Errorf("advance HEAD for changeset %s: %w", c.ID, err)
	}
	if err := os.Rename(postPath, indexPath); err != nil {
		return "", fmt.Errorf("commit succeeded but could not advance selected paths in index: %w", err)
	}
	return shortSHA(commit), nil
}

// validateCommit runs the hooks that can reject or prepare a normal commit. The
// private index exposes only the reviewed tree, so hooks cannot observe or stage
// unrelated user changes. A hook may edit the commit message, but changing the
// index is refused because that content was not part of the reviewed snapshot.
func (r *Repo) validateCommit(c *Changeset, message, tempDir string) (string, error) {
	index, err := os.CreateTemp(tempDir, "ycc-hook-index-*")
	if err != nil {
		return "", fmt.Errorf("prepare commit hooks: %w", err)
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		os.Remove(indexPath)
		return "", fmt.Errorf("prepare commit hooks: %w", err)
	}
	// read-tree expects either a valid index or no file.
	if err := os.Remove(indexPath); err != nil {
		return "", fmt.Errorf("prepare commit hooks: %w", err)
	}
	defer os.Remove(indexPath)
	env := []string{"GIT_INDEX_FILE=" + indexPath, "GIT_EDITOR=:"}
	if _, err := r.runEnv(env, "read-tree", c.Tree); err != nil {
		return "", fmt.Errorf("prepare commit hooks: %w", err)
	}

	messageFile, err := os.CreateTemp(tempDir, "ycc-commit-message-*")
	if err != nil {
		return "", fmt.Errorf("prepare commit message: %w", err)
	}
	messagePath := messageFile.Name()
	if _, err := messageFile.WriteString(message); err != nil {
		messageFile.Close()
		os.Remove(messagePath)
		return "", fmt.Errorf("prepare commit message: %w", err)
	}
	if err := messageFile.Close(); err != nil {
		os.Remove(messagePath)
		return "", fmt.Errorf("prepare commit message: %w", err)
	}

	for _, hook := range []struct {
		name string
		args []string
	}{
		{name: "pre-commit"},
		{name: "prepare-commit-msg", args: []string{messagePath, "message"}},
		{name: "commit-msg", args: []string{messagePath}},
	} {
		if err := r.runHook(env, hook.name, hook.args...); err != nil {
			os.Remove(messagePath)
			return "", err
		}
	}
	got, err := r.runEnv(env, "write-tree")
	if err != nil {
		os.Remove(messagePath)
		return "", fmt.Errorf("verify commit hook index: %w", err)
	}
	if got = strings.TrimSpace(got); got != c.Tree {
		os.Remove(messagePath)
		return "", fmt.Errorf("commit hook changed the reviewed tree from %s to %s; refusing to commit unreviewed hook changes (adjust the hook or review a new changeset)", shortSHA(c.Tree), shortSHA(got))
	}
	return messagePath, nil
}

func (r *Repo) runHook(env []string, name string, args ...string) error {
	out, err := r.run("rev-parse", "--git-path", "hooks/"+name)
	if err != nil {
		return fmt.Errorf("locate %s hook: %w", name, err)
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Dir, path)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if os.IsNotExist(err) || (err == nil && info.Mode().Perm()&0o111 == 0) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s hook: %w", name, err)
	}
	root, err := r.run("rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("locate worktree for %s hook: %w", name, err)
	}
	cmd := exec.Command(path, args...)
	cmd.Dir = strings.TrimSpace(root)
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail != "" {
			return fmt.Errorf("%s hook rejected commit: %v: %s", name, err, detail)
		}
		return fmt.Errorf("%s hook rejected commit: %v", name, err)
	}
	return nil
}

func (r *Repo) commitSigningEnabled() (bool, error) {
	out, err := r.run("config", "--bool", "--default=false", "--get", "commit.gpgSign")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

func (r *Repo) writeIndexTree() (string, error) {
	indexPath, err := r.indexPath()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "ycc-index-copy-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	copyPath := filepath.Join(dir, "index")
	if err := copyFile(indexPath, copyPath); err != nil {
		return "", err
	}
	out, err := r.runEnv([]string{"GIT_INDEX_FILE=" + copyPath}, "write-tree")
	return strings.TrimSpace(out), err
}

// writeWorktreeTree materializes the requested worktree paths over base in a
// temporary index. A nil path list means the whole visible worktree.
func (r *Repo) writeWorktreeTree(base string, paths []string) (string, error) {
	dir, err := os.MkdirTemp("", "ycc-index-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	index := filepath.Join(dir, "index")
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := r.runEnv(env, "read-tree", base); err != nil {
		return "", err
	}
	args := []string{"add", "-A", "--"}
	if paths == nil {
		args = append(args, ".")
	} else if len(paths) > 0 {
		args = append(args, topLiteralPathspecs(paths)...)
	} else {
		out, err := r.runEnv(env, "write-tree")
		return strings.TrimSpace(out), err
	}
	if _, err := r.runEnv(env, args...); err != nil {
		return "", err
	}
	out, err := r.runEnv(env, "write-tree")
	return strings.TrimSpace(out), err
}

func (r *Repo) pathUnion(trees ...string) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	for i := 1; i < len(trees); i++ {
		paths, err := r.changedPaths(trees[0], trees[i])
		if err != nil {
			return nil, err
		}
		for path := range paths {
			out[path] = struct{}{}
		}
	}
	return out, nil
}

func (r *Repo) changedPaths(a, b string) (map[string]struct{}, error) {
	out, err := r.run("diff-tree", "--no-commit-id", "--name-only", "-r", "-z", a, b)
	if err != nil {
		return nil, fmt.Errorf("compare snapshots: %w", err)
	}
	paths := make(map[string]struct{})
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			paths[path] = struct{}{}
		}
	}
	return paths, nil
}

func (r *Repo) diffTrees(a, b string) (string, error) {
	return r.run("diff-tree", "--no-commit-id", "--binary", "--no-color", "-p", a, b)
}

func topLiteralPathspecs(paths []string) []string {
	out := make([]string, len(paths))
	for i, path := range paths {
		out[i] = ":(top,literal)" + path
	}
	return out
}

func (r *Repo) indexPath() (string, error) {
	out, err := r.run("rev-parse", "--git-path", "index")
	if err != nil {
		return "", fmt.Errorf("locate index: %w", err)
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Dir, path)
	}
	return filepath.Clean(path), nil
}

func (r *Repo) runEnv(env []string, args ...string) (string, error) {
	return r.runWithEnv(env, args...)
}

func snapshotID(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		io.WriteString(h, part)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
