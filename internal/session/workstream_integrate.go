package session

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/notify"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

const (
	integrationVerifyTimeout = 30 * time.Minute
	integrationAgentTimeout  = time.Hour
	integrationOutputLimit   = 2 * 1024
)

type integrationOutcomeKind int

const (
	integrationHandled integrationOutcomeKind = iota
	integrationConflict
	integrationVerifyFailed
)

type integrationOutcome struct {
	kind         integrationOutcomeKind
	base         string
	reason       string
	conflicts    []string
	verifyCmd    string
	verifyOutput string
	verifyErr    string
}

func (o integrationOutcome) agentFixable() bool {
	return o.kind == integrationConflict || o.kind == integrationVerifyFailed
}

func (o integrationOutcome) eventData() map[string]any {
	if o.kind == integrationConflict {
		return map[string]any{"conflicts": o.conflicts}
	}
	if o.kind == integrationVerifyFailed {
		return map[string]any{"verify": o.verifyCmd, "output": o.verifyOutput, "error": o.verifyErr}
	}
	return nil
}

type integrateAgentResult struct {
	report  string
	blocked bool
	err     error
}

// workstreamIntegrator is one project's in-memory integration queue. Access to
// every field is guarded by Manager.mu. queued includes the currently-running id,
// so repeated readiness signals coalesce instead of scheduling a second attempt.
type workstreamIntegrator struct {
	pending  []string
	queued   map[string]bool
	draining bool
}

// effectiveIntegrationMode applies the safety degradations for the fast-path
// queue. Empty mode/strategy resolve to auto/rebase-ff. Warnings are emitted only
// for configurations that asked for auto but cannot safely auto-integrate.
func (m *Manager) effectiveIntegrationMode() string {
	cfg := m.reg.IntegrationConfig()
	mode := cfg.Mode
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" {
		return mode
	}
	if strings.TrimSpace(cfg.Verify) == "" {
		log.Printf("ycc: integration: mode auto requires integration.verify; degrading to gate")
		return "gate"
	}
	strategy := cfg.Strategy
	if strategy == "" {
		strategy = "rebase-ff"
	}
	if strategy != "rebase-ff" {
		log.Printf("ycc: integration: mode auto with strategy %q is not implemented; degrading to gate", strategy)
		return "gate"
	}
	return "auto"
}

// enqueueWorkstreamIntegration adds a ready workstream to its project's queue.
// A project has exactly one drainer, and ids already pending/running are ignored.
func (m *Manager) enqueueWorkstreamIntegration(ws workstream.Workstream) {
	m.mu.Lock()
	if m.integrationStop {
		m.mu.Unlock()
		return
	}
	q := m.integrators[ws.Project]
	if q == nil {
		q = &workstreamIntegrator{queued: make(map[string]bool)}
		m.integrators[ws.Project] = q
	}
	if q.queued[ws.ID] {
		m.mu.Unlock()
		return
	}
	q.queued[ws.ID] = true
	q.pending = append(q.pending, ws.ID)
	if q.draining {
		m.mu.Unlock()
		return
	}
	q.draining = true
	m.integrationWG.Add(1)
	m.mu.Unlock()

	go m.drainWorkstreamIntegrations(ws.Project, q)
}

func (m *Manager) drainWorkstreamIntegrations(project string, q *workstreamIntegrator) {
	defer m.integrationWG.Done()
	for {
		m.mu.Lock()
		if len(q.pending) == 0 || m.integrationStop {
			q.draining = false
			if len(q.pending) == 0 {
				delete(m.integrators, project)
			}
			m.mu.Unlock()
			return
		}
		id := q.pending[0]
		q.pending = q.pending[1:]
		m.mu.Unlock()

		m.integrateReadyWorkstream(id)

		m.mu.Lock()
		delete(q.queued, id)
		m.mu.Unlock()
	}
}

// integrateReadyWorkstream first tries the zero-token fast path, then delegates
// only content conflicts and red verification to bounded integrate-mode sessions.
// Every successful agent attempt is distrusted: the daemon reruns the complete
// fast path before it can advance base.
func (m *Manager) integrateReadyWorkstream(id string) {
	out := m.integrationFastPath(id)
	maxAttempts := m.reg.IntegrationConfig().EffectiveAgentAttempts()
	attempts := maxAttempts
	for out.agentFixable() {
		ws, ok := m.workstreams.Get(id)
		if !ok || ws.Status != workstream.StatusReady {
			return
		}
		if attempts == 0 {
			m.integrationNeedsAttention(ws, out.reason, out.eventData())
			return
		}
		m.mu.Lock()
		stopping := m.integrationStop
		m.mu.Unlock()
		if stopping || m.integrationCtx.Err() != nil {
			m.restoreWorkstreamReadyProjection(ws, "integration agent recovery interrupted")
			return
		}

		attempt := maxAttempts - attempts + 1
		res := m.integrateAgent(ws, attempt, out)
		attempts--
		if m.integrationCtx.Err() != nil {
			m.restoreWorkstreamReadyProjection(ws, "integration agent recovery interrupted")
			return
		}
		if res.blocked || res.err != nil {
			status := "failed"
			detail := res.report
			if res.blocked {
				status = "blocked"
			}
			if res.err != nil {
				if detail != "" {
					detail += ": "
				}
				detail += res.err.Error()
			}
			reason := fmt.Sprintf("integration agent %s", status)
			if detail != "" {
				reason += ": " + detail
			}
			reason += "\noriginal integration failure: " + out.reason
			m.integrationNeedsAttention(ws, reason, out.eventData())
			return
		}
		out = m.integrationFastPath(id)
	}
}

// integrationFastPath owns each inspection/rebase/verify/base-advance attempt.
// The global merge mutex excludes manual merge and discard while git refs and
// worktrees are being inspected or changed; it is deliberately released before
// an agent session runs.
func (m *Manager) integrationFastPath(id string) integrationOutcome {
	m.mergeMu.Lock()
	defer m.mergeMu.Unlock()

	if m.effectiveIntegrationMode() != "auto" {
		return integrationOutcome{kind: integrationHandled}
	}
	ws, ok := m.workstreams.Get(id)
	if !ok || ws.Status != workstream.StatusReady {
		return integrationOutcome{kind: integrationHandled}
	}
	cfg := m.reg.IntegrationConfig()

	repo, err := m.primaryRepo(ws)
	if err != nil {
		m.integrationNeedsAttention(ws, fmt.Sprintf("integration failed: %v", err), nil)
		return integrationOutcome{kind: integrationHandled}
	}
	base, err := m.workstreamBaseBranch(repo, ws)
	if err != nil {
		m.integrationNeedsAttention(ws, fmt.Sprintf("integration failed: %v", err), nil)
		return integrationOutcome{kind: integrationHandled}
	}
	if err := repo.CheckBaseClean(base); err != nil {
		if errors.Is(err, git.ErrBaseTreeDirty) {
			m.deferWorkstreamIntegration(ws, fmt.Sprintf("integration deferred: base tree dirty: %v", err))
			return integrationOutcome{kind: integrationHandled}
		}
		m.integrationNeedsAttention(ws, fmt.Sprintf("check base branch %s: %v", base, err), nil)
		return integrationOutcome{kind: integrationHandled}
	}
	if err := workstream.VerifyUnderRoot(m.worktreesRoot, ws.WorktreePath); err != nil {
		m.integrationNeedsAttention(ws, fmt.Sprintf("invalid worktree path: %v", err), nil)
		return integrationOutcome{kind: integrationHandled}
	}

	m.emitWorkstreamEvent(ws, event.WorkstreamIntegrating, map[string]any{
		"workstream":  ws.ID,
		"branch":      ws.Branch,
		"base_branch": base,
	})

	rebase, err := repo.RebaseOnto(ws.WorktreePath, base)
	if err != nil {
		m.integrationNeedsAttention(ws, fmt.Sprintf("rebase onto %s failed: %v", base, err), map[string]any{"error": err.Error()})
		return integrationOutcome{kind: integrationHandled}
	}
	if !rebase.Clean {
		reason := fmt.Sprintf("rebase onto %s conflicts: %s", base, strings.Join(rebase.Conflicts, ", "))
		return integrationOutcome{
			kind: integrationConflict, base: base, reason: reason, conflicts: rebase.Conflicts,
			verifyCmd: cfg.Verify,
		}
	}

	// Pin the exact rebased revision before verification. The workstream branch is
	// a live ref: a resumed session or a user's shell may advance it while verify
	// runs, and those later commits must never ride into base unverified.
	verifiedTip, err := repo.RevParse("refs/heads/" + ws.Branch)
	if err != nil {
		m.integrationNeedsAttention(ws, fmt.Sprintf("resolve rebased tip for verification: %v", err), map[string]any{"error": err.Error()})
		return integrationOutcome{kind: integrationHandled}
	}
	statusBefore, err := repo.WorktreeStatusPorcelain(ws.WorktreePath)
	if err != nil {
		reason := fmt.Sprintf("integration aborted: snapshot worktree before verify: %v", err)
		m.abortReadyIntegration(ws, reason, map[string]any{"error": err.Error()})
		return integrationOutcome{kind: integrationHandled}
	}
	if tracked := trackedWorktreeChanges(statusBefore); tracked != "" {
		reason := "integration aborted: rebased worktree has staged or unstaged tracked-file changes"
		m.abortReadyIntegration(ws, reason, map[string]any{
			"tracked_changes": boundedIntegrationOutput(tracked),
			"status_before":   boundedIntegrationOutput(statusBefore),
		})
		return integrationOutcome{kind: integrationHandled}
	}

	output, err := m.runIntegrationVerify(ws, cfg.Verify)
	if err != nil {
		tail := boundedIntegrationOutput(output)
		display := tail
		if display == "" {
			display = "(no output)"
		}
		reason := fmt.Sprintf("verify %q failed: %v\n%s", cfg.Verify, err, display)
		return integrationOutcome{
			kind: integrationVerifyFailed, base: base, reason: reason,
			verifyCmd: cfg.Verify, verifyOutput: tail, verifyErr: err.Error(),
		}
	}

	// Verification is valid only while both the branch and lifecycle state remain
	// unchanged. A resumed session legitimately flips ready to active; abort without
	// touching base and let its next readiness event enqueue a fresh attempt.
	currentTip, tipErr := repo.RevParse("refs/heads/" + ws.Branch)
	currentWS, statusOK := m.workstreams.Get(ws.ID)
	statusAfter, contentErr := repo.WorktreeStatusPorcelain(ws.WorktreePath)
	if tipErr != nil || currentTip != verifiedTip || !statusOK || currentWS.Status != workstream.StatusReady || contentErr != nil || statusAfter != statusBefore {
		reason := "integration aborted: workstream changed while verify was running"
		extra := map[string]any{}
		notifyUser := false
		if contentErr != nil {
			reason = fmt.Sprintf("integration aborted: snapshot worktree after verify: %v", contentErr)
			extra["error"] = contentErr.Error()
			notifyUser = true
		} else if statusAfter != statusBefore {
			reason = "integration aborted: worktree contents changed while verify was running"
			extra["status_before"] = boundedIntegrationOutput(statusBefore)
			extra["status_after"] = boundedIntegrationOutput(statusAfter)
			notifyUser = true
		} else if tipErr != nil {
			reason = fmt.Sprintf("integration aborted: resolve branch after verify: %v", tipErr)
		} else if currentTip != verifiedTip {
			reason = fmt.Sprintf("integration aborted: branch moved from verified tip %s to %s", verifiedTip, currentTip)
		} else if !statusOK {
			reason = "integration aborted: workstream was removed while verify was running"
		} else {
			reason = fmt.Sprintf("integration aborted: workstream status changed from ready to %s", currentWS.Status)
		}
		if notifyUser {
			m.abortReadyIntegration(ws, reason, extra)
		} else {
			m.restoreWorkstreamReadyProjectionData(ws, reason, extra)
		}
		return integrationOutcome{kind: integrationHandled}
	}

	// Advance to the immutable SHA that passed verify, never to the mutable branch
	// name. AdvanceBranch retains its fast-forward-only ancestry and clean-tree
	// checks for commit-ish targets.
	commit, err := repo.AdvanceBranch(base, verifiedTip)
	if err != nil {
		if errors.Is(err, git.ErrBaseTreeDirty) {
			reason := fmt.Sprintf("integration deferred: base tree dirty: %v", err)
			m.restoreWorkstreamReadyProjection(ws, reason)
			m.deferWorkstreamIntegration(ws, reason)
			return integrationOutcome{kind: integrationHandled}
		}
		m.integrationNeedsAttention(ws, fmt.Sprintf("advance base branch %s failed: %v", base, err), map[string]any{"error": err.Error()})
		return integrationOutcome{kind: integrationHandled}
	}

	// Base now contains exactly the verified tip. Do not clean up if the branch
	// advanced concurrently: its additional, unmerged commits must be preserved.
	finalTip, finalErr := repo.RevParse("refs/heads/" + ws.Branch)
	if finalErr != nil || finalTip != verifiedTip {
		reason := fmt.Sprintf("base advanced to verified tip %s, but branch gained commits during integration; worktree kept", verifiedTip)
		if finalErr != nil {
			reason = fmt.Sprintf("base advanced to verified tip %s, but branch could not be re-read: %v; worktree kept", verifiedTip, finalErr)
		}
		m.integrationNeedsAttentionFrom(ws, reason, map[string]any{
			"verified_tip": verifiedTip,
			"branch_tip":   finalTip,
		}, workstream.StatusReady, workstream.StatusActive)
		return integrationOutcome{kind: integrationHandled}
	}

	changed, err := m.workstreams.Transition(ws.ID, workstream.StatusMerged, "", workstream.StatusReady)
	if err != nil || !changed {
		m.restoreWorkstreamReadyProjection(ws, "integration finish aborted: workstream is no longer ready")
		return integrationOutcome{kind: integrationHandled}
	}
	m.emitWorkstreamEvent(ws, event.WorkstreamMerged, map[string]any{
		"workstream":  ws.ID,
		"branch":      ws.Branch,
		"base_branch": base,
		"commit":      commit,
	})
	if fresh, ok := m.workstreams.Get(ws.ID); ok {
		ws = fresh
	}
	m.stopWorkstreamSessions(ws)
	m.preserveWorkstreamSessions(ws)
	m.cleanupWorktree(repo, ws)
	m.Notify(notify.KindMerged, ws.Project, ws.SessionID,
		fmt.Sprintf("workstream %s merged into %s at %s", ws.ID, base, commit))
	return integrationOutcome{kind: integrationHandled}
}

// runIntegrateSession starts one normal unattended session rooted in the linked
// worktree and waits for its control-tool result. It never holds mergeMu, so the
// agent can run git in its worktree without giving it access to daemon-owned base
// advancement.
func (m *Manager) runIntegrateSession(ws workstream.Workstream, attempt int, out integrationOutcome) (result integrateAgentResult) {
	current, ok := m.workstreams.Get(ws.ID)
	if !ok || current.Status != workstream.StatusReady {
		return integrateAgentResult{err: fmt.Errorf("workstream is no longer ready")}
	}
	ws = current

	data := map[string]any{
		"workstream": ws.ID, "branch": ws.Branch, "base_branch": out.base,
		"agent": true, "attempt": attempt,
	}
	if len(out.conflicts) > 0 {
		data["conflicts"] = out.conflicts
	}
	if out.verifyOutput != "" {
		data["verify_output"] = boundedIntegrationOutput(out.verifyOutput)
	}
	m.emitWorkstreamEvent(ws, event.WorkstreamIntegrating, data)

	failure := out.reason
	if out.kind == integrationConflict {
		failure += "\nThe daemon aborted the conflicted rebase and restored the worktree; re-run git rebase yourself."
	}
	seed := fmt.Sprintf(`Recover integration for workstream %s on branch %s.
Base branch: %s
Verify command: %s
Failure to resolve:
%s

Work only in this linked worktree. Re-run the rebase onto the named base when needed, resolve or fix the failure, commit the result on the workstream branch, and run the verify command until green. Then call request_integration. Never check out, merge into, advance, reset, or otherwise modify the base branch; never push. The daemon will independently re-rebase and re-run verify and alone owns advancing base. If the correct resolution is not yours to decide, call report_blocked.`,
		ws.ID, ws.Branch, out.base, out.verifyCmd, failure)

	s, err := m.start(Config{Workspace: ws.WorktreePath, Mode: "integrate", Prompt: seed, Unattended: true}, false)
	if err != nil {
		return integrateAgentResult{err: fmt.Errorf("start integrate session: %w", err)}
	}
	// The registry exposes the latest recovery session for drill-in. Preserve an
	// earlier attempt before replacing that hook so bounded retries do not lose
	// their transcripts when the worktree is eventually removed.
	if ws.IntegrateSessionID != "" && ws.IntegrateSessionID != s.ID {
		m.preserveWorkstreamSessionID(ws, ws.IntegrateSessionID)
	}
	if err := m.workstreams.SetIntegrateSessionID(ws.ID, s.ID); err != nil {
		_ = m.Stop(s.ID)
		return integrateAgentResult{err: fmt.Errorf("record integrate session: %w", err)}
	}
	defer func() { _ = m.Stop(s.ID) }()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(integrationAgentTimeout)
	defer timer.Stop()
	var terminal event.Status
	for terminal == "" {
		switch status := s.Status(); status {
		case event.StatusIdle, event.StatusError, event.StatusStopped:
			terminal = status
			continue
		}
		select {
		case <-m.integrationCtx.Done():
			return integrateAgentResult{err: m.integrationCtx.Err()}
		case <-timer.C:
			return integrateAgentResult{err: fmt.Errorf("integrate session timed out after %s", integrationAgentTimeout)}
		case <-ticker.C:
		}
	}

	// Status is set immediately before its terminal event is emitted. Wait for that
	// event as well so a fast poll cannot miss the blocked flag or final report.
	var events []event.Event
	for {
		events = s.Log().Snapshot()
		recorded := terminal == event.StatusStopped
		for _, ev := range events {
			if (terminal == event.StatusIdle && ev.Type == event.SessionIdle) ||
				(terminal == event.StatusError && ev.Type == event.SessionError) {
				recorded = true
				break
			}
		}
		if recorded {
			break
		}
		select {
		case <-m.integrationCtx.Done():
			return integrateAgentResult{err: m.integrationCtx.Err()}
		case <-timer.C:
			return integrateAgentResult{err: fmt.Errorf("integrate session timed out after %s", integrationAgentTimeout)}
		case <-ticker.C:
		}
	}

	var report, sessionErr string
	blocked, requested := false, false
	for _, ev := range events {
		switch ev.Type {
		case event.ToolCall:
			if str(ev.Data, "name") == "request_integration" {
				requested = true
			}
		case event.SessionIdle:
			report = str(ev.Data, "report")
			blocked = boolVal(ev.Data, "blocked")
		case event.SessionError:
			sessionErr = str(ev.Data, "msg")
		}
	}
	if terminal == event.StatusError {
		if sessionErr == "" {
			sessionErr = "integrate session ended in error"
		}
		return integrateAgentResult{report: report, err: errors.New(sessionErr)}
	}
	if terminal == event.StatusStopped {
		return integrateAgentResult{report: report, err: errors.New("integrate session stopped before requesting integration")}
	}
	if !blocked && !requested {
		return integrateAgentResult{report: report, err: errors.New("integrate session ended without requesting integration")}
	}
	return integrateAgentResult{report: report, blocked: blocked}
}

func (m *Manager) runIntegrationVerify(ws workstream.Workstream, command string) (string, error) {
	ctx, cancel := context.WithTimeout(m.integrationCtx, integrationVerifyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = ws.WorktreePath
	wtCfg := m.reg.WorktreeConfig()
	if primary, ok := m.projects.Resolve(ws.Project); ok {
		wtCfg = m.worktreeConfigFor(primary)
	}
	cmd.Env = append(os.Environ(), sortedWorktreeEnv(wtCfg.Env)...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}
	return string(out), err
}

func boundedIntegrationOutput(output string) string {
	output = strings.TrimSpace(output)
	if len(output) <= integrationOutputLimit {
		return output
	}
	return "..." + output[len(output)-integrationOutputLimit:]
}

// trackedWorktreeChanges filters a porcelain-v1 snapshot to staged/unstaged
// tracked-file entries. Untracked files (??) are intentionally allowed before
// verify because worktree bootstrap commonly seeds files such as .env.
func trackedWorktreeChanges(snapshot string) string {
	var tracked []string
	for _, line := range strings.Split(strings.TrimSpace(snapshot), "\n") {
		if line == "" || strings.HasPrefix(line, "?? ") {
			continue
		}
		tracked = append(tracked, line)
	}
	return strings.Join(tracked, "\n")
}

func (m *Manager) integrationNeedsAttention(ws workstream.Workstream, reason string, extra map[string]any) {
	m.integrationNeedsAttentionFrom(ws, reason, extra, workstream.StatusReady)
}

func (m *Manager) integrationNeedsAttentionFrom(ws workstream.Workstream, reason string, extra map[string]any, from ...workstream.Status) {
	changed, err := m.workstreams.Transition(ws.ID, workstream.StatusNeedsAttention, reason, from...)
	if err != nil || !changed {
		return
	}
	data := map[string]any{
		"workstream": ws.ID,
		"branch":     ws.Branch,
		"reason":     reason,
	}
	for key, value := range extra {
		data[key] = value
	}
	m.emitWorkstreamEvent(ws, event.WorkstreamNeedsAttention, data)
	m.Notify(notify.KindAttention, ws.Project, ws.SessionID,
		fmt.Sprintf("workstream %s needs attention: %s", ws.ID, reason))
}

// restoreWorkstreamReadyProjection closes a post-workstream_integrating abort by
// recording that the durable integration state is ready again. It deliberately
// does not change the registry: a resumed session may already have moved it to
// active, while an ordinary defer remains ready.
func (m *Manager) restoreWorkstreamReadyProjection(ws workstream.Workstream, reason string) {
	m.restoreWorkstreamReadyProjectionData(ws, reason, nil)
}

func (m *Manager) restoreWorkstreamReadyProjectionData(ws workstream.Workstream, reason string, extra map[string]any) {
	data := map[string]any{
		"workstream": ws.ID,
		"branch":     ws.Branch,
		"reason":     reason,
	}
	for key, value := range extra {
		data[key] = value
	}
	m.emitWorkstreamEvent(ws, event.WorkstreamReady, data)
}

// abortReadyIntegration leaves registry state untouched, restores the durable
// projection from integrating to ready, and alerts unattended users that the
// worktree must be stable before the safe fast path can retry.
func (m *Manager) abortReadyIntegration(ws workstream.Workstream, reason string, extra map[string]any) {
	m.restoreWorkstreamReadyProjectionData(ws, reason, extra)
	m.Notify(notify.KindAttention, ws.Project, ws.SessionID,
		fmt.Sprintf("workstream %s: %s", ws.ID, reason))
}

func (m *Manager) deferWorkstreamIntegration(ws workstream.Workstream, reason string) {
	// Deferred attempts intentionally remain ready. A retry/restart may enqueue the
	// stream again after the user's base worktree is clean.
	m.Notify(notify.KindAttention, ws.Project, ws.SessionID,
		fmt.Sprintf("workstream %s: %s", ws.ID, reason))
}
