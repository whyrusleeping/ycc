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
	// UserInputDelivered marks the safe checkpoint at which a queued mid-run
	// user_input actually entered the conversation (steer-by-default, spec §18.7).
	// A user_input echoed while a run was in flight carries queued:true and is not
	// yet part of the model's view; the matching UserInputDelivered event
	// (data: { seq: <queued echo seq>, text }) records where it was appended, so
	// replay reconstructs the conversation at the real delivery position and the
	// TUI can render the echo as "queued" until it is delivered.
	UserInputDelivered Type = "user_input_delivered"
	ModelTurn          Type = "model_turn"
	// Thinking carries a model's reasoning summary for a turn (spec §7, §18).
	// Emitted before the corresponding ModelTurn when non-empty.
	Thinking     Type = "thinking"
	ToolCall     Type = "tool_call"
	ToolResult   Type = "tool_result"
	SessionIdle  Type = "session_idle"
	SessionError Type = "session_error"
	// SessionStopped is an informational marker that a session's live process
	// was terminated via StopSession (spec §12): its agent loop was cancelled and
	// its log closed. It does NOT prevent resume — reopening replays the durable
	// log (§18.6) regardless of this marker.
	SessionStopped   Type = "session_stopped"
	Narration        Type = "log" // free-text narration line for the UI
	SubagentSpawned  Type = "subagent_spawned"
	SubagentFinished Type = "subagent_finished"
	// Background jobs (docs/design/async-jobs.md §4). JobStarted marks a job
	// entering the registry (data: { id, kind, label }); JobFinished marks it
	// reaching a terminal state (data: { id, kind, label, status, tail }). Both
	// are tagged with the owning actor. JobNotified records a finished-job final
	// report injected into the conversation at a Steer checkpoint as a user-role
	// message (data: { id, kind, label, status, text }); recording it — like a
	// steer correction — lets reopen replay the identical history.
	JobStarted   Type = "job_started"
	JobFinished  Type = "job_finished"
	JobNotified  Type = "job_notified"
	PlanProposed Type = "plan_proposed"
	// ReviewTierSelected records which review tier the coordinator chose for a
	// change (spec §13), so tier selection is auditable in the work log/events.
	ReviewTierSelected Type = "review_tier_selected"
	ReviewSubmitted    Type = "review_submitted"
	DecisionMade       Type = "decision_made"
	DocUpdated         Type = "doc_updated"
	CommitMade         Type = "commit_made"
	QuestionAsked      Type = "question_asked"
	QuestionAnswered   Type = "question_answered"
	// TaskFocus durably links a session to the backlog task it is working on so
	// usage can be attributed "by backlog task" (spec §20.2). Emitted when focus
	// is established (data: { task: "0007", title?: "…" }); subsequent
	// model_turns are attributed to the most recent focus by the projection.
	TaskFocus Type = "task_focus"
	// Settings overlay (spec §18.2): mid-session config changes recorded in the log.
	InteractionLevelChanged Type = "interaction_level_changed"
	RoleConfigChanged       Type = "role_config_changed"
	ThinkingLevelChanged    Type = "thinking_level_changed"
	// Interrupt & steer (spec §5.2, §18.7): a running agent is gracefully paused
	// at a safe checkpoint (Interrupted ⇒ status paused) and later continues on
	// the same loop/conversation (Resumed ⇒ status running), optionally after a
	// steered-in correction.
	Interrupted Type = "interrupted"
	Resumed     Type = "resumed"
	// SessionReopened is an informational marker emitted when a persisted session
	// is re-opened ("resume = replay", spec §4.5/§18.6): its coordinator is
	// re-instantiated on the EXISTING event log with history reconstructed from
	// the log, and new activity appends to the same continuous stream. It does not
	// change status (see Reduce).
	SessionReopened Type = "session_reopened"
	// Parallel workstreams lifecycle (docs/design/parallel-workstreams.md §6, §8).
	// These are recorded on the workstream's own session stream so the merge flow
	// is auditable in the log and projectable by the reducer.
	//
	// WorkstreamCreated marks a linked worktree + branch being spawned off a
	// project's base (data: { workstream, branch, base, worktree, project, task }).
	WorkstreamCreated Type = "workstream_created"
	// WorkstreamMerged marks a clean, review-gated integration back to base and
	// subsequent cleanup (data: { workstream, branch, commit }).
	WorkstreamMerged Type = "workstream_merged"
	// WorkstreamConflict marks a trial/real merge that conflicted; the base branch
	// is never left conflicted (data: { workstream, branch, conflicts: []string }).
	WorkstreamConflict Type = "workstream_conflict"
	// WorkstreamDiscarded marks a workstream abandoned without merging, its
	// worktree + branch cleaned up (data: { workstream, branch }).
	WorkstreamDiscarded Type = "workstream_discarded"
	// TurnDelta carries a partial chunk of a model's in-progress turn output,
	// streamed to live subscribers as a transient (non-persisted) event so the
	// UI can tail model output incrementally (spec §5, task 0114). It is only
	// ever emitted via Log.Broadcast: it carries Transient=true and Seq=0, is
	// never written to events.jsonl / the in-memory replay / transcripts, and
	// the durable ModelTurn event remains the source of truth for the turn.
	TurnDelta Type = "turn_delta"
	// Retry marks an in-progress retry of a failed LLM API call: the engine loop
	// hit a transient failure (rate limit / overload / network) and is backing
	// off before the next attempt (spec §7.2). Like TurnDelta it is transient —
	// only ever emitted via Log.Broadcast (Transient=true, Seq=0), never
	// persisted — so live UIs can show the wait while the durable log stays
	// quiet unless the turn ultimately fails (which records a session_error).
	// Data: { attempt, max_attempts, delay_ms, kind, status, msg }.
	Retry Type = "retry"
	// Budget spend guard (task 0137, spec §20.6). BudgetWarning marks a session
	// crossing ~80% of a configured cost/token cap (data: { tokens, token_cap,
	// cost, cost_cap, pct }); it fires at most once per session. BudgetExceeded
	// marks a cap being crossed (same data plus an "action" field): action
	// "continue" when an attended user confirmed continuing past the cap, or
	// "halt" when the session was told to wrap up. The halt record is emitted as
	// actor "user" and carries the wrap-up instruction in "text" so reopen replay
	// reconstructs it as a user message (like job_notified) — the graceful halt is
	// durable in the log, never a silent overrun.
	BudgetWarning  Type = "budget_warning"
	BudgetExceeded Type = "budget_exceeded"
)

// ThinkingBlock mirrors gollama.ThinkingBlock for lossless serialization in the
// event log: it lets a model_turn carry the signed/redacted reasoning blocks so
// the conversation can be replayed verbatim on reopen (Anthropic verifies these
// signatures, spec §5.1). For a normal block Thinking holds the summary text and
// Signature the verification signature; for a redacted block Redacted holds the
// opaque data payload.
type ThinkingBlock struct {
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Redacted  string `json:"data,omitempty"`
}

// Usage is the per-turn token accounting attached to a model_turn event's data
// (spec §20.1, cost tracking). It is the source of truth for usage in the JSONL
// log: every field serializes (zeros for backends that don't report usage), so a
// turn always carries a complete, attributable breakdown. The classes are
// DISJOINT: Input is the fresh (uncached) prompt tokens, Output the completion
// tokens, and CacheRead/CacheWrite the prompt-cache read and creation tokens
// (Anthropic cache_* / OpenAI prompt_tokens_details — the engine subtracts
// OpenAI's cached subset from Input at emit time so cost math never
// double-counts a class); Total is the backend-reported total. ReasoningTokens
// is observability metadata: it is a subset of Output, never an additional cost
// class, and is omitted for providers that do not report it.
type Usage struct {
	Input           int `json:"input"`
	Output          int `json:"output"`
	CacheRead       int `json:"cache_read"`
	CacheWrite      int `json:"cache_write"`
	Total           int `json:"total"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// Event is a single entry in a session's log.
type Event struct {
	Seq   int            `json:"seq"`
	TS    time.Time      `json:"ts"`
	Actor string         `json:"actor"` // coordinator | implementer | reviewer:<model> | user | system
	Type  Type           `json:"type"`
	Data  map[string]any `json:"data,omitempty"`
	// Transient marks a broadcast-only event that is delivered to live
	// subscribers but never persisted: it carries Seq=0, is never written to
	// events.jsonl, never appended to the in-memory replay slice, and is invisible
	// to Snapshot / ReadLog / late subscribers. Used for streaming UI hints such
	// as turn_delta (spec §5, task 0114). Subscribers must tolerate seq-less
	// events and must not use them to advance a resume cursor.
	Transient bool `json:"transient,omitempty"`
}

// Recorder is the sequence authority for a session: it stamps an event with the
// next seq and a timestamp, durably records it, and returns the stamped event.
// Implementations must be safe for concurrent use.
type Recorder interface {
	Record(actor string, t Type, data map[string]any) Event
}

// Broadcaster is the optional capability of a Recorder that can deliver
// transient, non-persisted events to live subscribers (spec §5, task 0114).
// *event.Log satisfies it; recorders without a live subscriber notion (e.g.
// StdoutRecorder, FuncRecorder) do not, so streaming hints degrade to no-ops.
type Broadcaster interface {
	Broadcast(actor string, t Type, data map[string]any) Event
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

// Actor returns the emitter's default actor label.
func (e *Emitter) Actor() string { return e.actor }

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

// Broadcast delivers a transient, non-persisted event tagged with the emitter's
// default actor to live subscribers, and reports whether it was delivered. It
// no-ops (returns ok=false) when the underlying Recorder is not a Broadcaster
// (e.g. StdoutRecorder, FuncRecorder, or a nil recorder), so streaming callers
// can attempt a broadcast unconditionally and fall back gracefully. Transient
// events are never persisted (see Broadcaster / Log.Broadcast).
func (e *Emitter) Broadcast(t Type, data map[string]any) (Event, bool) {
	return e.BroadcastAs(e.actor, t, data)
}

// BroadcastAs is like Broadcast but overrides the actor.
func (e *Emitter) BroadcastAs(actor string, t Type, data map[string]any) (Event, bool) {
	b, ok := e.rec.(Broadcaster)
	if !ok || b == nil {
		return Event{Actor: actor, Type: t, Data: data, Transient: true}, false
	}
	return b.Broadcast(actor, t, data), true
}

// CanBroadcast reports whether this emitter's Recorder supports transient
// broadcasts, without emitting anything. Streaming callers use it to decide
// whether to bother producing deltas at all.
func (e *Emitter) CanBroadcast() bool {
	b, ok := e.rec.(Broadcaster)
	return ok && b != nil
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

// FuncRecorder is a Recorder that stamps a monotonic seq + timestamp and hands
// each event to fn. It is used to stream a transient agent's events (e.g. the
// quick-add capture agent) live to a caller without a durable log.
type FuncRecorder struct {
	mu  sync.Mutex
	seq int
	fn  func(Event)
}

// NewFuncRecorder returns a FuncRecorder that invokes fn with each stamped event.
func NewFuncRecorder(fn func(Event)) *FuncRecorder { return &FuncRecorder{fn: fn} }

func (r *FuncRecorder) Record(actor string, t Type, data map[string]any) Event {
	r.mu.Lock()
	r.seq++
	ev := Event{Seq: r.seq, TS: time.Now(), Actor: actor, Type: t, Data: data}
	r.mu.Unlock()
	if r.fn != nil {
		r.fn(ev)
	}
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
		if ms := durationMS(ev.Data["duration_ms"]); ms > 0 {
			fmt.Fprintf(&b, " %s", fmtDur(ms))
		}
	case ModelTurn:
		if txt, ok := ev.Data["text"].(string); ok && txt != "" {
			fmt.Fprintf(&b, " %s", truncate(txt, 200))
		}
		if tok := usageTotal(ev.Data["usage"]); tok > 0 {
			fmt.Fprintf(&b, " (%d tok)", tok)
		}
		if ms := durationMS(ev.Data["duration_ms"]); ms > 0 {
			fmt.Fprintf(&b, " %s", fmtDur(ms))
		}
	default:
		for _, k := range []string{"text", "report", "msg", "plan", "summary", "role", "sha", "task"} {
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

// durationMS extracts a duration in milliseconds from an event data value. Like
// usageTotal it accepts the freshly-emitted int64/int as well as a JSONL-decoded
// float64, returning 0 when absent or unparsable.
func durationMS(v any) int64 {
	switch d := v.(type) {
	case int64:
		return d
	case int:
		return int64(d)
	case float64:
		return int64(d)
	}
	return 0
}

// fmtDur renders a millisecond duration compactly: sub-second values as "340ms",
// otherwise one-decimal seconds like "1.2s".
func fmtDur(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
