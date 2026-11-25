package session

import (
	"fmt"
	"path/filepath"
)

// TaskHolder is the live session holding a backlog task (spec §9.1).
type TaskHolder struct {
	SessionID string
	Title     string
	Loop      bool // started by the project's work loop
}

// TaskHolders maps each task a live session in project's workspace holds to
// that session: the tasks the work loop skips and backlog views mark not
// actionable. It follows the heldTasks rule, so it does not depend on session
// coordination and ignores sessions in other worktrees.
func (m *Manager) TaskHolders(project string) (map[string]TaskHolder, error) {
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, err
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	held := m.heldTasks(absWS, "")
	out := make(map[string]TaskHolder, len(held))
	bySession := map[string]TaskHolder{}
	for id, sid := range held {
		h, ok := bySession[sid]
		if !ok {
			h = TaskHolder{SessionID: sid}
			if s, live := m.Get(sid); live {
				h.Title = deriveTitle(s.Log().Snapshot())
				h.Loop = s.origin == OriginWorkLoop
			}
			bySession[sid] = h
		}
		out[id] = h
	}
	return out, nil
}
