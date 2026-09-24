package session

import (
	"os"

	"github.com/whyrusleeping/ycc/internal/workstream"
)

// FileRoot resolves the directory remote file browsing (ListFiles/ReadFile)
// is confined to. With a session id, files resolve against that session's
// workspace — which for a workstream is its linked worktree, where the agent's
// links point at files that may not exist on the base branch yet. When that
// workspace is gone (a merged/discarded workstream's reclaimed worktree) the
// project root is used instead and fellBack reports it. Without a session id,
// or for a session that simply ran in the project checkout, the root is the
// registered project path.
func (m *Manager) FileRoot(project, sessionID string) (root string, fellBack bool, err error) {
	if sessionID != "" {
		if s, ok := m.Get(sessionID); ok && s.Workspace != "" {
			if dirExists(s.Workspace) {
				return s.Workspace, false, nil
			}
			base, err := m.resolveProjectWorkspace(project)
			return base, err == nil, err
		}
	}
	base, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return "", false, err
	}
	if sessionID == "" {
		return base, false, nil
	}
	for _, w := range m.workstreams.List() {
		if w.SessionID != sessionID && w.IntegrateSessionID != sessionID {
			continue
		}
		// Persisted worktree paths are untrusted (see ReconcileWorkstreams):
		// only follow in-flight entries under the daemon's worktrees root.
		if w.Status.InFlight() && workstream.VerifyUnderRoot(m.worktreesRoot, w.WorktreePath) == nil && dirExists(w.WorktreePath) {
			return w.WorktreePath, false, nil
		}
		return base, true, nil
	}
	return base, false, nil
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
