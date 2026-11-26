package session

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
)

// ErrShuttingDown is returned when a session would start or reopen after
// graceful daemon shutdown began.
var ErrShuttingDown = errors.New("daemon is shutting down")

// drainPoll is how often Drain re-checks live sessions for a clean boundary.
const drainPoll = 100 * time.Millisecond

// idLock is one session id's Reopen serialization lock; refs counts holders and
// waiters so the map entry is dropped when nobody needs it.
type idLock struct {
	mu   sync.Mutex
	refs int
}

// lockSessionID serializes preparation of one session id and returns its unlock.
// Lock order: an idLock is taken before mu and never while holding mu.
func (m *Manager) lockSessionID(id string) func() {
	m.mu.Lock()
	if m.idLocks == nil {
		m.idLocks = map[string]*idLock{}
	}
	l := m.idLocks[id]
	if l == nil {
		l = &idLock{}
		m.idLocks[id] = l
	}
	l.refs++
	m.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		m.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(m.idLocks, id)
		}
		m.mu.Unlock()
	}
}

// HoldExecution makes sessions registered from now on wait for
// ReleaseExecution before their run owners start, and holds background git sync.
// A persistent daemon holds execution while it prepares recovered state at
// startup (docs/design/daemon-restart.md §1).
func (m *Manager) HoldExecution() {
	m.mu.Lock()
	m.execHeld = true
	m.mu.Unlock()
}

// ReleaseExecution starts every run owner held since HoldExecution and lets
// later sessions start immediately.
func (m *Manager) ReleaseExecution() {
	m.mu.Lock()
	held := m.heldRuns
	m.execHeld, m.heldRuns = false, nil
	m.mu.Unlock()
	for _, s := range held {
		s.startRun()
	}
}

func (m *Manager) executionHeld() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.execHeld
}

// launch starts a registered session's run owner, or holds it while startup
// holds execution.
func (m *Manager) launch(s *Session) {
	m.mu.Lock()
	if m.execHeld {
		m.heldRuns = append(m.heldRuns, s)
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	s.startRun()
}

// startRun starts the run owner unless restart teardown already retired the
// session.
func (s *Session) startRun() {
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	if s.retired || s.launched {
		return
	}
	s.launched = true
	go s.run()
}

// BeginShutdown starts graceful daemon shutdown: new sessions are refused
// (ErrShuttingDown), and manager-owned background workers (git sync, work
// loops, the integration queue, grooms, the watch scheduler, the GC reaper) are
// cancelled. Work loops of a daemon that keeps a restart manifest are not
// cancelled but stop starting sessions, and are suspended with their sessions
// (suspendLoopsForRestart). They are joined only after the sessions are suspended
// (ReclaimAll), since some worker steps (a fetch, a rebase) are not
// interruptible. From here on ShutdownForRestart is the single teardown owner:
// a worker that would reclaim one of its sessions leaves it live
// (reclaimIfCurrent), so Drain gives every live session the same window. It is
// idempotent and does not block.
func (m *Manager) BeginShutdown() {
	m.mu.Lock()
	// A daemon that keeps a restart manifest suspends its work loops for the
	// restart rather than ending them (workloop_restart.go).
	if m.restartStateFile != "" {
		m.loopRestart.Store(true)
	}
	m.shuttingDown.Store(true)
	// The integration queue's stop flag lives under mu too: no stream that
	// becomes ready during the drain is enqueued or dequeued (it stays durably
	// ready for the next daemon).
	m.integrationStop = true
	m.mu.Unlock()
	m.gitSyncCancel()
	m.cancelBackground()
}

// Drain waits until every live session is at a clean restart boundary
// (restartQuiet), or ctx ends. It returns the ids still busy when it gave up.
func (m *Manager) Drain(ctx context.Context) []string {
	t := time.NewTicker(drainPoll)
	defer t.Stop()
	for {
		var busy []string
		for _, s := range m.List() {
			if !s.restartQuiet() {
				busy = append(busy, s.ID)
			}
		}
		if len(busy) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return busy
		case <-t.C:
		}
	}
}

// ShutdownForRestart finishes graceful shutdown: after BeginShutdown it
// suspends every live session for restart (suspendAll), and then writes the
// restart manifest (restart_state.go) naming the sessions the next daemon
// reopens and the leases it holds again. It is the single teardown owner for
// the persistent daemon; a later ReclaimAll finds nothing left to undo.
func (m *Manager) ShutdownForRestart() {
	m.BeginShutdown()
	candidates := m.restartCandidates()
	m.ReclaimAll()
	m.writeRestartManifest(candidates)
}

// suspendAll suspends every live session for restart. Each one is cancelled
// before any is joined, so independent in-flight turns stop concurrently. A
// session stays in the live map until it is fully suspended, so a writer that
// looks it up appends through its single log rather than a second handle.
func (m *Manager) suspendAll() {
	sessions := m.List()
	for _, s := range sessions {
		s.cancelForRestart()
	}
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			m.suspendSession(s)
		}(s)
	}
	wg.Wait()
	m.mu.Lock()
	for _, s := range sessions {
		if m.sessions[s.ID] == s {
			delete(m.sessions, s.ID)
		}
	}
	m.mu.Unlock()
}

// suspendSession is the restart-specific teardown, distinct from Stop, idle
// reap, and fail-stop. It cancels with the restart cause and joins the run
// owner, whose exit closes job registration, kills and joins subagents and
// background jobs (with the restart cause, so a subagent's wait claims
// nothing), and stops its writes. It then releases leases (keeping those the
// restart manifest carries), stops and joins the
// workstream watcher while the log is still open, and only then closes the
// log. It records no session_stopped, and an interrupted wait or ask_user keeps
// its trailing tool_call unresolved for recovery. It shares stopOnce with Stop
// and reap, so at most one of them runs.
func (m *Manager) suspendSession(s *Session) {
	s.stopOnce.Do(func() {
		s.cancelForRestart()
		s.launchMu.Lock()
		s.retired = true
		launched := s.launched
		s.launchMu.Unlock()
		if launched && s.runDone != nil {
			<-s.runDone
		}
		s.killJobs()
		s.releaseLeasesForRestart()
		m.stopWorkstreamWatcher(s.ID)
		if s.log != nil {
			s.log.Close()
		}
	})
	m.waitWorkstreamWatcher(s.ID)
}

// cancelForRestart cancels the session's run context with the restart cause, so
// blocked wait and ask_user calls exit without recording a result.
func (s *Session) cancelForRestart() {
	if s.cancelCause != nil {
		s.cancelCause(jobs.ErrRestartSuspend)
	} else if s.cancel != nil {
		s.cancel()
	}
}

// restartQuiet reports whether the session is at a clean boundary graceful
// shutdown need not wait for: its coordinator is between turns (idle, paused,
// errored, stopped), parked on ask_user, or blocked in wait — and every running
// subagent is itself blocked in wait. A coordinator or subagent mid model turn
// or mid foreground tool is still busy.
func (s *Session) restartQuiet() bool {
	if s.logFailure() != nil {
		return true
	}
	if s.Status() == event.StatusRunning && !s.PendingQuestion() && !s.blockedInWait() {
		return false
	}
	if s.deps != nil && s.deps.Jobs != nil {
		for _, info := range s.deps.Jobs.List("", true) {
			// A subagent's loop reports its current tool through the job's
			// activity; it has no ask_user.
			if info.Kind == "agent" && info.Status == jobs.Running && info.Activity.CurrentTool != "wait" {
				return false
			}
		}
	}
	return true
}

// blockedInWait reports whether the coordinator's latest turn step is the
// tool_call of a wait still in progress. Other coordinator-actor events (a job
// finishing while a wait for all its targets continues, notices) are skipped.
func (s *Session) blockedInWait() bool {
	if s.log == nil {
		return false
	}
	ev, ok := s.log.LastByActor("coordinator", event.ModelTurn, event.ToolCall, event.ToolResult)
	if !ok || ev.Type != event.ToolCall {
		return false
	}
	name, _ := ev.Data["name"].(string)
	return name == "wait"
}
