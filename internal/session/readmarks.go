package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Read marks record, per session, the daemon-clock time through which the user
// has seen it. They are shared by every client (web, iOS), so reading a session
// on one clears its unread marker on the others. Like follow-up bookmarks they
// live beside the event logs rather than in them: marking read is not session
// activity and must not invalidate the history cache.
//
// BaselineAt is when read tracking began for the workspace (the first mark).
// A session without an explicit mark is read through the baseline, so only
// activity after tracking began can make it unread.
type sessionReadMarks struct {
	BaselineAt time.Time            `json:"baseline_at"`
	Sessions   map[string]time.Time `json:"sessions"`
}

const readMarksFile = "session-read-marks.json"

func loadSessionReadMarks(workspace string) (sessionReadMarks, bool, error) {
	out := sessionReadMarks{Sessions: map[string]time.Time{}}
	data, err := os.ReadFile(filepath.Join(workspace, ".ycc", readMarksFile))
	if errors.Is(err, os.ErrNotExist) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	var parsed sessionReadMarks
	if err := json.Unmarshal(data, &parsed); err != nil {
		return out, false, fmt.Errorf("read session read marks: %w", err)
	}
	if parsed.Sessions == nil {
		parsed.Sessions = map[string]time.Time{}
	}
	return parsed, true, nil
}

// readThrough is the effective mark for a session: its explicit mark, else the
// baseline. Zero means no read state exists for the workspace yet.
func (r sessionReadMarks) readThrough(id string) time.Time {
	if t, ok := r.Sessions[id]; ok {
		return t
	}
	return r.BaselineAt
}

// SessionReadMark is one session's read-through stamp.
type SessionReadMark struct {
	SessionID   string
	ReadThrough time.Time
}

// MarkSessionsRead advances read marks for sessions in a project. Marks only
// move forward; stamps later than now are clamped to now. Unknown or invalid
// ids are skipped so one stale row cannot fail a "mark all read". It returns
// the resulting mark for each accepted session, in request order.
func (m *Manager) MarkSessionsRead(project string, marks []SessionReadMark) ([]SessionReadMark, error) {
	workspace, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return nil, err
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	accepted := make([]SessionReadMark, 0, len(marks))
	for _, mark := range marks {
		if mark.ReadThrough.IsZero() || !m.sessionInWorkspace(project, workspace, mark.SessionID) {
			continue
		}
		accepted = append(accepted, mark)
	}
	if len(accepted) == 0 {
		return nil, nil
	}

	m.readMarkMu.Lock()
	defer m.readMarkMu.Unlock()
	now := time.Now().UTC().Truncate(time.Millisecond)
	state, exists, err := loadSessionReadMarks(workspace)
	if err != nil {
		// Read marks are a convenience; a corrupt file is replaced rather than
		// leaving read state permanently unwritable.
		log.Printf("ycc: session read marks: replacing unreadable file: %v", err)
		exists = false
	}
	if !exists {
		state = sessionReadMarks{BaselineAt: now, Sessions: map[string]time.Time{}}
	}
	changed := !exists
	for i, mark := range accepted {
		t := mark.ReadThrough.UTC().Truncate(time.Millisecond)
		if t.After(now) {
			t = now
		}
		if current, ok := state.Sessions[mark.SessionID]; !ok || t.After(current) {
			state.Sessions[mark.SessionID] = t
			changed = true
		}
		accepted[i].ReadThrough = state.readThrough(mark.SessionID)
	}
	if changed {
		if err := writeSessionReadMarks(workspace, state); err != nil {
			return nil, err
		}
	}
	return accepted, nil
}

// sessionInWorkspace reports whether id names a live session of workspace or a
// persisted session of project.
func (m *Manager) sessionInWorkspace(project, workspace, id string) bool {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return false
	}
	if live, ok := m.Get(id); ok {
		return m.primaryTreeFor(live.Workspace) == workspace
	}
	_, _, err := m.SessionLogPath(project, id)
	return err == nil
}

func writeSessionReadMarks(workspace string, state sessionReadMarks) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	dir := filepath.Join(workspace, ".ycc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".session-read-marks-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(dir, readMarksFile))
}
