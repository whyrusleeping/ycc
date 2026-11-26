package session

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/jobs"
	"github.com/whyrusleeping/ycc/internal/jobshim"
	"github.com/whyrusleeping/ycc/internal/tools"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// ErrWorktreeSettling refuses to start or reopen a session in a workstream
// worktree while a merge, integration or discard settles it.
var ErrWorktreeSettling = errors.New("the workstream worktree is being merged or discarded")

// passiveStopWait bounds how long startup waits for the cancelled shells of
// closed sessions' subagents to exit: the supervisor's kill plus its
// WaitDelay, and some slack.
const passiveStopWait = jobshim.WaitDelay + 5*time.Second

// SupervisePassiveJobs is persistent startup's passive resource supervision
// (docs/design/daemon-restart.md §5, run in phase 3 with execution held). It
// scans the session directories of every registered project and of every
// workstream's verified worktree (implementation and integration sessions)
// for supervised background jobs that may still run, and holds them in the
// manager's passive table without writing to any session log, recording a
// session_reopened, or starting a turn:
//   - a subagent's shell, which does not outlive its subagent, is cancelled and
//     joined, bounded (one it cannot confirm stopped stays held as unknown);
//   - the rest stay held, guarding their worktree against workstream merge,
//     discard and integration and registered as running shells there, until
//     their exit is confirmed or their session's reopen takes them over.
//
// Their job directories stay protected from GC by what is on disk (no exit
// record, or an execution_unknown marker) across later restarts until an
// exit record or an operator's cleared marker settles them. Without a job
// launcher, nothing was supervised and nothing is scanned.
func (m *Manager) SupervisePassiveJobs() {
	if m.jobLauncher == nil {
		return
	}
	var stopping []*jobshim.Handle
	for _, c := range m.passiveCandidates() {
		stopping = append(stopping, m.supervisePassiveSession(c.id, c.workspace)...)
	}
	joinPassiveStops(stopping, passiveStopWait)
}

// joinPassiveStops waits, up to one deadline shared by all of them, for the
// cancelled shells' handles to report their outcome. Shells it gives up on
// stay held.
func joinPassiveStops(stopping []*jobshim.Handle, wait time.Duration) {
	if len(stopping) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	var wg sync.WaitGroup
	for _, h := range stopping {
		wg.Add(1)
		go func(h *jobshim.Handle) {
			defer wg.Done()
			select {
			case <-h.Done():
				if h.Outcome().Exit == nil {
					log.Printf("passive job supervision: %s: a subagent's shell could not be confirmed stopped; it stays held", h.Dir())
				}
			case <-ctx.Done():
				log.Printf("passive job supervision: %s: a subagent's shell has not stopped yet; it stays held", h.Dir())
			}
		}(h)
	}
	wg.Wait()
}

type passiveCandidate struct{ id, workspace string }

// passiveCandidates lists the closed sessions whose directories hold
// supervised jobs: those of registered projects with a log, and those of
// workstream sessions in a worktree under the daemon's worktrees root.
func (m *Manager) passiveCandidates() []passiveCandidate {
	var out []passiveCandidate
	seen := map[string]bool{}
	add := func(workspace, id string) {
		abs, err := filepath.Abs(workspace)
		if err != nil || id == "" || filepath.Base(id) != id {
			return
		}
		// One entry per physical directory, however it is reached.
		dir := sessionKey(filepath.Join(abs, ".ycc", "sessions", id))
		if seen[dir] || nonEmptyLog(filepath.Join(dir, "events.jsonl")) != nil {
			return
		}
		if info, err := os.Stat(jobshim.JobsDir(dir)); err != nil || !info.IsDir() {
			return
		}
		seen[dir] = true
		out = append(out, passiveCandidate{id: id, workspace: abs})
	}
	if m.projects != nil {
		for _, p := range m.projects.List() {
			entries, err := os.ReadDir(filepath.Join(p.Path, ".ycc", "sessions"))
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					add(p.Path, e.Name())
				}
			}
		}
	}
	if m.workstreams != nil {
		for _, w := range m.workstreams.List() {
			if w.WorktreePath == "" || workstream.VerifyUnderRoot(m.worktreesRoot, w.WorktreePath) != nil {
				continue
			}
			// Every session directory there: an integration session a crash
			// left before its id was recorded included.
			for _, id := range m.workstreamSessionIDs(w) {
				add(w.WorktreePath, id)
			}
		}
	}
	return out
}

// supervisePassiveSession holds one closed session's supervised jobs, under
// its preparation lock so a concurrent reopen either takes them over after
// or reconciles them itself first. A live session is left to itself. It
// returns the handles of the shells it asked to stop.
func (m *Manager) supervisePassiveSession(id, workspace string) []*jobshim.Handle {
	unlock := m.lockSessionID(id)
	defer unlock()
	if _, live := m.Get(id); live {
		return nil
	}
	dir := filepath.Join(workspace, ".ycc", "sessions", id)
	sup := m.jobLauncher.ForSession(dir)
	// Only the committed prefix counts, as for a reopen: a torn final record
	// (a handoff, say) never happened.
	events, err := event.ReadLog(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		// Fail closed: hold every job that may still run as the coordinator's,
		// so it guards its worktree without being stopped on a guess.
		log.Printf("passive job supervision: session %s: %v; holding its jobs unread", id, err)
		return m.passiveJobs.Supervise(sup, unreadJobEntries(sup), "coordinator", m.passiveWorkspace(id, workspace))
	}
	return m.supervisePassively(id, sup, workspace, events)
}

// unreadJobEntries stands in for the log of a session whose log cannot be
// read: every job directory, as a supervised coordinator shell.
func unreadJobEntries(sup *jobshim.Session) []jobs.Restored {
	dirs, _ := os.ReadDir(jobshim.JobsDir(sup.Dir()))
	var out []jobs.Restored
	for _, d := range dirs {
		if d.IsDir() {
			out = append(out, jobs.Restored{ID: d.Name(), Kind: "bash", Owner: "coordinator", Status: jobs.Running, Supervised: true})
		}
	}
	return out
}

// passiveWorkspace is the closed session's worktree, registered for shell
// overlap under its own attribution scope.
func (m *Manager) passiveWorkspace(id, workspace string) *tools.Workspace {
	return &tools.Workspace{Root: workspace, Ownership: m.ownership,
		MutationToken: m.ownership.NewScopedToken(id, "session "+id+" passive job supervision")}
}

// supervisePassively holds the supervised jobs events record for the closed
// session id in workspace. The caller holds id's preparation lock.
func (m *Manager) supervisePassively(id string, sup *jobshim.Session, workspace string, events []event.Event) []*jobshim.Handle {
	if sup == nil {
		return nil
	}
	return m.passiveJobs.Supervise(sup, engine.RestoreJobEntries(events), "coordinator", m.passiveWorkspace(id, workspace))
}

// passiveUnsettled reports why a closed session's passively held job may
// still run in workspace, or "".
func (m *Manager) passiveUnsettled(workspace, id string) string {
	return m.passiveJobs.Unsettled(sessionKey(filepath.Join(workspace, ".ycc", "sessions", id)))
}

// sessionKey is a session directory in the passive table's form, as
// jobshim.Launcher.ForSession takes it.
func sessionKey(dir string) string { return jobshim.CanonicalSessionDir(dir) }
