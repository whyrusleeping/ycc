package session

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/jobshim"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// MergePreview is the read-only result of trial-merging a workstream's branch
// into its project's current base. Clean reports whether the
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
//   - NeedsAccept: the trial merge is clean but explicit acceptance is required;
//     Diff holds the integrated diff to
//     review. Nothing was mutated.
//   - Conflicts: the merge conflicted; the base branch is untouched and the
//     worktree is kept so the conflict can be resolved.
type MergeOutcome struct {
	Merged      bool
	Commit      string
	NeedsAccept bool
	Diff        string
	Conflicts   []string
}

// emitWorkstreamEvent records a workstream lifecycle event on the workstream's
// own session stream. If the session is live in the manager it uses
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
	// Persisted registry paths are untrusted. Do not open (and potentially create)
	// an event log through a path outside daemon-owned worktree state.
	if err := workstream.VerifyUnderRoot(m.worktreesRoot, ws.WorktreePath); err != nil {
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

// primaryRepo resolves the parent project's primary tree for an active
// workstream and opens it.
func (m *Manager) primaryRepo(ws workstream.Workstream) (*git.Repo, error) {
	primary, ok := m.projects.Resolve(ws.Project)
	if !ok {
		return nil, fmt.Errorf("unknown project %q", ws.Project)
	}
	return git.OpenExisting(primary)
}

// workstreamBaseBranch resolves and validates the local branch integration must
// advance. Registry entries without BaseBranch use the config/default fallback.
func (m *Manager) workstreamBaseBranch(repo *git.Repo, ws workstream.Workstream) (string, error) {
	base := ws.BaseBranch
	if base == "" {
		base = m.reg.IntegrationBase()
	}
	if base == "" {
		var err error
		base, err = repo.DefaultBranch()
		if err != nil {
			return "", fmt.Errorf("resolve base branch for workstream %q: %w", ws.ID, err)
		}
	}
	name, local, err := repo.LocalBranch(base)
	if err != nil {
		return "", fmt.Errorf("resolve base branch %q for workstream %q: %w", base, ws.ID, err)
	}
	if !local {
		return "", fmt.Errorf("base branch %q for workstream %q is not a local branch", base, ws.ID)
	}
	return name, nil
}

// WorkstreamSessionStatus reports the live status of a workstream's session as a
// string (running | idle | paused | stopped | error), for the Workstreams panel.
// It returns the in-memory status when the session is live; when
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
// added since its base commit. Best-effort: it returns 0 on any git
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
				repo, _ = git.OpenExisting(primary)
			}
			repos[ws.Project] = repo
		}
		counts[ws.ID] = commitCount(repo, ws)
	}
	return counts
}

// PreviewWorkstreamMerge trial-merges a workstream's branch into its project's
// current base without mutating anything. On a clean trial it
// also computes the integrated diff. It emits no events and changes no state.
func (m *Manager) PreviewWorkstreamMerge(id string) (MergePreview, error) {
	ws, ok := m.workstreams.Get(id)
	if !ok {
		return MergePreview{}, fmt.Errorf("unknown workstream %q", id)
	}
	if !ws.Status.InFlight() {
		return MergePreview{}, fmt.Errorf("workstream %q is not in flight (status %s)", id, ws.Status)
	}
	repo, err := m.primaryRepo(ws)
	if err != nil {
		return MergePreview{}, err
	}
	base, err := m.workstreamBaseBranch(repo, ws)
	if err != nil {
		return MergePreview{}, err
	}
	trial, err := repo.TrialMerge(base, ws.Branch)
	if err != nil {
		return MergePreview{}, err
	}
	if !trial.Clean {
		return MergePreview{Clean: false, Conflicts: trial.Conflicts}, nil
	}
	diff, err := repo.DiffMergeBase(base, ws.Branch)
	if err != nil {
		return MergePreview{}, err
	}
	return MergePreview{Clean: true, Diff: diff}, nil
}

// MergeWorkstream integrates a completed workstream's branch back to its
// project's base with an explicit, conflict-aware, review-gated flow. The whole
// operation is serialized across workstreams so each merge sees
// the previous one's changes (sequential reconciliation).
//
// The outcome depends on the trial merge and explicit acceptance:
//   - conflict → a workstream_conflict event listing the paths; base untouched,
//     worktree + active status kept so the conflict can be resolved.
//   - clean and accept=true → the branch is rebased onto the current base, which
//     is advanced by fast-forward only; a workstream_merged event is recorded,
//     the session stopped, and the worktree + branch cleaned up; registry status
//     set to merged. The session log is preserved into the
//     primary workspace before cleanup so its transcript remains viewable.
//   - clean and accept=false → NeedsAccept with the integrated diff; nothing is
//     mutated and no event is recorded.
func (m *Manager) MergeWorkstream(id string, accept bool) (MergeOutcome, error) {
	m.mergeMu.Lock()
	defer m.mergeMu.Unlock()

	ws, ok := m.workstreams.Get(id)
	if !ok {
		return MergeOutcome{}, fmt.Errorf("unknown workstream %q", id)
	}
	if !ws.Status.InFlight() {
		return MergeOutcome{}, fmt.Errorf("workstream %q is not in flight (status %s)", id, ws.Status)
	}
	if why := m.workstreamExecutionUnsettled(ws); why != "" {
		m.integrationNeedsAttentionFrom(ws, "merge blocked: "+why, nil, workstream.StatusActive, workstream.StatusReady, workstream.StatusNeedsAttention)
		return MergeOutcome{}, fmt.Errorf("workstream %q cannot be merged yet: %s", id, why)
	}
	token := m.ownership.NewToken("workstream " + id + " merge")
	worktreeLease, err := m.acquireSection(ws.WorktreePath, token)
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("merge workstream: %w", err)
	}
	defer worktreeLease.Release()
	primary, ok := m.projects.Resolve(ws.Project)
	if !ok {
		return MergeOutcome{}, fmt.Errorf("unknown project %q", ws.Project)
	}
	primaryLease, err := m.acquireSection(primary, token)
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("merge workstream: %w", err)
	}
	defer primaryLease.Release()
	repo, err := m.primaryRepo(ws)
	if err != nil {
		return MergeOutcome{}, err
	}
	base, err := m.workstreamBaseBranch(repo, ws)
	if err != nil {
		return MergeOutcome{}, err
	}

	// Step 1: trial-merge against the current base branch to detect conflicts
	// without touching the base branch.
	trial, err := repo.TrialMerge(base, ws.Branch)
	if err != nil {
		return MergeOutcome{}, err
	}
	if !trial.Clean {
		return m.surfaceConflict(ws, trial.Conflicts), nil
	}

	// Step 2: every clean merge requires explicit acceptance after preview.
	if !accept {
		diff, derr := repo.DiffMergeBase(base, ws.Branch)
		if derr != nil {
			return MergeOutcome{}, derr
		}
		return MergeOutcome{NeedsAccept: true, Diff: diff}, nil
	}

	// Step 3: preflight the base before rebasing so a dirty checked-out base
	// refuses the attempt without rewriting the workstream branch or tree.
	// AdvanceBranch repeats this check after the rebase to close the race window.
	if err := repo.CheckBaseClean(base); err != nil {
		return MergeOutcome{}, err
	}

	// Reconcile in the workstream itself, then mechanically fast-forward the
	// selected base. Persisted worktree paths are untrusted, so never run git there
	// until containment under daemon-owned state has been verified.
	if err := workstream.VerifyUnderRoot(m.worktreesRoot, ws.WorktreePath); err != nil {
		return MergeOutcome{}, fmt.Errorf("workstream %q has invalid worktree path: %w", ws.ID, err)
	}
	rebase, err := repo.RebaseOnto(ws.WorktreePath, base)
	if err != nil {
		return MergeOutcome{}, err
	}
	if !rebase.Clean {
		return m.surfaceConflict(ws, rebase.Conflicts), nil
	}
	commit, err := repo.AdvanceBranch(base, ws.Branch)
	if err != nil {
		return MergeOutcome{}, err
	}

	// Step 4: record the integration on the session stream while its log still
	// exists, then stop the session and clean up the worktree + branch.
	m.emitWorkstreamEvent(ws, event.WorkstreamMerged, map[string]any{
		"workstream":  ws.ID,
		"branch":      ws.Branch,
		"base_branch": base,
		"commit":      commit,
	})
	// Mark terminal before Stop emits session_stopped, otherwise the readiness
	// watcher can race the merge and briefly resurrect an integration state.
	if err := m.workstreams.SetStatus(ws.ID, workstream.StatusMerged); err != nil {
		return MergeOutcome{}, err
	}
	if fresh, ok := m.workstreams.Get(ws.ID); ok {
		ws = fresh
	}
	if unjoined := m.stopWorkstreamSessions(ws); unjoined != nil {
		m.keptAfterMerge(ws, commit, unjoined)
		return MergeOutcome{Merged: true, Commit: commit}, nil
	}
	m.preserveWorkstreamSessions(ws)
	m.cleanupWorktree(repo, ws)
	return MergeOutcome{Merged: true, Commit: commit}, nil
}

// surfaceConflict records a workstream_conflict event and returns the conflict
// outcome. The base branch is left untouched (Merge/TrialMerge already restored
// it) and the worktree + active registry status are preserved so the conflict
// can be resolved in place or handed off.
func (m *Manager) surfaceConflict(ws workstream.Workstream, conflicts []string) MergeOutcome {
	m.emitWorkstreamEvent(ws, event.WorkstreamConflict, map[string]any{
		"workstream": ws.ID,
		"branch":     ws.Branch,
		"conflicts":  conflicts,
	})
	return MergeOutcome{Conflicts: conflicts}
}

// stopWorkstreamSessions ensures neither the implementation session nor the
// integrate recovery session can write while transcripts are copied and the
// linked worktree is removed. Stops are best-effort because a completed recovery
// session is normally already stopped by its runner, except that a stop which
// could not join a background job (its supervision was lost, so it may still
// write in the worktree), or a supervised job left running, is returned: the
// caller must keep the worktree.
func (m *Manager) stopWorkstreamSessions(ws workstream.Workstream) error {
	var unjoined error
	for _, id := range []string{ws.SessionID, ws.IntegrateSessionID} {
		if id != "" {
			if s, ok := m.Get(id); ok {
				// Even a stop during graceful shutdown kills and joins the
				// shells of a workstream being merged or discarded.
				s.noDetach.Store(true)
			}
			var u *jobs.UnjoinedError
			if err := m.Stop(id); errors.As(err, &u) && unjoined == nil {
				unjoined = err
			}
		}
	}
	if unjoined == nil {
		// The sessions are no longer live, so what remains on disk decides: a
		// stop during graceful shutdown detaches supervised shells, which keep
		// running in the worktree.
		if why := m.workstreamExecutionUnsettled(ws); why != "" {
			unjoined = errors.New(why)
		}
	}
	if unjoined != nil {
		log.Printf("workstream %s: worktree %s kept: %v", ws.ID, ws.WorktreePath, unjoined)
	}
	return unjoined
}

// keptAfterMerge records that a merged workstream's worktree was kept because
// a background job may still run in it: the stream needs attention, and
// discarding it once the job's stop is confirmed removes the tree and branch.
// The session logs are not archived meanwhile: the worktree's stay
// authoritative for a reopen.
func (m *Manager) keptAfterMerge(ws workstream.Workstream, commit string, why error) {
	m.integrationNeedsAttentionFrom(ws, fmt.Sprintf("merged at %s, but the worktree was kept: %v; discard the workstream once the job's stop is confirmed", commit, why),
		nil, workstream.StatusMerged)
}

// workstreamExecutionUnsettled reports why a workstream's worktree must not be
// merged away or removed yet: one of its sessions has a background job whose
// supervision was lost (it may still write there) or that is still running
// after being re-attached across a daemon restart, or a session that is not
// live left a supervised job that may still run in it. A live session's other
// running jobs do not count: stopping the session kills and joins them.
func (m *Manager) workstreamExecutionUnsettled(ws workstream.Workstream) string {
	for _, id := range []string{ws.SessionID, ws.IntegrateSessionID} {
		if id == "" {
			continue
		}
		if s, ok := m.Get(id); ok {
			if s.deps != nil && s.deps.Jobs != nil {
				for _, info := range s.deps.Jobs.List("", true) {
					if info.ExecutionUnknown {
						return fmt.Sprintf("session %s: supervision of background job %s was lost and its command may still be running", id, info.ID)
					}
					// A supervised job re-attached across a daemon restart is
					// only followed, so the operation waits for it to finish
					// (or be killed) rather than stopping it after the fact.
					if job, ok := s.deps.Jobs.Get(info.ID); ok && info.Restored && job.ExecutionRunning() {
						return fmt.Sprintf("session %s: background job %s kept running across a daemon restart and has not finished", id, info.ID)
					}
				}
			}
			continue
		}
		if ws.WorktreePath == "" || workstream.VerifyUnderRoot(m.worktreesRoot, ws.WorktreePath) != nil {
			continue
		}
		if why := jobshim.SessionUnsettled(filepath.Join(ws.WorktreePath, ".ycc", "sessions", id)); why != "" {
			return fmt.Sprintf("session %s: %s", id, why)
		}
	}
	return ""
}

// preserveWorkstreamSessions copies both normal and integrate-agent transcripts
// from the linked worktree before lifecycle cleanup removes it.
func (m *Manager) preserveWorkstreamSessions(ws workstream.Workstream) {
	m.preserveWorkstreamSessionID(ws, ws.SessionID)
	m.preserveWorkstreamSessionID(ws, ws.IntegrateSessionID)
}

// preserveWorkstreamSessionID copies one durable session log out of a worktree
// into the project's primary workspace. It is entirely best-effort: preservation
// never blocks a lifecycle transition, and an existing destination is untouched.
func (m *Manager) preserveWorkstreamSessionID(ws workstream.Workstream, sessionID string) {
	if sessionID == "" || ws.WorktreePath == "" {
		return
	}
	// Never traverse a persisted source path outside the daemon's worktrees root.
	if err := workstream.VerifyUnderRoot(m.worktreesRoot, ws.WorktreePath); err != nil {
		return
	}
	primary, ok := m.projects.Resolve(ws.Project)
	if !ok {
		return
	}
	src := filepath.Join(ws.WorktreePath, ".ycc", "sessions", sessionID)
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return
	}
	dst := filepath.Join(primary, ".ycc", "sessions", sessionID)
	dstLog := filepath.Join(dst, "events.jsonl")
	if nonEmptyLog(dstLog) == nil {
		return // already preserved
	}
	// An empty log an older daemon left by reopening from the primary would
	// otherwise survive the no-overwrite copy and hide the real one.
	_ = os.Remove(dstLog)
	copyDir(src, dst)
}

// copyDir recursively copies the private session-state directory at src to dst,
// best-effort. It does not overwrite files that already exist at the destination.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
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
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// cleanupWorktree tears down a workstream's worktree + branch after a successful
// merge or a discard. Every git step is best-effort: a failure
// to remove a tree/branch must not block the lifecycle transition.
func (m *Manager) cleanupWorktree(repo *git.Repo, ws workstream.Workstream) {
	if ws.WorktreePath != "" {
		// Registry entries are untrusted. Fail closed for paths outside daemon-owned
		// worktree state while still allowing branch cleanup and lifecycle progress.
		if err := workstream.VerifyUnderRoot(m.worktreesRoot, ws.WorktreePath); err == nil {
			repo.RemoveWorktree(ws.WorktreePath)
		}
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
// worktree + branch, and marks the registry entry discarded. It is allowed for active or stale workstreams; git cleanup is best-effort
// so a stale entry whose tree is already gone still transitions cleanly.
func (m *Manager) DiscardWorkstream(id string) error {
	m.mergeMu.Lock()
	defer m.mergeMu.Unlock()

	ws, ok := m.workstreams.Get(id)
	if !ok {
		return fmt.Errorf("unknown workstream %q", id)
	}
	if !ws.Status.InFlight() && ws.Status != workstream.StatusStale {
		return fmt.Errorf("workstream %q cannot be discarded (status %s)", id, ws.Status)
	}
	if why := m.workstreamExecutionUnsettled(ws); why != "" {
		m.integrationNeedsAttentionFrom(ws, "discard blocked: "+why, nil, workstream.StatusActive, workstream.StatusReady, workstream.StatusNeedsAttention)
		return fmt.Errorf("workstream %q cannot be discarded yet: %s", id, why)
	}
	token := m.ownership.NewToken("workstream " + id + " discard")
	if primary, projectOK := m.projects.Resolve(ws.Project); projectOK {
		primaryLease, err := m.acquireSection(primary, token)
		if err != nil {
			return fmt.Errorf("discard workstream: %w", err)
		}
		defer primaryLease.Release()
	}
	if _, statErr := os.Stat(ws.WorktreePath); statErr == nil {
		worktreeLease, acquireErr := m.acquireSection(ws.WorktreePath, token)
		if acquireErr != nil {
			return fmt.Errorf("discard workstream: %w", acquireErr)
		}
		defer worktreeLease.Release()
	}
	m.emitWorkstreamEvent(ws, event.WorkstreamDiscarded, map[string]any{
		"workstream": ws.ID,
		"branch":     ws.Branch,
	})
	if err := m.workstreams.SetStatus(ws.ID, workstream.StatusDiscarded); err != nil {
		return err
	}
	if fresh, ok := m.workstreams.Get(ws.ID); ok {
		ws = fresh
	}
	if unjoined := m.stopWorkstreamSessions(ws); unjoined != nil {
		// A job may still be writing in the tree: keep it, the branch and the
		// session logs there (still authoritative for a reopen), and leave the
		// stream in flight (needs attention) so discard can be retried once
		// the stop is confirmed.
		m.integrationNeedsAttentionFrom(ws, "discard incomplete: "+unjoined.Error(), nil, workstream.StatusDiscarded)
		return fmt.Errorf("workstream %q was not discarded: %w", id, unjoined)
	}
	m.preserveWorkstreamSessions(ws)
	// Cleanup is best-effort; a stale entry's tree may already be gone. Persisted
	// paths are untrusted, so an out-of-root path is never passed to git. Branch
	// cleanup and the discarded transition still proceed in that fail-closed case.
	if repo, err := m.primaryRepo(ws); err == nil {
		if ws.WorktreePath != "" {
			if err := workstream.VerifyUnderRoot(m.worktreesRoot, ws.WorktreePath); err == nil {
				repo.RemoveWorktree(ws.WorktreePath)
			}
		}
		if ws.Branch != "" {
			repo.DeleteBranch(ws.Branch, true)
		}
		repo.PruneWorktrees()
	}
	return nil
}
