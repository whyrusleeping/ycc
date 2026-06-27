// Package event defines the session event model. Per spec §5, the event log is
// the source of truth for a session; everything else is a projection over it.
//
// Sequence numbers are assigned by the Recorder (the Log, or the spike's stdout
// recorder), NOT by Emitters — so that multiple emitters (coordinator and its
// subagents) writing to one session log share a single monotonic sequence.
package event

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Type enumerates event kinds. Kept as string constants so the on-disk JSONL
// stays human-readable and forward-compatible.
type Type string

const (
	SessionStarted Type = "session_started"
	ModeChanged    Type = "mode_changed"
	UserInput      Type = "user_input"
	ModelTurn      Type = "model_turn"
	// Thinking carries a model's reasoning summary for a turn (spec §7, §18).
	// Emitted before the corresponding ModelTurn when non-empty.
	Thinking         Type = "thinking"
	ToolCall         Type = "tool_call"
	ToolResult       Type = "tool_result"
	SessionIdle      Type = "session_idle"
	SessionError     Type = "session_error"
	Narration        Type = "log" // free-text narration line for the UI
	SubagentSpawned  Type = "subagent_spawned"
	SubagentFinished Type = "subagent_finished"
	PlanProposed     Type = "plan_proposed"
	ReviewSubmitted  Type = "review_submitted"
	DecisionMade     Type = "decision_made"
	DocUpdated       Type = "doc_updated"
	CommitMade       Type = "commit_made"
	QuestionAsked    Type = "question_asked"
	QuestionAnswered Type = "question_answered"
	// Settings overlay (spec §18.2): mid-session config changes recorded in the log.
	InteractionLevelChanged Type = "interaction_level_changed"
	RoleConfigChanged       Type = "role_config_changed"
	ThinkingLevelChanged    Type = "thinking_level_changed"
)

// Usage is the per-turn token accounting attached to a model_turn event's data
// (spec §20.1, cost tracking). It is the source of truth for usage in the JSONL
// log: every field serializes (zeros for backends that don't report usage), so a
// turn always carries a complete, attributable breakdown. Input/Output are the
// prompt/completion tokens; CacheRead/CacheWrite are the prompt-cache read and
// creation tokens (Anthropic cache_* / OpenAI prompt_tokens_details); Total is
// the backend-reported total.
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	Total      int `json:"total"`
}

// Event is a single entry in a session's log.
type Event struct {
	Seq   int            `json:"seq"`
	TS    time.Time      `json:"ts"`
	Actor string         `json:"actor"` // coordinator | implementer | reviewer:<model> | user | system
	Type  Type           `json:"type"`
	Data  map[string]any `json:"data,omitempty"`
}

// Recorder is the sequence authority for a session: it stamps an event with the
// next seq and a timestamp, durably records it, and returns the stamped event.
// Implementations must be safe for concurrent use.
type Recorder interface {
	Record(actor string, t Type, data map[string]any) Event
}

// Emitter binds a default actor to a Recorder. It is the handle the engine and
// tools hold. Multiple emitters (one per agent) can share one Recorder so their
// events interleave in a single ordered stream.
type Emitter struct {
	rec   Recorder
	actor string
}

// NewEmitter returns an Emitter that tags events with the given default actor.
func NewEmitter(rec Recorder, actor string) *Emitter {
	return &Emitter{rec: rec, actor: actor}
}

// With returns a sibling Emitter for a different actor sharing the same Recorder
// (and thus the same session sequence). Used to give a subagent its own actor.
func (e *Emitter) With(actor string) *Emitter {
	return &Emitter{rec: e.rec, actor: actor}
}

// Emit records an event with the emitter's default actor.
func (e *Emitter) Emit(t Type, data map[string]any) Event {
	return e.EmitAs(e.actor, t, data)
}

// EmitAs is like Emit but overrides the actor (e.g. a tool emitting as "user").
func (e *Emitter) EmitAs(actor string, t Type, data map[string]any) Event {
	if e.rec == nil {
		return Event{Actor: actor, Type: t, Data: data}
	}
	return e.rec.Record(actor, t, data)
}

// StdoutRecorder renders events to a writer for the M0 spike, assigning its own
// sequence. It is terse and human-facing; the JSONL Log is the real store.
type StdoutRecorder struct {
	mu  sync.Mutex
	seq int
	w   interface{ Write([]byte) (int, error) }
}

// NewStdoutRecorder returns a StdoutRecorder writing to w.
func NewStdoutRecorder(w interface{ Write([]byte) (int, error) }) *StdoutRecorder {
	return &StdoutRecorder{w: w}
}

func (s *StdoutRecorder) Record(actor string, t Type, data map[string]any) Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	ev := Event{Seq: s.seq, TS: time.Now(), Actor: actor, Type: t, Data: data}
	s.w.Write([]byte(Render(ev) + "\n"))
	return ev
}

// Render formats an event as a single terse human-readable line.
func Render(ev Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%3d] %-12s %-16s", ev.Seq, ev.Actor, ev.Type)
	switch ev.Type {
	case ToolCall:
		fmt.Fprintf(&b, " %v(%s)", ev.Data["name"], truncate(fmt.Sprint(ev.Data["args"]), 120))
	case ToolResult:
		fmt.Fprintf(&b, " %s", truncate(fmt.Sprint(ev.Data["result"]), 120))
	case ModelTurn:
		if txt, ok := ev.Data["text"].(string); ok && txt != "" {
			fmt.Fprintf(&b, " %s", truncate(txt, 200))
		}
		if tok := usageTotal(ev.Data["usage"]); tok > 0 {
			fmt.Fprintf(&b, " (%d tok)", tok)
		}
	default:
		for _, k := range []string{"text", "report", "msg", "plan", "summary", "role", "sha"} {
			if v, ok := ev.Data[k].(string); ok && v != "" {
				fmt.Fprintf(&b, " %s", truncate(v, 200))
				break
			}
		}
	}
	return b.String()
}

// usageTotal extracts the total token count from a model_turn event's "usage"
// field for terse rendering. It accepts both a freshly-emitted Usage value and a
// JSONL-decoded map (where numbers come back as float64), returning 0 when usage
// is absent or unparsable so rendering degrades gracefully.
func usageTotal(v any) int {
	switch u := v.(type) {
	case Usage:
		return u.Total
	case *Usage:
		if u != nil {
			return u.Total
		}
	case map[string]any:
		switch t := u["total"].(type) {
		case float64:
			return int(t)
		case int:
			return t
		}
	}
	return 0
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
