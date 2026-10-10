package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// Automatic memory grooming. Agents that
// hit the memory budget are mid-task and badly placed to groom, so the daemon
// schedules a dedicated unattended memory-groom session instead. It uses the
// memory-groom preset binding (else the default coordinator) — deliberately not
// a cheaper model — and is bounded by one-in-flight per project, a persisted
// cooldown, and a regrowth threshold so a project that legitimately needs more
// than the soft budget is not re-groomed on every session start.
const (
	memoryGroomPreset = "memory-groom"
	// memoryGroomCooldown is the minimum time between automatic grooms of one
	// project.
	memoryGroomCooldown = 6 * time.Hour
	// memoryGroomRegrowth is how many active bytes memory must grow past the last
	// automatic groom's result before another is worthwhile.
	memoryGroomRegrowth = 1024
	// memoryGroomTimeout bounds one automatic groom session.
	memoryGroomTimeout = 2 * time.Hour
	// memoryGroomStateFile persists scheduling state per primary tree.
	memoryGroomStateFile = "memory-groom.json"
)

// MemoryGroomState is the persisted record of a project's latest automatic
// memory groom.
type MemoryGroomState struct {
	LastSession  string    `json:"last_session,omitempty"`
	LastStarted  time.Time `json:"last_started,omitempty"`
	LastFinished time.Time `json:"last_finished,omitempty"`
	ActiveBefore int       `json:"active_before,omitempty"`
	// ActiveAfter is the active size measured when the groom ended; 0 when it
	// has not finished (or was interrupted before measuring).
	ActiveAfter int    `json:"active_after,omitempty"`
	Outcome     string `json:"outcome,omitempty"` // finished | error | stopped | timed out | interrupted
}

func memoryGroomStatePath(primary string) string {
	return filepath.Join(primary, ".ycc", memoryGroomStateFile)
}

func loadMemoryGroomState(primary string) MemoryGroomState {
	var st MemoryGroomState
	if data, err := os.ReadFile(memoryGroomStatePath(primary)); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func saveMemoryGroomState(primary string, st MemoryGroomState) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	path := memoryGroomStatePath(primary)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// MemoryGroomInfo reports a project's automatic-groom status for clients.
type MemoryGroomInfo struct {
	Enabled bool
	// RunningSession is the live automatic groom session id, or "".
	RunningSession string
	Last           MemoryGroomState
}

// MemoryGroomInfo returns automatic-groom status for a registered project.
func (m *Manager) MemoryGroomInfo(project string) (MemoryGroomInfo, error) {
	absWS, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return MemoryGroomInfo{}, err
	}
	primary := m.primaryTreeFor(filepath.Clean(absWS))
	m.groomMu.Lock()
	running := m.grooms[primary]
	m.groomMu.Unlock()
	if running == groomStarting {
		running = ""
	}
	return MemoryGroomInfo{Enabled: m.reg.MemoryAutoGroom(), RunningSession: running, Last: loadMemoryGroomState(primary)}, nil
}

// memoryPressure is the Deps.MemoryPressure hook (and the session-start check):
// when the project's active memory is over its soft budget it schedules an
// automatic groom if one is due. It returns a status sentence for the calling
// agent, or "" when automatic grooming is disabled, not needed, or the caller is
// the groom session itself (which should see the plain budget figures).
func (m *Manager) memoryPressure(absWS, callerID string) string {
	if !m.reg.MemoryAutoGroom() {
		return ""
	}
	absWS = filepath.Clean(absWS)
	primary := m.primaryTreeFor(absWS)
	if primary != absWS {
		// A linked worktree carries its own memory.md copy that merges back on
		// integration; grooming the primary would not relieve it (and would make
		// memory.md conflicts likelier), so worktree writers get the store's own
		// retire/merge advice instead.
		return ""
	}

	m.groomMu.Lock()
	if m.groomStop {
		m.groomMu.Unlock()
		return ""
	}
	if id, running := m.grooms[primary]; running {
		m.groomMu.Unlock()
		if id == callerID {
			return ""
		}
		if id == groomStarting {
			return "an automatic memory-groom session is starting to consolidate project memory in the background — no action needed; continue your task"
		}
		return fmt.Sprintf("an automatic memory-groom session (%s) is already consolidating project memory in the background — no action needed; continue your task", id)
	}
	status, err := docs.NewStore(primary).MemoryStatus()
	if err != nil || !status.OverSoftBudget() {
		m.groomMu.Unlock()
		return ""
	}
	last := loadMemoryGroomState(primary)
	now := time.Now()
	if !last.LastStarted.IsZero() && now.Sub(last.LastStarted) < memoryGroomCooldown {
		m.groomMu.Unlock()
		return fmt.Sprintf("an automatic memory groom already ran recently (%s, %s); the daemon will groom again after %s — no action needed; continue your task",
			last.LastSession, last.LastStarted.Format(time.RFC3339), last.LastStarted.Add(memoryGroomCooldown).Format(time.RFC3339))
	}
	// Only a groom that actually finished establishes a baseline worth waiting to
	// outgrow: a failed/interrupted one says nothing about what memory needs. At
	// the backstop, writes are refused and memory cannot regrow, so never wait there.
	if last.Outcome == "finished" && last.ActiveAfter > 0 && status.ActiveBytes < docs.MemoryHardBudget &&
		status.ActiveBytes < last.ActiveAfter+memoryGroomRegrowth {
		m.groomMu.Unlock()
		return fmt.Sprintf("the last automatic memory groom (%s) left %d active bytes and memory has not grown much since; "+
			"the daemon will groom again once it grows by %d bytes — no action needed unless a note you rely on is wrong",
			last.LastSession, last.ActiveAfter, memoryGroomRegrowth)
	}
	// Reserve the slot and the watcher's WaitGroup count, then launch unlocked so
	// status reads and other writers never wait on session construction.
	// ReclaimAll waits on groomWG, so a launch racing shutdown is still joined.
	m.grooms[primary] = groomStarting
	m.groomWG.Add(1)
	m.groomMu.Unlock()

	start := m.startMemoryGroom
	if start == nil {
		start = m.startMemoryGroomSession
	}
	s, err := start(primary, status)

	m.groomMu.Lock()
	if err != nil {
		delete(m.grooms, primary)
		m.groomMu.Unlock()
		m.groomWG.Done()
		return ""
	}
	m.grooms[primary] = s.ID
	m.groomMu.Unlock()
	st := MemoryGroomState{LastSession: s.ID, LastStarted: now, ActiveBefore: status.ActiveBytes}
	saveMemoryGroomState(primary, st)
	// The watcher observes groomCtx: if shutdown began during the launch it
	// reclaims the new session straight away.
	go m.watchMemoryGroom(primary, s, st)
	return fmt.Sprintf("the daemon started an automatic memory-groom session (%s) to consolidate it in the background — no action needed; continue your task", s.ID)
}

// groomStarting marks a groom slot reserved while its session is being built.
const groomStarting = "(starting)"

// startMemoryGroomSession starts the real unattended groom in the primary tree.
// The preset field routes model selection through roles.presets["memory-groom"],
// falling back to the configured default coordinator.
func (m *Manager) startMemoryGroomSession(primary string, status docs.MemoryStatus) (*Session, error) {
	return m.start(Config{
		Workspace:  primary,
		Mode:       "pm",
		Unattended: true,
		Origin:     OriginMemoryGroom,
		Preset:     memoryGroomPreset,
		Prompt:     orchestrator.MemoryAutoGroomPrompt(status.ActiveBytes, status.ActiveNotes),
	}, false)
}

// watchMemoryGroom waits for an automatic groom to finish, reclaims it (its log
// stays reopenable from history), and records the outcome. It is joined by
// ReclaimAll via groomWG.
func (m *Manager) watchMemoryGroom(primary string, s *Session, st MemoryGroomState) {
	defer m.groomWG.Done()
	outcome := m.awaitMemoryGroom(s)

	st.LastFinished = time.Now()
	st.Outcome = outcome
	if status, err := docs.NewStore(primary).MemoryStatus(); err == nil {
		st.ActiveAfter = status.ActiveBytes
	}
	saveMemoryGroomState(primary, st)

	m.groomMu.Lock()
	if m.grooms[primary] == s.ID {
		delete(m.grooms, primary)
	}
	m.groomMu.Unlock()
}

func (m *Manager) awaitMemoryGroom(s *Session) string {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(memoryGroomTimeout)
	defer timer.Stop()
	for {
		status, awaitingJobs := s.StatusWithJobContinuation()
		switch status {
		case event.StatusIdle, event.StatusError, event.StatusStopped:
			if awaitingJobs {
				break
			}
			m.reclaimIfCurrent(s)
			switch status {
			case event.StatusError:
				return "error"
			case event.StatusStopped:
				return "stopped"
			}
			return "finished"
		}
		if live, ok := m.Get(s.ID); !ok || live != s {
			return "stopped"
		}
		select {
		case <-m.groomCtx.Done():
			m.reclaimIfCurrent(s)
			return "interrupted"
		case <-timer.C:
			if live, ok := m.Get(s.ID); ok && live == s {
				_ = m.Stop(s.ID)
			}
			return "timed out"
		case <-ticker.C:
		}
	}
}
