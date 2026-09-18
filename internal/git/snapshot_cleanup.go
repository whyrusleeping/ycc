package git

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const changesetRecordVersion = 1

type changesetRecord struct {
	Version    int    `json:"version"`
	ID         string `json:"id"`
	BaselineID string `json:"baseline_id"`
	Tree       string `json:"tree"`
}

type finalizationEvidence struct {
	Version        int             `json:"version"`
	BaselineID     string          `json:"baseline_id"`
	FinalizationID string          `json:"finalization_id"`
	Commit         *CommitRecovery `json:"commit,omitempty"`
}

// SnapshotCleanup reports retained snapshot evidence and the expired artifacts
// which were (or, for a dry run, would be) removed.
type SnapshotCleanup struct {
	BaselineRecords  int
	ChangesetRecords int
	RemovedRecords   int
	RemovedRefs      int
	AmbiguousRefs    int
}

type snapshotBaselineEvidence struct {
	path      string
	sessionID string
	workspace string
	record    baselineRecord
	legacy    bool
	live      bool
}

type snapshotChangesetEvidence struct {
	path   string
	record changesetRecord
}

type snapshotCommitEvidence struct {
	path     string
	identity commitIdentity
}

// PruneSnapshots removes only refs and sidecars which are no longer reachable
// from retained workspace session directories or task-finalization journals in
// any worktree sharing this repository. A dry run performs the same validation
// and reports what would be removed. Unreadable or malformed evidence aborts
// before any mutation; legacy metadata without workspace ownership is retained.
func (r *Repo) PruneSnapshots(dryRun bool) (SnapshotCleanup, error) {
	var result SnapshotCleanup
	if !dryRun && !snapshotFileLockSupported() {
		return result, fmt.Errorf("pruning retained snapshots is unsupported on this platform: advisory file locking is unavailable")
	}
	common, err := r.commonGitDir()
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(filepath.Join(common, "ycc"), 0o700); err != nil {
		return result, fmt.Errorf("create snapshot lock directory: %w", err)
	}
	unlock, err := acquireSnapshotFileLock(filepath.Join(common, "ycc", "snapshots.lock"))
	if err != nil {
		return result, fmt.Errorf("lock retained snapshots: %w", err)
	}
	defer unlock()

	worktrees, err := r.snapshotWorktrees()
	if err != nil {
		return result, err
	}
	retainedBaselines := map[string]bool{}
	activeBaselines := map[string]bool{}
	retainedChangesets := map[string]bool{}
	workspaceRoots := map[string]bool{}
	var baselines []snapshotBaselineEvidence

	// The invocation directory may itself be a nested ycc workspace. Git only
	// lists worktree roots, so include it as well as roots persisted by writers.
	invocationRoot, err := filepath.Abs(r.Dir)
	if err != nil {
		return result, fmt.Errorf("locate cleanup workspace: %w", err)
	}
	workspaceRoots[filepath.Clean(invocationRoot)] = true
	for _, wt := range worktrees {
		workspaceRoots[wt.path] = true
		dir := filepath.Join(wt.gitDir, "ycc", "session-baselines")
		entries, err := readOptionalDir(dir)
		if err != nil {
			return result, fmt.Errorf("read baseline records in %s: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			file := filepath.Join(dir, entry.Name())
			var record baselineRecord
			if err := readJSONFile(file, &record); err != nil {
				return result, fmt.Errorf("read baseline evidence %s: %w", file, err)
			}
			if (record.Version != baselineRecordLegacyVersion && record.Version != baselineRecordVersion) || record.ID != snapshotID(record.Head, record.IndexTree, record.WorktreeTree) {
				return result, fmt.Errorf("invalid baseline evidence %s", file)
			}
			sessionID := strings.TrimSuffix(entry.Name(), ".json")
			if sessionID == "" || strings.ContainsAny(sessionID, "/\\") || sessionID == "." || sessionID == ".." {
				return result, fmt.Errorf("invalid baseline evidence %s: invalid session id", file)
			}
			evidence := snapshotBaselineEvidence{path: file, sessionID: sessionID, record: record}
			if record.Version == baselineRecordLegacyVersion {
				// Version 1 did not identify the nested workspace which owned the
				// session. Keep it and every changeset based on it rather than guess.
				evidence.legacy = true
				retainedBaselines[record.ID] = true
				activeBaselines[record.ID] = true
			} else {
				owner, err := workspaceFromRecord(wt.path, record.Workspace)
				if err != nil {
					return result, fmt.Errorf("invalid baseline evidence %s: %w", file, err)
				}
				evidence.workspace = owner
				workspaceRoots[owner] = true
			}
			baselines = append(baselines, evidence)
			result.BaselineRecords++
		}
	}

	workspaceSessions := make(map[string]map[string]bool, len(workspaceRoots))
	roots := make([]string, 0, len(workspaceRoots))
	for root := range workspaceRoots {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	for _, root := range roots {
		sessions, err := scanSessionEvidence(root, retainedBaselines, retainedChangesets)
		if err != nil {
			return result, err
		}
		workspaceSessions[root] = sessions
	}
	for i := range baselines {
		evidence := &baselines[i]
		if evidence.legacy {
			continue
		}
		if workspaceSessions[evidence.workspace][evidence.sessionID] {
			evidence.live = true
			activeBaselines[evidence.record.ID] = true
			retainedBaselines[evidence.record.ID] = true
		}
	}

	for _, wt := range worktrees {
		finalizations := filepath.Join(wt.gitDir, "ycc", "task-finalizations")
		entries, err := readOptionalDir(finalizations)
		if err != nil {
			return result, fmt.Errorf("read finalization evidence in %s: %w", finalizations, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			file := filepath.Join(finalizations, entry.Name())
			var evidence finalizationEvidence
			if err := readJSONFile(file, &evidence); err != nil {
				return result, fmt.Errorf("read finalization evidence %s: %w", file, err)
			}
			if evidence.Version != 1 || !validSnapshotID(evidence.BaselineID) || !validSnapshotID(evidence.FinalizationID) {
				return result, fmt.Errorf("invalid finalization evidence %s", file)
			}
			retainedBaselines[evidence.BaselineID] = true
			retainedChangesets[evidence.FinalizationID] = true
			if evidence.Commit != nil {
				if err := validateRecovery(evidence.Commit); err != nil {
					return result, fmt.Errorf("invalid finalization evidence %s: %w", file, err)
				}
				retainedChangesets[evidence.Commit.ChangesetID] = true
			}
		}
	}

	recordDir := filepath.Join(common, "ycc", "changeset-records")
	entries, err := readOptionalDir(recordDir)
	if err != nil {
		return result, fmt.Errorf("read changeset records: %w", err)
	}
	changesetRecords := map[string]snapshotChangesetEvidence{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		file := filepath.Join(recordDir, entry.Name())
		var record changesetRecord
		if err := readJSONFile(file, &record); err != nil {
			return result, fmt.Errorf("read changeset evidence %s: %w", file, err)
		}
		if record.Version != changesetRecordVersion || !validSnapshotID(record.ID) || !validSnapshotID(record.BaselineID) || entry.Name() != record.ID+".json" {
			return result, fmt.Errorf("invalid changeset evidence %s", file)
		}
		changesetRecords[record.ID] = snapshotChangesetEvidence{path: file, record: record}
		result.ChangesetRecords++
		if activeBaselines[record.BaselineID] {
			retainedChangesets[record.ID] = true
		}
	}
	// Retained review commands name both the reviewed changeset and its baseline.
	// Follow that relationship even if the originating session record expired.
	for id := range retainedChangesets {
		if evidence, ok := changesetRecords[id]; ok {
			retainedBaselines[evidence.record.BaselineID] = true
		}
	}

	baselineRefs, err := r.refsBySnapshot("refs/ycc/baselines/")
	if err != nil {
		return result, err
	}
	changesetRefs, err := r.refsBySnapshot("refs/ycc/changesets/")
	if err != nil {
		return result, err
	}
	commitRefs, err := r.refsBySnapshot("refs/ycc/commits/")
	if err != nil {
		return result, err
	}

	for _, evidence := range baselines {
		refs := baselineRefs[evidence.record.ID]
		expected := map[string]struct {
			object string
			typeOf string
		}{
			"head":     {evidence.record.Head, "commit"},
			"index":    {evidence.record.IndexTree, "tree"},
			"worktree": {evidence.record.WorktreeTree, "tree"},
		}
		for name, want := range expected {
			ref := "refs/ycc/baselines/" + evidence.record.ID + "/" + name
			object, present, err := r.snapshotRefObject(ref, refs)
			if err != nil {
				return result, fmt.Errorf("validate baseline evidence %s: %w", evidence.path, err)
			}
			if present && object != want.object {
				return result, fmt.Errorf("invalid baseline evidence %s: retained %s ref is mismatched", evidence.path, name)
			}
			if retainedBaselines[evidence.record.ID] && !present {
				return result, fmt.Errorf("invalid baseline evidence %s: retained %s ref is missing", evidence.path, name)
			}
			if present || retainedBaselines[evidence.record.ID] {
				if err := r.requireObject(want.object, want.typeOf); err != nil {
					return result, fmt.Errorf("invalid baseline evidence %s: %w", evidence.path, err)
				}
			}
		}
	}

	liveBaselineOwners := map[string]bool{}
	for _, evidence := range baselines {
		if evidence.live {
			liveBaselineOwners[evidence.workspace+"\x00"+evidence.record.ID] = true
		}
	}
	var expiredBaselineRecords []string
	for _, evidence := range baselines {
		if evidence.legacy || evidence.live {
			continue
		}
		// Keep an ownership record while non-session evidence is the only way
		// cleanup can rediscover a nested workspace on a later invocation. A
		// duplicate expired record is unnecessary when a live record has the
		// same owner and baseline.
		if retainedBaselines[evidence.record.ID] && !liveBaselineOwners[evidence.workspace+"\x00"+evidence.record.ID] {
			continue
		}
		expiredBaselineRecords = append(expiredBaselineRecords, evidence.path)
	}
	var expiredChangesetRecords []string
	for id, evidence := range changesetRecords {
		ref := "refs/ycc/changesets/" + id
		object, present, err := r.snapshotRefObject(ref, changesetRefs[id])
		if err != nil {
			return result, fmt.Errorf("validate changeset evidence %s: %w", evidence.path, err)
		}
		if present && object != evidence.record.Tree {
			return result, fmt.Errorf("invalid changeset evidence %s: retained ref is mismatched", evidence.path)
		}
		if !present && retainedChangesets[id] {
			return result, fmt.Errorf("invalid changeset evidence %s: retained ref is missing", evidence.path)
		}
		if present || retainedChangesets[id] {
			if err := r.requireObject(evidence.record.Tree, "tree"); err != nil {
				return result, fmt.Errorf("invalid changeset evidence %s: %w", evidence.path, err)
			}
		}
		if !retainedChangesets[id] {
			expiredChangesetRecords = append(expiredChangesetRecords, evidence.path)
		}
	}

	var commitRecords []snapshotCommitEvidence
	for _, wt := range worktrees {
		dir := filepath.Join(wt.gitDir, "ycc", "commit-recovery")
		entries, err := readOptionalDir(dir)
		if err != nil {
			return result, fmt.Errorf("read commit recovery evidence in %s: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			file := filepath.Join(dir, entry.Name())
			var identity commitIdentity
			if err := readJSONFile(file, &identity); err != nil {
				return result, fmt.Errorf("read commit recovery evidence %s: %w", file, err)
			}
			id := strings.TrimSuffix(entry.Name(), ".json")
			if identity.Version != commitIdentityVersion || identity.Recovery.ChangesetID != id {
				return result, fmt.Errorf("invalid commit recovery evidence %s", file)
			}
			if err := validateRecovery(&identity.Recovery); err != nil {
				return result, fmt.Errorf("invalid commit recovery evidence %s: %w", file, err)
			}
			ref := commitRecoveryRef(&identity.Recovery, identity.Message)
			object, present, err := r.snapshotRefObject(ref, commitRefs[id])
			if err != nil {
				return result, fmt.Errorf("validate commit recovery evidence %s: %w", file, err)
			}
			if present && object != identity.Commit {
				return result, fmt.Errorf("invalid commit recovery evidence %s: retained ref is mismatched", file)
			}
			// An absent ref is tolerated only for expired evidence, allowing a
			// ref-first cleanup interrupted before deleting its sidecar to resume.
			if retainedChangesets[id] && !present {
				return result, fmt.Errorf("invalid commit recovery evidence %s: retained ref is missing", file)
			}
			if present || retainedChangesets[id] {
				if err := r.validateCommitIdentity(&identity, &identity.Recovery, identity.Message); err != nil {
					return result, fmt.Errorf("invalid commit recovery evidence %s: %w", file, err)
				}
			}
			commitRecords = append(commitRecords, snapshotCommitEvidence{path: file, identity: identity})
		}
	}

	var deleteRefs []string
	for id, refs := range baselineRefs {
		if !retainedBaselines[id] {
			deleteRefs = append(deleteRefs, refs...)
		}
	}
	for id, refs := range changesetRefs {
		if retainedChangesets[id] {
			continue
		}
		if _, known := changesetRecords[id]; !known {
			result.AmbiguousRefs += len(refs)
			continue
		}
		deleteRefs = append(deleteRefs, refs...)
	}
	for id, refs := range commitRefs {
		if !retainedChangesets[id] {
			deleteRefs = append(deleteRefs, refs...)
		}
	}
	sort.Strings(deleteRefs)
	deleteRecords := append(expiredBaselineRecords, expiredChangesetRecords...)
	for _, evidence := range commitRecords {
		if !retainedChangesets[evidence.identity.Recovery.ChangesetID] {
			deleteRecords = append(deleteRecords, evidence.path)
		}
	}
	result.RemovedRefs = len(deleteRefs)
	result.RemovedRecords = len(deleteRecords)
	if dryRun {
		return result, nil
	}
	// Refs are removed first. Validation above explicitly recognizes an expired
	// sidecar whose ref is already absent, so interruption is safely resumable.
	for _, ref := range deleteRefs {
		if _, err := r.run("update-ref", "-d", ref); err != nil {
			return SnapshotCleanup{}, fmt.Errorf("delete expired snapshot ref %s: %w", ref, err)
		}
	}
	for _, file := range deleteRecords {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return SnapshotCleanup{}, fmt.Errorf("delete expired snapshot record %s: %w", file, err)
		}
	}
	return result, nil
}

type snapshotWorktree struct {
	path   string
	gitDir string
}

func (r *Repo) snapshotWorktrees() ([]snapshotWorktree, error) {
	out, err := r.run("worktree", "list", "--porcelain", "-z")
	separator := "\x00"
	quoted := false
	if err != nil {
		// Git before 2.42 lacks -z. Its porcelain format C-quotes unusual
		// worktree paths, which strconv.Unquote handles without splitting the
		// quoted newline escapes into records.
		out, err = r.run("worktree", "list", "--porcelain")
		separator = "\n"
		quoted = true
	}
	if err != nil {
		return nil, fmt.Errorf("list git worktrees: %w", err)
	}
	var worktrees []snapshotWorktree
	for _, field := range strings.Split(out, separator) {
		if !strings.HasPrefix(field, "worktree ") {
			continue
		}
		worktreePath := strings.TrimPrefix(field, "worktree ")
		if quoted && strings.HasPrefix(worktreePath, "\"") {
			worktreePath, err = strconv.Unquote(worktreePath)
			if err != nil {
				return nil, fmt.Errorf("decode git worktree path: %w", err)
			}
		}
		if worktreePath == "" {
			return nil, fmt.Errorf("git reported an empty worktree path")
		}
		wt := &Repo{Dir: worktreePath}
		gitDir, err := wt.run("rev-parse", "--absolute-git-dir")
		if err != nil {
			return nil, fmt.Errorf("inspect git directory for worktree %s: %w", worktreePath, err)
		}
		worktrees = append(worktrees, snapshotWorktree{path: filepath.Clean(worktreePath), gitDir: filepath.Clean(strings.TrimSpace(gitDir))})
	}
	if len(worktrees) == 0 {
		return nil, fmt.Errorf("git reported no worktrees")
	}
	return worktrees, nil
}

func (r *Repo) workspaceRelativePath() (string, error) {
	out, err := r.run("rev-parse", "--show-prefix")
	if err != nil {
		return "", err
	}
	workspace := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(out, "\n"), "\r"), "/")
	if workspace == "" {
		return ".", nil
	}
	workspace = path.Clean(workspace)
	if !validWorkspacePath(workspace) {
		return "", fmt.Errorf("git reported invalid workspace path %q", workspace)
	}
	return workspace, nil
}

func workspaceFromRecord(worktree, workspace string) (string, error) {
	if !validWorkspacePath(workspace) {
		return "", fmt.Errorf("invalid workspace path %q", workspace)
	}
	if workspace == "." {
		return filepath.Clean(worktree), nil
	}
	return filepath.Clean(filepath.Join(worktree, filepath.FromSlash(workspace))), nil
}

func validWorkspacePath(workspace string) bool {
	return workspace == "." || (workspace != "" && path.Clean(workspace) == workspace && workspace != ".." && !strings.HasPrefix(workspace, "../") && !strings.HasPrefix(workspace, "/") && !strings.Contains(workspace, "\\"))
}

func (r *Repo) commonGitDir() (string, error) {
	out, err := r.run("rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("locate common git directory: %w", err)
	}
	common := strings.TrimSpace(out)
	if !filepath.IsAbs(common) {
		common = filepath.Join(r.Dir, common)
	}
	return filepath.Clean(common), nil
}

func (r *Repo) withSnapshotLock(fn func(common string) error) error {
	common, err := r.commonGitDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(common, "ycc"), 0o700); err != nil {
		return err
	}
	unlock, err := acquireSnapshotFileLock(filepath.Join(common, "ycc", "snapshots.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return fn(common)
}

func (r *Repo) saveChangesetRecord(c changesetRecord) error {
	return r.withSnapshotLock(func(common string) error {
		if _, err := r.run("update-ref", "refs/ycc/changesets/"+c.ID, c.Tree); err != nil {
			return err
		}
		data, err := json.Marshal(c)
		if err != nil {
			return err
		}
		file := filepath.Join(common, "ycc", "changeset-records", c.ID+".json")
		return writeAtomicPrivate(file, append(data, '\n'))
	})
}

func scanSessionEvidence(workspace string, baselines, changesets map[string]bool) (map[string]bool, error) {
	dir := filepath.Join(workspace, ".ycc", "sessions")
	entries, err := readOptionalDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read session evidence in %s: %w", dir, err)
	}
	sessions := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessions[entry.Name()] = true
		file := filepath.Join(dir, entry.Name(), "events.jsonl")
		f, err := os.Open(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read session evidence %s: %w", file, err)
		}
		dec := json.NewDecoder(f)
		for {
			var value any
			err := dec.Decode(&value)
			if err == io.EOF {
				break
			}
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("decode session evidence %s: %w", file, err)
			}
			if _, ok := value.(map[string]any); !ok {
				f.Close()
				return nil, fmt.Errorf("decode session evidence %s: event is not a JSON object", file)
			}
			collectEvidenceIDs(value, baselines, changesets)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("read session evidence %s: %w", file, err)
		}
	}
	return sessions, nil
}

func collectEvidenceIDs(value any, baselines, changesets map[string]bool) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if text, ok := child.(string); ok {
				switch key {
				case "baseline_id":
					if baselines != nil && text != "" {
						baselines[text] = true
					}
				case "changeset_id", "finalization_id":
					if changesets != nil && text != "" {
						changesets[text] = true
					}
				}
			}
			collectEvidenceIDs(child, baselines, changesets)
		}
	case []any:
		for _, child := range value {
			collectEvidenceIDs(child, baselines, changesets)
		}
	}
}

func readOptionalDir(file string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return entries, err
}

func validSnapshotID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func readJSONFile(file string, dst any) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func (r *Repo) refsBySnapshot(prefix string) (map[string][]string, error) {
	out, err := r.run("for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return nil, fmt.Errorf("list retained refs under %s: %w", prefix, err)
	}
	refs := map[string][]string{}
	for _, ref := range strings.Split(strings.TrimSpace(out), "\n") {
		if ref == "" {
			continue
		}
		rest := strings.TrimPrefix(ref, prefix)
		id := strings.SplitN(rest, "/", 2)[0]
		if rest == ref || id == "" {
			continue
		}
		refs[id] = append(refs[id], ref)
	}
	return refs, nil
}

func (r *Repo) snapshotRefObject(want string, refs []string) (string, bool, error) {
	for _, ref := range refs {
		if ref != want {
			continue
		}
		out, err := r.run("rev-parse", "--verify", ref)
		if err != nil {
			return "", false, err
		}
		return strings.TrimSpace(out), true, nil
	}
	return "", false, nil
}
