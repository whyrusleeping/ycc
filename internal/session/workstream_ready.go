package session

import (
	"fmt"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/workstream"
)

// deriveWorkstreamReadiness implements the completion gate from
// docs/design/workstream-integration.md §3. A blocked run is never ready even
// when its branch happens to contain commits.
func deriveWorkstreamReadiness(ws workstream.Workstream, sessStatus event.Status, blocked bool, commitCount int, taskStatus docs.Status, taskErr error) (workstream.Status, string) {
	if sessStatus == event.StatusError {
		return workstream.StatusNeedsAttention, "session ended in error"
	}
	if blocked {
		return workstream.StatusNeedsAttention, "session ended blocked"
	}
	if commitCount == 0 {
		return workstream.StatusNeedsAttention, "no commits since base"
	}
	if ws.TaskID != "" {
		if taskErr != nil {
			return workstream.StatusNeedsAttention, fmt.Sprintf("task %s could not be read: %v", ws.TaskID, taskErr)
		}
		if taskStatus != docs.StatusInReview && taskStatus != docs.StatusDone {
			return workstream.StatusNeedsAttention, fmt.Sprintf("task %s is %s, want in_review or done", ws.TaskID, taskStatus)
		}
	}
	return workstream.StatusReady, ""
}

// evaluateWorkstreamReadiness re-fetches and CAS-transitions a workstream so a
// concurrent merge, discard, or stale reconciliation always wins.
func (m *Manager) evaluateWorkstreamReadiness(wsID string, sessStatus event.Status, blocked bool) {
	ws, ok := m.workstreams.Get(wsID)
	if !ok || !ws.Status.InFlight() {
		return
	}
	commits := 0
	if repo, err := m.primaryRepo(ws); err == nil {
		commits = commitCount(repo, ws)
	}
	var taskStatus docs.Status
	var taskErr error
	if ws.TaskID != "" {
		var task *docs.Task
		task, taskErr = docs.NewStore(ws.WorktreePath).Get(ws.TaskID)
		if taskErr == nil {
			taskStatus = task.Status
		}
	}
	to, reason := deriveWorkstreamReadiness(ws, sessStatus, blocked, commits, taskStatus, taskErr)
	changed, err := m.workstreams.Transition(ws.ID, to, reason,
		workstream.StatusActive, workstream.StatusReady, workstream.StatusNeedsAttention)
	if err != nil || !changed {
		return
	}
	if to == workstream.StatusReady {
		m.emitWorkstreamEvent(ws, event.WorkstreamReady, map[string]any{
			"workstream": ws.ID,
			"branch":     ws.Branch,
			"commits":    commits,
			"task":       ws.TaskID,
		})
		return
	}
	m.emitWorkstreamEvent(ws, event.WorkstreamNeedsAttention, map[string]any{
		"workstream": ws.ID,
		"branch":     ws.Branch,
		"reason":     reason,
	})
}

// startWorkstreamWatcher maps terminal session events into durable workstream
// completion state. Activity after completion returns the stream to active.
func (m *Manager) startWorkstreamWatcher(ws workstream.Workstream, log *event.Log) {
	done := make(chan struct{})
	m.mu.Lock()
	if _, watching := m.workstreamWatches[ws.SessionID]; watching {
		m.mu.Unlock()
		return
	}
	m.workstreamWatches[ws.SessionID] = done
	m.mu.Unlock()

	ch, cancel := log.Subscribe(log.LastSeq())
	go func() {
		defer func() {
			cancel()
			m.mu.Lock()
			if m.workstreamWatches[ws.SessionID] == done {
				delete(m.workstreamWatches, ws.SessionID)
			}
			close(done)
			m.mu.Unlock()
		}()
		blocked := false
		for ev := range ch {
			if ev.Transient {
				continue
			}
			switch ev.Type {
			case event.SubagentFinished:
				blocked = updateWorkstreamBlocked(blocked, ev)
			case event.SessionIdle:
				m.evaluateWorkstreamReadiness(ws.ID, event.StatusIdle, blocked)
			case event.SessionError:
				m.evaluateWorkstreamReadiness(ws.ID, event.StatusError, blocked)
			case event.SessionStopped:
				// Re-reduce the log so an error immediately followed by Stop remains
				// an errored completion rather than being downgraded to stopped.
				m.evaluateWorkstreamReadiness(ws.ID, workstreamTerminalStatus(log.Snapshot()), blocked)
			case event.UserInput, event.UserInputDelivered, event.Resumed:
				blocked = false
				_, _ = m.workstreams.Transition(ws.ID, workstream.StatusActive, "",
					workstream.StatusReady, workstream.StatusNeedsAttention)
			case event.ModelTurn:
				_, _ = m.workstreams.Transition(ws.ID, workstream.StatusActive, "",
					workstream.StatusReady, workstream.StatusNeedsAttention)
			}
		}
	}()
}

// workstreamTerminalStatus preserves an error immediately followed by the
// informational session_stopped marker; Reduce correctly displays stopped, but
// readiness must retain the errored outcome.
func workstreamTerminalStatus(events []event.Event) event.Status {
	status := event.Reduce(events).Status
	if status != event.StatusStopped {
		return status
	}
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Type {
		case event.SessionError:
			return event.StatusError
		case event.SessionIdle:
			return event.StatusIdle
		case event.UserInput, event.UserInputDelivered, event.Resumed, event.ModelTurn:
			return event.StatusStopped
		}
	}
	return event.StatusStopped
}

// workstreamRunBlocked reports whether the terminal run was preceded by a
// blocked subagent result without subsequent user/model activity.
func workstreamRunBlocked(events []event.Event) bool {
	blocked := false
	for _, ev := range events {
		switch ev.Type {
		case event.SubagentFinished:
			blocked = updateWorkstreamBlocked(blocked, ev)
		case event.UserInput, event.UserInputDelivered, event.Resumed:
			blocked = false
		}
	}
	return blocked
}

// updateWorkstreamBlocked tracks the last implementer finish. Reviewer finishes
// are unrelated, and an unblocked implementer revision resolves a prior block.
func updateWorkstreamBlocked(blocked bool, ev event.Event) bool {
	if str(ev.Data, "role") != "implementer" {
		return blocked
	}
	return boolVal(ev.Data, "blocked")
}
