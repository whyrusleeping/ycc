package event

// Status is a session's lifecycle state derived from its event log.
type Status string

const (
	StatusRunning Status = "running"
	StatusIdle    Status = "idle"
	StatusError   Status = "error"
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
}

// Reduce folds an event slice into a Projection.
func Reduce(events []Event) Projection {
	var p Projection
	for _, ev := range events {
		p.LastSeq = ev.Seq
		switch ev.Type {
		case SessionStarted:
			p.Status = StatusRunning
			p.Mode = str(ev.Data, "mode")
			p.InteractionLevel = str(ev.Data, "interaction_level")
			p.Workspace = str(ev.Data, "workspace")
		case ModelTurn:
			p.Turns++
		case ToolCall:
			p.ToolCalls++
		case SessionIdle:
			p.Status = StatusIdle
			if r := str(ev.Data, "report"); r != "" {
				p.LastReport = r
			}
		case SessionError:
			p.Status = StatusError
			p.LastError = str(ev.Data, "msg")
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
