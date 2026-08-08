package session

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

const workLoopPersistVersion = 1

// persistedWorkLoop is the durable, versioned representation of the last work
// loop for a workspace. Runtime-only control fields (budget caps, fingerprints,
// and the session runner) are deliberately omitted: restored loops never resume.
type persistedWorkLoop struct {
	Version          int                        `json:"version"`
	LoopID           string                     `json:"loop_id"`
	Project          string                     `json:"project"`
	Workspace        string                     `json:"workspace"`
	State            string                     `json:"state"`
	CurrentSessionID string                     `json:"current_session_id,omitempty"`
	Outcome          string                     `json:"outcome,omitempty"`
	StartedAt        time.Time                  `json:"started_at"`
	Sessions         []persistedWorkLoopSession `json:"sessions,omitempty"`
	Completed        []persistedWorkLoopTask    `json:"completed,omitempty"`
	Blocked          []persistedWorkLoopTask    `json:"blocked,omitempty"`
	InReview         []persistedWorkLoopTask    `json:"in_review,omitempty"`
	Created          []persistedWorkLoopTask    `json:"created,omitempty"`
	TotalTokens      int64                      `json:"total_tokens"`
	TotalCost        float64                    `json:"total_cost"`
	CostStatus       string                     `json:"cost_status"`
}

type persistedWorkLoopSession struct {
	SessionID   string  `json:"session_id"`
	Focus       string  `json:"focus,omitempty"`
	Tokens      int64   `json:"tokens"`
	Cost        float64 `json:"cost"`
	PriceStatus string  `json:"price_status"`
}

type persistedWorkLoopTask struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	Status       string  `json:"status"`
	SHA          string  `json:"sha,omitempty"`
	VerdictTally string  `json:"verdict_tally,omitempty"`
	Tokens       int64   `json:"tokens"`
	Cost         float64 `json:"cost"`
	PriceStatus  string  `json:"price_status"`
	Reason       string  `json:"reason,omitempty"`
}

func workLoopPersistPath(workspace string) string {
	return filepath.Join(workspace, ".ycc", "workloop.json")
}

// persist snapshots and atomically records the loop. Persistence is best-effort:
// an unwritable state directory must not stop or otherwise alter a live loop.
func (wl *workLoop) persist() {
	wl.persistMu.Lock()
	defer wl.persistMu.Unlock()

	wl.persistSnapshot(wl.snapshot())
}

// persistSnapshot writes a snapshot without taking either loop mutex. It is also
// used by finish while both mutexes are held, ensuring a newly started loop cannot
// race and then be overwritten by the previous loop's final snapshot.
func (wl *workLoop) persistSnapshot(snapshot *WorkLoop) {
	if err := writePersistedWorkLoop(persistedWorkLoopFromSnapshot(snapshot)); err != nil {
		log.Printf("ycc: persist work loop %s: %v", wl.loopID, err)
	}
}

func persistedWorkLoopFromSnapshot(snapshot *WorkLoop) persistedWorkLoop {
	p := persistedWorkLoop{
		Version:          workLoopPersistVersion,
		LoopID:           snapshot.LoopID,
		Project:          snapshot.Project,
		Workspace:        snapshot.Workspace,
		State:            snapshot.State,
		CurrentSessionID: snapshot.CurrentSessionID,
		Outcome:          snapshot.Outcome,
		StartedAt:        snapshot.StartedAt,
		TotalTokens:      snapshot.TotalTokens,
		TotalCost:        snapshot.TotalCost,
		CostStatus:       snapshot.CostStatus,
	}
	for _, s := range snapshot.Sessions {
		p.Sessions = append(p.Sessions, persistedWorkLoopSession{
			SessionID: s.SessionID, Focus: s.Focus, Tokens: s.Tokens,
			Cost: s.Cost, PriceStatus: s.PriceStatus,
		})
	}
	p.Completed = persistedTasks(snapshot.Completed)
	p.Blocked = persistedTasks(snapshot.Blocked)
	p.InReview = persistedTasks(snapshot.InReview)
	p.Created = persistedTasks(snapshot.Created)
	return p
}

func persistedTasks(tasks []WorkLoopDigestTask) []persistedWorkLoopTask {
	out := make([]persistedWorkLoopTask, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, persistedWorkLoopTask{
			ID: task.ID, Title: task.Title, Status: task.Status, SHA: task.SHA,
			VerdictTally: task.VerdictTally, Tokens: task.Tokens, Cost: task.Cost,
			PriceStatus: task.PriceStatus, Reason: task.Reason,
		})
	}
	return out
}

func restoredTasks(tasks []persistedWorkLoopTask) []WorkLoopDigestTask {
	out := make([]WorkLoopDigestTask, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, WorkLoopDigestTask{
			ID: task.ID, Title: task.Title, Status: task.Status, SHA: task.SHA,
			VerdictTally: task.VerdictTally, Tokens: task.Tokens, Cost: task.Cost,
			PriceStatus: task.PriceStatus, Reason: task.Reason,
		})
	}
	return out
}

func writePersistedWorkLoop(p persistedWorkLoop) error {
	path := workLoopPersistPath(p.Workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')

	f, err := os.CreateTemp(filepath.Dir(path), ".workloop-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return fmt.Errorf("set temporary state permissions: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write temporary state: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temporary state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

// restoreWorkLoopLocked loads a workspace's last loop when it is not already in
// memory. Caller holds m.loopMu. A live persisted state means the previous daemon
// died mid-loop; it is terminated explicitly and the correction is made durable.
func (m *Manager) restoreWorkLoopLocked(workspace string) {
	p, ok := readPersistedWorkLoop(workspace)
	if !ok {
		return
	}
	wl := &workLoop{
		m:                m,
		loopID:           p.LoopID,
		project:          p.Project,
		workspace:        workspace,
		state:            p.State,
		currentSessionID: p.CurrentSessionID,
		outcome:          p.Outcome,
		startedAt:        p.StartedAt,
		cumTokens:        p.TotalTokens,
		cumCost:          p.TotalCost,
		costStatus:       p.CostStatus,
		completed:        restoredTasks(p.Completed),
		blocked:          restoredTasks(p.Blocked),
		inReview:         restoredTasks(p.InReview),
		created:          restoredTasks(p.Created),
	}
	if wl.project == "" {
		wl.project = m.projectLabel(workspace)
	}
	for _, s := range p.Sessions {
		wl.sessions = append(wl.sessions, loopSessRec{
			id: s.SessionID, focus: s.Focus, tokens: s.Tokens,
			cost: s.Cost, priceStatus: s.PriceStatus,
		})
	}

	interrupted := wl.state == "running" || wl.state == "stopping"
	if interrupted {
		wl.state = "finished"
		wl.outcome = "loop interrupted: daemon restarted"
		wl.currentSessionID = ""
	}
	m.workLoops[workspace] = wl
	if interrupted {
		wl.persist()
	}
}

// readPersistedWorkLoop treats absent, corrupt, unsupported, and semantically
// incomplete files as no prior loop. GetWorkLoop remains a state query rather
// than surfacing local persistence failures to clients.
func readPersistedWorkLoop(workspace string) (persistedWorkLoop, bool) {
	data, err := os.ReadFile(workLoopPersistPath(workspace))
	if err != nil {
		return persistedWorkLoop{}, false
	}
	var p persistedWorkLoop
	if err := json.Unmarshal(data, &p); err != nil {
		return persistedWorkLoop{}, false
	}
	if p.Version != workLoopPersistVersion || p.LoopID == "" {
		return persistedWorkLoop{}, false
	}
	switch p.State {
	case "running", "stopping", "finished":
	default:
		return persistedWorkLoop{}, false
	}
	// The location is authoritative. This also keeps a moved workspace's state
	// writable at its new path instead of following the stale JSON field.
	p.Workspace = workspace
	return p, true
}
