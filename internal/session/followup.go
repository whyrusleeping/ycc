package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type sessionFollowUp struct {
	FlaggedAt time.Time `json:"flagged_at"`
}

type sessionFollowUps struct {
	Sessions map[string]sessionFollowUp `json:"sessions"`
}

func loadSessionFollowUps(workspace string) (sessionFollowUps, error) {
	var out sessionFollowUps
	data, err := os.ReadFile(filepath.Join(workspace, ".ycc", "session-follow-ups.json"))
	if errors.Is(err, os.ErrNotExist) {
		out.Sessions = make(map[string]sessionFollowUp)
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("read session follow-ups: %w", err)
	}
	if out.Sessions == nil {
		return out, fmt.Errorf("read session follow-ups: missing sessions map")
	}
	for id, entry := range out.Sessions {
		if entry.FlaggedAt.IsZero() {
			return out, fmt.Errorf("read session follow-ups: missing flagged_at for %q", id)
		}
	}
	return out, nil
}

// SetSessionFollowUp changes a user-owned bookmark, not the session's agent
// status. Repeated sets preserve the timestamp; only an explicit clear removes it.
func (m *Manager) SetSessionFollowUp(project, id string, flagged bool) (time.Time, error) {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return time.Time{}, fmt.Errorf("%w %q", ErrUnknownSession, id)
	}
	workspace, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return time.Time{}, err
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return time.Time{}, err
	}
	if live, ok := m.Get(id); ok {
		if m.primaryTreeFor(live.Workspace) != workspace {
			return time.Time{}, fmt.Errorf("%w %q", ErrUnknownSession, id)
		}
	} else if _, _, err := m.SessionLogPath(project, id); err != nil {
		return time.Time{}, err
	}

	m.followUpMu.Lock()
	defer m.followUpMu.Unlock()
	entries, err := loadSessionFollowUps(workspace)
	if err != nil {
		return time.Time{}, err
	}
	entry, exists := entries.Sessions[id]
	if exists == flagged {
		return entry.FlaggedAt, nil
	}
	if flagged {
		entry.FlaggedAt = time.Now().UTC().Truncate(time.Millisecond)
		entries.Sessions[id] = entry
	} else {
		delete(entries.Sessions, id)
		entry = sessionFollowUp{}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return time.Time{}, err
	}
	dir := filepath.Join(workspace, ".ycc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return time.Time{}, err
	}
	f, err := os.CreateTemp(dir, ".session-follow-ups-*")
	if err != nil {
		return time.Time{}, err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return time.Time{}, writeErr
	}
	if closeErr != nil {
		return time.Time{}, closeErr
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "session-follow-ups.json")); err != nil {
		return time.Time{}, err
	}
	return entry.FlaggedAt, nil
}
