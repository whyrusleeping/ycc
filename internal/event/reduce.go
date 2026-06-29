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
	// StatusStopped is a session hard-terminated via StopSession (spec §12): its
	// agent loop is cancelled and its log closed; it cannot resume.
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
			p.Status = StatusStopped
		case SessionReopened:
			// Purely informational: marks that the session was re-instantiated on
			// its existing log. It does NOT change status — the reopened session's
			// status is whatever its prior events established (it resumes idle,
			// awaiting the first new input).
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
