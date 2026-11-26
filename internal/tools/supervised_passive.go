package tools

import (
	"sort"
	"sync"
	"time"

	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/jobshim"
)

// PassiveJobs is the daemon's passive supervisor table
// (docs/design/daemon-restart.md §5): the supervised background jobs of
// sessions that stayed closed after a daemon restart or crash, which keep
// running with no live session following them. Each held job is followed
// through its control socket and registered as a running background shell in
// its worktree, so other sessions' shell attribution sees it overlap, until
// its exit is confirmed or a reopen of its session takes it over (Take). It
// writes nothing to session logs: a reopen's reconciliation records outcomes.
// Its methods are safe on a nil table, which holds nothing.
type PassiveJobs struct {
	mu       sync.Mutex
	sessions map[string]map[string]*passiveJob // session dir → job id → job
}

type passiveJob struct {
	id, dir string
	handle  *jobshim.Handle
	ws      *Workspace     // the session's worktree, for the shell registration
	shell   *shellSnapshot // its running-shell registration
	taken   chan struct{}  // closed when a reopen takes the job over
	settled chan struct{}  // closed when its exit is confirmed or an operator cleared it
}

// NewPassiveJobs returns an empty table.
func NewPassiveJobs() *PassiveJobs {
	return &PassiveJobs{sessions: map[string]map[string]*passiveJob{}}
}

// clearedPoll is how often a held job whose execution is unknown is checked
// for an operator's cleared marker.
const clearedPoll = 2 * time.Second

// Supervise holds the closed session's supervised jobs that may still run:
// every supervised bash job in entries (engine.RestoreJobEntries of its log)
// whose spec.json matches and that has neither a valid exit record nor an
// operator's cleared marker. ws is a workspace over the session's worktree
// (Root, Ownership and the session's scoped MutationToken). A job owned by
// anyone but coordinator (a subagent's shell, which does not outlive its
// subagent) or already killed is asked to stop; Supervise returns those
// handles for the caller to join (a bounded wait on Done), and keeps them held
// until their exit is confirmed, so one that cannot be stopped stays held as
// unknown. Jobs already held are left alone.
func (p *PassiveJobs) Supervise(sup *jobshim.Session, entries []jobs.Restored, coordinator string, ws *Workspace) (stopping []*jobshim.Handle) {
	if p == nil || sup == nil {
		return nil
	}
	key := sup.Dir()
	for _, e := range entries {
		if !e.Supervised || e.Kind != "bash" {
			continue
		}
		if _, err := sup.ReadSpec(e.ID); err != nil {
			continue
		}
		dir := sup.JobDir(e.ID)
		if _, err := jobshim.ReadExit(dir); err == nil || jobshim.Cleared(dir) {
			continue
		}
		p.mu.Lock()
		_, held := p.sessions[key][e.ID]
		p.mu.Unlock()
		if held {
			continue
		}
		j := &passiveJob{id: e.ID, dir: dir, handle: sup.Attach(e.ID), ws: ws, taken: make(chan struct{}), settled: make(chan struct{})}
		j.shell = ws.recoveredShell()
		p.mu.Lock()
		if p.sessions[key] == nil {
			p.sessions[key] = map[string]*passiveJob{}
		}
		p.sessions[key][e.ID] = j
		p.mu.Unlock()
		go p.follow(key, j)
		if e.Owner != coordinator || e.Status == jobs.Killed {
			select {
			case <-j.handle.Done():
			default:
				_ = j.handle.Kill()
				stopping = append(stopping, j.handle)
			}
		}
	}
	return stopping
}

// follow drops a held job once its exit is confirmed: by the exit record its
// supervisor reports, or, after supervision was lost, by one it left before
// releasing shim.lock. Without one the job stays held as unknown until an
// operator clears it (jobshim.Cleared), which follow watches for.
func (p *PassiveJobs) follow(key string, j *passiveJob) {
	select {
	case <-j.handle.Done():
	case <-j.taken:
		return
	}
	if j.handle.Outcome().Exit == nil {
		select {
		case <-j.handle.Exited():
		case <-j.taken:
			return
		}
		if _, err := jobshim.ReadExit(j.dir); err != nil {
			t := time.NewTicker(clearedPoll)
			defer t.Stop()
			for !jobshim.Cleared(j.dir) {
				select {
				case <-t.C:
				case <-j.taken:
					return
				}
				if !p.holds(key, j) {
					return // retired by Unsettled
				}
			}
		}
	}
	p.retire(key, j)
}

// holds reports whether j is still held under key.
func (p *PassiveJobs) holds(key string, j *passiveJob) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[key][j.id] == j
}

// retire drops j, if still held, and ends its shell registration.
func (p *PassiveJobs) retire(key string, j *passiveJob) {
	p.mu.Lock()
	held := p.sessions[key][j.id] == j
	if held {
		delete(p.sessions[key], j.id)
		if len(p.sessions[key]) == 0 {
			delete(p.sessions, key)
		}
	}
	p.mu.Unlock()
	if held {
		j.ws.endShell(j.shell)
		close(j.settled)
	}
}

// Watch reports whether the session's job id is held and, if so, returns two
// channels: settled closes once the job's exit is confirmed or an operator
// cleared it (never while its execution is unknown), and taken closes when a
// reopen takes the job over (Take), after which settled never closes. A
// passive lease follows its job through them (docs/design/daemon-restart.md §5).
func (p *PassiveJobs) Watch(sessionDir, id string) (settled, taken <-chan struct{}, ok bool) {
	if p == nil {
		return nil, nil, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	j := p.sessions[sessionDir][id]
	if j == nil {
		return nil, nil, false
	}
	return j.settled, j.taken, true
}

// Take hands the session's held jobs to its reopen, which follows them through
// these handles instead of connecting again (ReconcileSupervisedJobs). The
// table stops following them at once. end unregisters their passive running
// shells; call it after the reopen registered its own (SupervisedReopen.Activate),
// so the worktree never lacks the registration in between.
func (p *PassiveJobs) Take(sessionDir string) (handles map[string]*jobshim.Handle, end func()) {
	if p == nil {
		return nil, func() {}
	}
	p.mu.Lock()
	held := p.sessions[sessionDir]
	delete(p.sessions, sessionDir)
	p.mu.Unlock()
	handles = make(map[string]*jobshim.Handle, len(held))
	for id, j := range held {
		close(j.taken)
		handles[id] = j.handle
	}
	var once sync.Once
	return handles, func() {
		once.Do(func() {
			for _, j := range held {
				j.ws.endShell(j.shell)
			}
		})
	}
}

// Unsettled reports why a held job of the session may still run in its
// worktree, or "" when none is held. A held job an operator cleared is
// retired here, before the caller acts on the answer (removing the worktree
// or the session directory), so no registration outlives its evidence.
func (p *PassiveJobs) Unsettled(sessionDir string) string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	held := make([]*passiveJob, 0, len(p.sessions[sessionDir]))
	for _, j := range p.sessions[sessionDir] {
		held = append(held, j)
	}
	p.mu.Unlock()
	sort.Slice(held, func(a, b int) bool { return jobNumber(held[a].id) < jobNumber(held[b].id) })
	for _, j := range held {
		if jobshim.Cleared(j.dir) {
			p.retire(sessionDir, j)
			continue
		}
		select {
		case <-j.handle.Done():
			if j.handle.Outcome().Exit != nil {
				continue // confirmed; follow is dropping it
			}
			if _, err := jobshim.ReadExit(j.dir); err == nil {
				continue
			}
			return "background job " + j.id + " lost supervision after a daemon restart and its command may still be running"
		default:
			return "background job " + j.id + " kept running across a daemon restart and has not finished"
		}
	}
	return ""
}

// Held lists the session's held job ids in id order.
func (p *PassiveJobs) Held(sessionDir string) []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, 0, len(p.sessions[sessionDir]))
	for id := range p.sessions[sessionDir] {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return jobNumber(ids[a]) < jobNumber(ids[b]) })
	return ids
}
