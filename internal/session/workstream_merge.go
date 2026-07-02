package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// MergePreview is the read-only result of trial-merging a workstream's branch
// into its project's current base (design §6 step 1). Clean reports whether the
// merge would apply without conflict; Conflicts lists the conflicted paths when
// not clean; Diff is the integrated diff preview (only set when clean).
type MergePreview struct {
	Clean     bool
	Conflicts []string
	Diff      string
}

// MergeOutcome reports the result of MergeWorkstream. Exactly one of Merged,
// NeedsAccept, or a non-empty Conflicts describes the outcome:
//   - Merged: the branch was integrated (Commit holds the merge commit sha) and
//     the worktree + branch were cleaned up.
//   - NeedsAccept: the trial merge is clean but the interaction level gates the
//     integration behind explicit acceptance; Diff holds the integrated diff to
//     review. Nothing was mutated.
//   - Conflicts: the merge conflicted; the base branch is untouched and the
//     worktree is kept so the conflict can be resolved (design §6).
type MergeOutcome struct {
	Merged      bool
	Commit      string
	NeedsAccept bool
	Diff        string
	Conflicts   []string
}

// emitWorkstreamEvent records a workstream lifecycle event on the workstream's
// own session stream (design §8). If the session is live in the manager it uses
// the live emitter (sharing the single sequence authority); otherwise it opens
// the durable log, appends, and closes it. Opening a live session's log a second
// time would fork the sequence, so that case is explicitly avoided.
func (m *Manager) emitWorkstreamEvent(ws workstream.Workstream, t event.Type, data map[string]any) {
	if ws.SessionID != "" {
		m.mu.Lock()
		s, live := m.sessions[ws.SessionID]
		m.mu.Unlock()
		if live {
			s.emitter.EmitAs("system", t, data)
			return
		}
	}
	if ws.SessionID == "" || ws.WorktreePath == "" {
		return
	}
	logPath := filepath.Join(ws.WorktreePath, ".ycc", "sessions", ws.SessionID, "events.jsonl")
	log, err := event.OpenLog(logPath)
	if err != nil {
		return
	}
	log.Record("system", t, data)
	log.Close()
}

// effectiveLevel resolves a workstream session's current interaction level: the
// live session's Level() if it is running, else the reduced projection of its
// durable log. Defaults to "judgement" when nothing establishes a level.
func (m *Manager) effectiveLevel(ws workstream.Workstream) string {
	if ws.SessionID != "" {
		m.mu.Lock()
		s, live := m.sessions[ws.SessionID]
		m.mu.Unlock()
		if live {
			if lvl := s.Level(); lvl != "" {
				return lvl
			}
		}
	}
	if ws.SessionID != "" && ws.WorktreePath != "" {
		logPath := filepath.Join(ws.WorktreePath, ".ycc", "sessions", ws.SessionID, "events.jsonl")
		if events, err := event.ReadLog(logPath); err == nil {
			if lvl := event.Reduce(events).InteractionLevel; lvl != "" {
				return lvl
			}
		}
	}
	return "judgement"
}

// primaryRepo resolves the parent project's primary tree for an active
// workstream and opens it.
func (m *Manager) primaryRepo(ws workstream.Workstream) (*git.Repo, error) {
	primary, ok := m.projects.Resolve(ws.Project)
	if !ok {
		return nil, fmt.Errorf("unknown project %q", ws.Project)
	}
	return git.Open(primary)
}

// WorkstreamSessionStatus reports the live status of a workstream's session as a
// string (running | idle | paused | stopped | error), for the Workstreams panel
// (design §8). It returns the in-memory status when the session is live; when
// the session is not live it returns "stopped" for a session that exists on
// disk, and "" (unknown) when there is no session at all.
func (m *Manager) WorkstreamSessionStatus(ws workstream.Workstream) string {
	if ws.SessionID == "" {
		return ""
	}
	m.mu.Lock()
	s, live := m.sessions[ws.SessionID]
	m.mu.Unlock()
	if live {
		return string(s.Status())
	}
	return string(event.StatusStopped)
}

// commitCount computes how many commits ws.Branch has added since ws.BaseCommit
// using an already-opened repo. Best-effort: returns 0 when the workstream has
// no branch/base or on any git error.
func commitCount(repo *git.Repo, ws workstream.Workstream) int {
	if repo == nil || ws.Branch == "" || ws.BaseCommit == "" {
		return 0
	}
	n, err := repo.CountCommits(ws.BaseCommit, ws.Branch)
	if err != nil {
		return 0
	}
	return n
}

// WorkstreamCommitCount reports how many commits the workstream's branch has
// added since its base commit (design §8). Best-effort: it returns 0 on any git
// error so a transient failure never blocks listing.
func (m *Manager) WorkstreamCommitCount(ws workstream.Workstream) int {
	if ws.Branch == "" || ws.BaseCommit == "" {
		return 0
	}
	repo, err := m.primaryRepo(ws)
	if err != nil {
		return 0
	}
	return commitCount(repo, ws)
}

// WorkstreamCommitCounts computes WorkstreamCommitCount for a batch of
// workstreams while opening each project's primary repo at most once per call.
// The result is keyed by workstream ID. Enrichment is best-effort: a workstream
// whose project cannot be resolved/opened, or whose count fails, maps to 0
// (same as WorkstreamCommitCount). This deduplicates the git.Open subprocess
// cost when a single ListWorkstreams call enriches many workstreams that share
// a project.
func (m *Manager) WorkstreamCommitCounts(wss []workstream.Workstream) map[string]int {
	counts := make(map[string]int, len(wss))
	// Cache resolved/opened repos per project name. A nil entry records a
	// project that failed to resolve/open so it is not retried per workstream.
	repos := make(map[string]*git.Repo)
	for _, ws := range wss {
		if ws.Branch == "" || ws.BaseCommit == "" {
			counts[ws.ID] = 0
			continue
		}
		repo, cached := repos[ws.Project]
		if !cached {
			if primary, ok := m.projects.Resolve(ws.Project); ok {
				repo, _ = git.Open(primary)
			}
			repos[ws.Project] = repo
		}
		counts[ws.ID] = commitCount(repo, ws)
	}
	return counts
}

// PreviewWorkstreamMerge trial-merges a workstream's branch into its project's
// current base without mutating anything (design §6 step 1). On a clean trial it
// also computes the integrated diff. It emits no events and changes no state.
func (m *Manager) PreviewWorkstreamMerge(id string) (MergePreview, error) {
	ws, ok := m.workstreams.Get(id)
	if !ok {
		return MergePreview{}, fmt.Errorf("unknown workstream %q", id)
	}
	if ws.Status != workstream.StatusActive {
		return MergePreview{}, fmt.Errorf("workstream %q is not active (status %s)", id, ws.Status)
	}
	repo, err := m.primaryRepo(ws)
	if err != nil {
		return MergePreview{}, err
	}
	trial, err := repo.TrialMerge(ws.Branch)
	if err != nil {
		return MergePreview{}, err
	}
	if !trial.Clean {
		return MergePreview{Clean: false, Conflicts: trial.Conflicts}, nil
	}
	diff, err := repo.DiffMergeBase(ws.Branch)
	if err != nil {
		return MergePreview{}, err
	}
	return MergePreview{Clean: true, Diff: diff}, nil
}

// MergeWorkstream integrates a completed workstream's branch back to its
// project's base with an explicit, conflict-aware, review-gated flow (design
// §6). The whole operation is serialized across workstreams so each merge sees
// the previous one's changes (sequential reconciliation).
//
// The outcome depends on the trial merge and the interaction level:
//   - conflict → a workstream_conflict event listing the paths; base untouched,
//     worktree + active status kept so the conflict can be resolved.
//   - clean, autonomous level (or accept=true) → the branch is merged --no-ff,
//     a workstream_merged event recorded, the session stopped, and the
//     worktree + branch cleaned up; registry status set to merged. The session
//     log is preserved into the primary workspace before cleanup so its
//     transcript stays viewable afterwards.
//   - clean, interactive/judgement level and accept=false → NeedsAccept with the
//     integrated diff; nothing is mutated and no event is recorded.
func (m *Manager) MergeWorkstream(id string, accept bool) (MergeOutcome, error) {
	m.mergeMu.Lock()
	defer m.mergeMu.Unlock()

	ws, ok := m.workstreams.Get(id)
	if !ok {
		return MergeOutcome{}, fmt.Errorf("unknown workstream %q", id)
	}
	if ws.Status != workstream.StatusActive {
		return MergeOutcome{}, fmt.Errorf("workstream %q is not active (status %s)", id, ws.Status)
	}
	repo, err := m.primaryRepo(ws)
	if err != nil {
		return MergeOutcome{}, err
	}

	// Step 1: trial-merge against the current base HEAD to detect conflicts
	// without touching the base branch.
	trial, err := repo.TrialMerge(ws.Branch)
	if err != nil {
		return MergeOutcome{}, err
	}
	if !trial.Clean {
		return m.surfaceConflict(ws, trial.Conflicts), nil
	}

	// Step 2: review gate. Autonomous integrates clean workstreams silently;
	// interactive/judgement surface the integrated diff and wait for acceptance.
	if m.effectiveLevel(ws) != "autonomous" && !accept {
		diff, derr := repo.DiffMergeBase(ws.Branch)
		if derr != nil {
			return MergeOutcome{}, derr
		}
		return MergeOutcome{NeedsAccept: true, Diff: diff}, nil
	}

	// Step 3: integrate for real. A conflict here (should be impossible under
	// mergeMu, but a live session could have pushed commits between trial and
	// merge) is handled exactly like the trial conflict — Merge already aborted
	// so the base is restored.
	res, err := repo.Merge(ws.Branch, git.MergeNoFF)
	if err != nil {
		return MergeOutcome{}, err
	}
	if !res.Clean {
		return m.surfaceConflict(ws, res.Conflicts), nil
	}

	// Step 4: record the merge on the session stream while its log still exists,
	// then stop the session and clean up the worktree + branch (design §5 step 4).
	m.emitWorkstreamEvent(ws, event.WorkstreamMerged, map[string]any{
		"workstream": ws.ID,
		"branch":     ws.Branch,
		"commit":     res.Commit,
	})
	if ws.SessionID != "" {
		m.Stop(ws.SessionID)
	}
	m.preserveWorkstreamSession(ws)
	m.cleanupWorktree(repo, ws)
	if err := m.workstreams.SetStatus(ws.ID, workstream.StatusMerged); err != nil {
		return MergeOutcome{}, err
	}
	return MergeOutcome{Merged: true, Commit: res.Commit}, nil
}

// surfaceConflict records a workstream_conflict event and returns the conflict
// outcome. The base branch is left untouched (Merge/TrialMerge already restored
// it) and the worktree + active registry status are preserved so the conflict
// can be resolved in place or handed off (design §6).
func (m *Manager) surfaceConflict(ws workstream.Workstream, conflicts []string) MergeOutcome {
	m.emitWorkstreamEvent(ws, event.WorkstreamConflict, map[string]any{
		"workstream": ws.ID,
		"branch":     ws.Branch,
		"conflicts":  conflicts,
	})
	return MergeOutcome{Conflicts: conflicts}
}

// preserveWorkstreamSession copies a workstream's durable session log out of its
// worktree into the project's primary workspace so the transcript remains
// viewable (panel drill-in / session browser) after the worktree is removed at
// merge/discard time. Session logs are resolved against the primary workspace at
// <primary>/.ycc/sessions/<id>/events.jsonl, but a workstream's live log lives at
// <worktree>/.ycc/sessions/<id>/events.jsonl, which cleanup destroys.
//
// It is entirely best-effort: any error is swallowed so preservation never
// blocks the lifecycle transition, matching the best-effort cleanup philosophy.
// An existing destination is left untouched (session ids are unique, so a
// collision means the log was already preserved).
func (m *Manager) preserveWorkstreamSession(ws workstream.Workstream) {
	if ws.SessionID == "" || ws.WorktreePath == "" {
		return
	}
	primary, ok := m.projects.Resolve(ws.Project)
	if !ok {
		return
	}
	src := filepath.Join(ws.WorktreePath, ".ycc", "sessions", ws.SessionID)
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return
	}
	dst := filepath.Join(primary, ".ycc", "sessions", ws.SessionID)
	if _, err := os.Stat(dst); err == nil {
		return // already preserved
	}
	copyDir(src, dst)
}

// copyDir recursively copies the directory at src to dst, best-effort. It does
// not overwrite files that already exist at the destination.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		sp := filepath.Join(src, e.Name())
		dp := filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyDir(sp, dp)
			continue
		}
		copyFile(sp, dp)
	}
	return nil
}

// copyFile copies a single file, best-effort, without overwriting an existing
// destination.
func copyFile(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// cleanupWorktree tears down a workstream's worktree + branch after a successful
// merge or a discard (design §5 step 4). Every git step is best-effort: a failure
// to remove a tree/branch must not block the lifecycle transition.
func (m *Manager) cleanupWorktree(repo *git.Repo, ws workstream.Workstream) {
	if ws.WorktreePath != "" {
		repo.RemoveWorktree(ws.WorktreePath)
	}
	if ws.Branch != "" {
		// Prefer a safe delete; fall back to force when the branch isn't reported
		// merged (e.g. a --no-ff merge commit, or a discard).
		if err := repo.DeleteBranch(ws.Branch, false); err != nil {
			repo.DeleteBranch(ws.Branch, true)
		}
	}
	repo.PruneWorktrees()
}

// DiscardWorkstream abandons a workstream without merging: it records a
// workstream_discarded event, stops the session, preserves the session log into
// the primary workspace (so its transcript stays viewable), cleans up the
// worktree + branch, and marks the registry entry discarded (design §6, §5 step
// 4). It is allowed for active or stale workstreams; git cleanup is best-effort
// so a stale entry whose tree is already gone still transitions cleanly.
func (m *Manager) DiscardWorkstream(id string) error {
	ws, ok := m.workstreams.Get(id)
	if !ok {
		return fmt.Errorf("unknown workstream %q", id)
	}
	if ws.Status != workstream.StatusActive && ws.Status != workstream.StatusStale {
		return fmt.Errorf("workstream %q is not active (status %s)", id, ws.Status)
	}
	m.emitWorkstreamEvent(ws, event.WorkstreamDiscarded, map[string]any{
		"workstream": ws.ID,
		"branch":     ws.Branch,
	})
	if ws.SessionID != "" {
		m.Stop(ws.SessionID)
	}
	m.preserveWorkstreamSession(ws)
	// Cleanup is best-effort; a stale entry's tree may already be gone.
	if repo, err := m.primaryRepo(ws); err == nil {
		if ws.WorktreePath != "" {
			repo.RemoveWorktree(ws.WorktreePath)
		}
		if ws.Branch != "" {
			repo.DeleteBranch(ws.Branch, true)
		}
		repo.PruneWorktrees()
	}
	return m.workstreams.SetStatus(ws.ID, workstream.StatusDiscarded)
}
