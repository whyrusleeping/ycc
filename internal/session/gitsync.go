package session

import (
	"os"
	"path/filepath"
	"time"

	"github.com/whyrusleeping/ycc/internal/git"
)

const (
	defaultGitSyncInterval = 3 * time.Minute
	initialGitSyncDelay    = time.Second
)

// gitFetchState is the network-dependent portion of a project's sync status.
// lastFetch records the most recent successful fetch; a failed fetch preserves
// that timestamp and marks the snapshot stale via fetchError.
type gitFetchState struct {
	lastFetch  time.Time
	fetchError string
}

// ProjectGitStatus is a workspace's fresh local git status merged with metadata
// from the background remote-ref refresh. LastFetch is zero until the first
// successful fetch.
type ProjectGitStatus struct {
	Branch      string
	HasUpstream bool
	Ahead       int
	Behind      int
	Dirty       bool
	LastFetch   time.Time
	FetchError  string
}

// SetGitSyncInterval changes the background fetch cadence. It is primarily a
// test seam; non-positive durations restore the default. The next cycle uses the
// new interval without waiting for the previous timer to expire.
func (m *Manager) SetGitSyncInterval(interval time.Duration) {
	if interval <= 0 {
		interval = defaultGitSyncInterval
	}
	m.gitSyncMu.Lock()
	m.gitSyncInterval = interval
	m.gitSyncMu.Unlock()
	select {
	case m.gitSyncWake <- struct{}{}:
	default:
	}
}

func (m *Manager) currentGitSyncInterval() time.Duration {
	m.gitSyncMu.RLock()
	defer m.gitSyncMu.RUnlock()
	return m.gitSyncInterval
}

// runGitSyncPoller is the only path that performs networked git operations. It
// is manager-owned and joined by ReclaimAll so teardown cannot race a worktree.
func (m *Manager) runGitSyncPoller() {
	defer m.gitSyncWG.Done()
	timer := time.NewTimer(initialGitSyncDelay)
	defer timer.Stop()

	for {
		select {
		case <-m.gitSyncCtx.Done():
			return
		case <-m.gitSyncWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(m.currentGitSyncInterval())
		case <-timer.C:
			m.refreshProjectGitRefs()
			timer.Reset(m.currentGitSyncInterval())
		}
	}
}

// refreshProjectGitRefs fetches each registered checkout serially and records
// success/failure independently. Non-repositories are intentionally invisible.
func (m *Manager) refreshProjectGitRefs() {
	for _, p := range m.projects.List() {
		if !isGitWorkspace(p.Path) {
			continue
		}
		err := (&git.Repo{Dir: p.Path}).Fetch()
		m.gitSyncMu.Lock()
		state := m.gitSyncCache[p.Path]
		if err != nil {
			state.fetchError = err.Error()
		} else {
			state.lastFetch = time.Now()
			state.fetchError = ""
		}
		m.gitSyncCache[p.Path] = state
		m.gitSyncMu.Unlock()
	}
}

// ProjectGitStatus computes the cheap local portion inline, so dirty and
// ahead/behind reflect the checkout at ListProjects time. It never fetches or
// otherwise contacts a remote. Non-repository paths return nil.
func (m *Manager) ProjectGitStatus(path string) *ProjectGitStatus {
	if !isGitWorkspace(path) {
		return nil
	}
	status, err := (&git.Repo{Dir: path}).Status()
	if err != nil {
		return nil
	}
	m.gitSyncMu.RLock()
	fetch := m.gitSyncCache[path]
	m.gitSyncMu.RUnlock()
	return &ProjectGitStatus{
		Branch:      status.Branch,
		HasUpstream: status.HasUpstream,
		Ahead:       status.Ahead,
		Behind:      status.Behind,
		Dirty:       status.Dirty,
		LastFetch:   fetch.lastFetch,
		FetchError:  fetch.fetchError,
	}
}

func isGitWorkspace(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}
