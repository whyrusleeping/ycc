package session

import (
	"fmt"
	"path/filepath"

	"github.com/whyrusleeping/ycc/internal/usage"
)

// UsageHours returns hourly agent activity for a project's workspace, or for
// every registered project when project is empty and several exist (the same
// scope rule as UsageReport). The returned workspace is empty for the
// all-project rollup.
func (m *Manager) UsageHours(project string, opts usage.Options) ([]usage.HourTotal, string, error) {
	var workspaces []string
	single := ""
	if projects := m.projects.List(); project == "" && len(projects) > 1 {
		seen := make(map[string]bool, len(projects))
		for _, p := range projects {
			absWS, err := filepath.Abs(p.Path)
			if err != nil {
				return nil, "", fmt.Errorf("resolve workspace for project %q: %w", p.Name, err)
			}
			if !seen[absWS] {
				seen[absWS] = true
				workspaces = append(workspaces, absWS)
			}
		}
	} else {
		ws, err := m.resolveProjectWorkspace(project)
		if err != nil {
			return nil, "", err
		}
		single, err = filepath.Abs(ws)
		if err != nil {
			return nil, "", fmt.Errorf("resolve workspace: %w", err)
		}
		workspaces = []string{single}
	}
	var entries []usage.HourEntry
	for _, ws := range workspaces {
		e, err := m.usageScans.ScanHours(ws)
		if err != nil {
			return nil, "", err
		}
		entries = append(entries, e...)
	}
	return usage.AggregateHours(entries, opts), single, nil
}
