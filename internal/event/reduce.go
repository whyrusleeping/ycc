package event

// Status is a session's lifecycle state derived from its event log.
type Status string

const (
	StatusRunning Status = "running"
	StatusIdle    Status = "idle"
	StatusError   Status = "error"
	// StatusPaused is a running session gracefully paused at a steer checkpoint
	// (spec §18.7); a Resume (or steered SendInput) returns it to running.
	StatusPaused Status = "paused"
	// StatusStopped marks a session whose live process was terminated via
	// StopSession (spec §12): its agent loop was cancelled and its log closed.
	// This is purely a display status for the on-disk history — the session is
	// still reopenable (resume = log replay, §18.6); it is not a resume barrier.
	StatusStopped Status = "stopped"
)

// Projection is the reduced view of a session's event log (spec §5: UI state is
// a projection over the log). It is rebuilt by replaying events and is the
// canonical source for things like "what mode is this, is it idle, last report".
type Projection struct {
	Mode             string
	InteractionLevel string
	Workspace        string
	Status           Status
	Turns            int
	ToolCalls        int
	LastReport       string
	LastError        string
	LastSeq          int
	// FocusTask is the backlog task the session is currently working on, set by
	// the most recent task_focus event ("" before any focus). TurnsByTask counts
	// model_turns attributed to each focused task (spec §20.2); the empty-string
	// key holds turns that occurred before any focus ("unattributed").
	FocusTask   string
	TurnsByTask map[string]int
	// Parallel-workstream projection (docs/design/parallel-workstreams.md §6, §8):
	// the workstream lifecycle folded from its own session stream. WorkstreamID is
	// set once created; WorkstreamState is one of created/merged/conflict/discarded;
	// WorkstreamConflicts lists the conflicted paths from the most recent conflict
	// (cleared on a subsequent merge/discard).
	WorkstreamID        string
	WorkstreamState     string
	WorkstreamConflicts []string
}

// Reduce folds an event slice into a Projection.
func Reduce(events []Event) Projection {
	p := Projection{TurnsByTask: map[string]int{}}
	for _, ev := range events {
		p.LastSeq = ev.Seq
		switch ev.Type {
		case SessionStarted:
			p.Status = StatusRunning
			p.Mode = str(ev.Data, "mode")
			p.InteractionLevel = str(ev.Data, "interaction_level")
			p.Workspace = str(ev.Data, "workspace")
		case TaskFocus:
			p.FocusTask = str(ev.Data, "task")
		case ModelTurn:
			if p.Status == StatusError {
				p.Status = StatusRunning
			}
			p.Turns++
			p.TurnsByTask[p.FocusTask]++
		case ToolCall:
			if p.Status == StatusError {
				p.Status = StatusRunning
			}
			p.ToolCalls++
		case UserInput:
			if p.Status == StatusError {
				p.Status = StatusRunning
			}
		case UserInputDelivered:
			// A queued mid-run input entering the conversation counts as real
			// activity: clear a latched error status like UserInput does.
			if p.Status == StatusError {
				p.Status = StatusRunning
			}
		case SessionIdle:
			p.Status = StatusIdle
			if r := str(ev.Data, "report"); r != "" {
				p.LastReport = r
			}
		case SessionError:
			p.Status = StatusError
			p.LastError = str(ev.Data, "msg")
		case Interrupted:
			p.Status = StatusPaused
		case Resumed:
			p.Status = StatusRunning
		case SessionStopped:
			// Informational: the live process was terminated. Recorded as a
			// display status only; the session remains reopenable via log replay.
			p.Status = StatusStopped
		case SessionReopened:
			// Purely informational: marks that the session was re-instantiated on
			// its existing log. It does NOT change status — the reopened session's
			// status is whatever its prior events established (it resumes idle,
			// awaiting the first new input).
		case InteractionLevelChanged:
			// A mid-session settings overlay (spec §18.2): keep the projection's
			// interaction level current so consumers (e.g. the merge accept gate,
			// design §6) read the effective level of a non-live session.
			if to := str(ev.Data, "to"); to != "" {
				p.InteractionLevel = to
			}
		case WorkstreamCreated:
			p.WorkstreamID = str(ev.Data, "workstream")
			p.WorkstreamState = "created"
			p.WorkstreamConflicts = nil
		case WorkstreamConflict:
			p.WorkstreamState = "conflict"
			p.WorkstreamConflicts = strSlice(ev.Data, "conflicts")
		case WorkstreamMerged:
			p.WorkstreamState = "merged"
			p.WorkstreamConflicts = nil
		case WorkstreamDiscarded:
			p.WorkstreamState = "discarded"
			p.WorkstreamConflicts = nil
		}
	}
	return p
}

func str(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

// strSlice extracts a []string from event data, accepting both a freshly-emitted
// []string and a JSONL-decoded []any (where each element decodes to a string).
// Returns nil when absent or unparsable.
func strSlice(m map[string]any, k string) []string {
	if m == nil {
		return nil
	}
	switch v := m[k].(type) {
	case []string:
		if len(v) == 0 {
			return nil
		}
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return nil
}
