package session

import (
	"fmt"
	"path/filepath"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/git"
)

// WorkingChanges inspects the session's persisted scope without retaining a new
// snapshot or taking a workspace mutation lease.
func (m *Manager) WorkingChanges(project, sessionID, taskID string) (*git.Changeset, string, error) {
	projectWorkspace, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, "", err
	}
	ws, _, err := m.SessionLogPath(project, sessionID)
	if err != nil {
		return nil, "", err
	}
	if ws != projectWorkspace {
		linked, ok := m.workstreams.BySession(sessionID)
		if !ok || (project != "" && linked.Project != project) || (project == "" && len(m.Projects()) != 1) {
			return nil, "", fmt.Errorf("%w %q", ErrUnknownSession, sessionID)
		}
	}
	repo, err := git.OpenExisting(ws)
	if err != nil {
		return nil, "", fmt.Errorf("open session repository: %w", err)
	}
	baseline, err := repo.LoadBaselineReadOnly(sessionID)
	if err != nil {
		return nil, "", fmt.Errorf("session baseline: %w", err)
	}
	scope := fmt.Sprintf("session %s baseline %s", sessionID, baseline.ID)
	var adopted []string
	if taskID != "" {
		store := docs.NewStore(ws)
		// Store.Get may self-heal duplicate ids by renaming backlog files.
		// Explicitly refuse that write on an inspection request.
		store.SetRepairLease(func() (func(), error) { return nil, fmt.Errorf("read-only inspection") })
		tasks, err := store.ListMetadata()
		if err != nil {
			return nil, "", fmt.Errorf("task %s: %w", taskID, err)
		}
		var path string
		for _, task := range tasks {
			if task.ID != taskID {
				continue
			}
			if path != "" {
				return nil, "", fmt.Errorf("duplicate backlog task %s; resolve before inspection", taskID)
			}
			path = task.Path
		}
		if path == "" {
			return nil, "", fmt.Errorf("unknown backlog task %s", taskID)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, "", err
		}
		adopted = append(adopted, path)
		scope += fmt.Sprintf(", task %s doc adopted", taskID)
	}
	changes, _, err := repo.InspectChanges(baseline, adopted...)
	return changes, scope, err
}
