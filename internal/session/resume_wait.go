package session

import (
	"time"

	"github.com/whyrusleeping/gollama"

	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

// A coordinator blocked in wait when its session closed — a graceful daemon
// restart suspends it without a result, and a crash or stop can cut it off —
// leaves tool_call(wait) + wait_started with no tool_result at the end of its
// log. Reopening such a session resumes that wait on the same targets until
// its recorded deadline, records the missing tool_result, and continues the
// turn, instead of handing the model a "(no result recorded)" placeholder
// (docs/design/daemon-restart.md §4). This mirrors resume_question.go.

// resumableWait is a trailing unresolved coordinator wait found in a persisted
// log.
type resumableWait struct {
	callID string // raw recorded wait tool_call id
	index  int    // the call's position among its turn's recorded tool calls
	spec   tools.WaitSpec

	// Filled in by Reopen once the replayed history's placeholder is located.
	historyCallID string
}

// findResumableWait returns the wait the log's coordinator conversation is
// still blocked in, or nil: the last coordinator tool call is wait, with its
// wait_started and no tool_result, and nothing after it that moved the
// conversation on (a model turn, delivered input, a job notification, a
// context switch, a fork, a question). Messages queued while it waited do not
// end it (see pendingQueuedInputs); neither does a reopen that restored it and
// was cut off again.
func findResumableWait(events []event.Event) *resumableWait {
	var (
		w            *resumableWait
		lastCallID   string
		lastCallName string
		lastResolved = true
		lastIndex    int
		turnCalls    int
	)
	for _, ev := range events {
		if ev.Transient {
			continue
		}
		switch ev.Type {
		case event.ModelTurn:
			if ev.Actor != "coordinator" {
				continue
			}
			turnCalls = 0
			lastCallID, lastCallName, lastResolved = "", "", true
			w = nil
		case event.ToolCall:
			if ev.Actor != "coordinator" {
				continue
			}
			lastCallID, lastCallName, lastResolved = str(ev.Data, "id"), str(ev.Data, "name"), false
			lastIndex = turnCalls
			turnCalls++
			w = nil
		case event.ToolResult:
			if ev.Actor != "coordinator" {
				continue
			}
			if id := str(ev.Data, "id"); id == "" || id == lastCallID {
				lastResolved = true
			}
			w = nil
		case event.WaitStarted:
			if ev.Actor != "coordinator" {
				continue
			}
			w = nil
			callID, spec, ok := tools.ParseWaitStarted(ev.Data)
			if !ok || lastCallName != "wait" || lastResolved || callID != lastCallID {
				continue
			}
			w = &resumableWait{callID: callID, index: lastIndex, spec: spec}
		case event.UserInput:
			if !boolVal(ev.Data, "queued") {
				w = nil
			}
		case event.SessionError:
			// A subagent's failure (its own actor) does not end the
			// coordinator's wait.
			if ev.Actor == "" || ev.Actor == "coordinator" {
				w = nil
			}
		case event.UserInputDelivered, event.ContextViewChanged, event.SessionForked, event.SessionStarted,
			event.SessionIdle:
			w = nil
		case event.JobNotified, event.QuestionAsked, event.QuestionAnswered, event.BudgetExceeded,
			event.CoordinationNotice, event.PlanOnlyDelivered, event.RestartContext:
			w = nil
		}
	}
	return w
}

// pendingQueuedInputs returns the user messages queued during the log's
// active run that never entered the conversation, in sequence order. A run
// ends at idle input, an idle report or error (whose queued messages are
// delivered at once), a context switch, a fork, or session start. A reopen
// marker keeps only the messages on its correction queue when it was written
// (requeued_inputs): any others were dropped by that reopen.
func pendingQueuedInputs(events []event.Event) []correction {
	var queued []correction
	for _, ev := range events {
		if ev.Transient {
			continue
		}
		switch ev.Type {
		case event.UserInput:
			if !boolVal(ev.Data, "queued") {
				queued = nil
				continue
			}
			queued = append(queued, correction{
				message:        engine.UserMessage{Text: str(ev.Data, "text")},
				seq:            ev.Seq,
				recordedImages: ev.Data["images"],
			})
		case event.UserInputDelivered:
			seq := intVal(ev.Data, "seq")
			for i, c := range queued {
				if c.seq == seq {
					queued = append(queued[:i], queued[i+1:]...)
					break
				}
			}
		case event.SessionReopened:
			kept := map[int]bool{}
			for _, seq := range intList(ev.Data["requeued_inputs"]) {
				kept[seq] = true
			}
			var still []correction
			for _, c := range queued {
				if kept[c.seq] {
					still = append(still, c)
				}
			}
			queued = still
		case event.ContextViewChanged, event.SessionForked, event.SessionStarted, event.SessionIdle, event.SessionError:
			queued = nil
		}
	}
	return queued
}

func intList(v any) []int {
	var out []int
	switch l := v.(type) {
	case []int:
		out = append(out, l...)
	case []any:
		for _, x := range l {
			if f, ok := x.(float64); ok {
				out = append(out, int(f))
			}
		}
	}
	return out
}

// prepareReopenDisposition decides, before the reopened session is
// published, how its run resumes: a restored question or wait first, the
// durable context gate, wake-eligible idle (armed here, so observers never see
// a reopen awaiting its jobs as running), idle waiting for input, or straight
// on with the turn its history still owes a response to. A session that goes
// on with its run — a restored wait, or a turn cut short — gets that run's
// undelivered queued messages back on the correction queue, ahead of any new
// input, with input routing already on the correction path, so its first
// checkpoint delivers them after the results they followed.
func (s *Session) prepareReopenDisposition(events []event.Event) {
	idle, awaiting := reopenBoundary(events)
	s.reopenIdle = idle
	held := s.resumeQuestion != nil || s.forkAwaitsInput || s.contextModelSwitchRequired()
	continues := s.resumeWait != nil
	if !held && !continues {
		switch {
		case awaiting && s.deps != nil && s.deps.Jobs != nil && s.deps.Jobs.PendingContinuation("coordinator"):
			s.reopenAwaitingJobs = true
			s.setIdle(true)
		case idle || !s.currentLoop().PendingResponse():
			s.reopenParked = true
		default:
			continues = true
		}
	} else if s.forkAwaitsInput && s.resumeQuestion == nil && !s.contextModelSwitchRequired() {
		s.reopenParked = true
	}
	if !continues {
		return
	}
	pending := pendingQueuedInputs(events)
	s.steerMu.Lock()
	s.running = true
	s.corrections = append(append([]correction(nil), pending...), s.corrections...)
	s.steerMu.Unlock()
}

// restoreWait prepares the reopened session to resume rw. Called by Reopen
// before the session is published; prepareReopenDisposition then routes input
// to the correction queue, so a message ends the restored wait early as it
// would a live one.
func (s *Session) restoreWait(rw *resumableWait, history []gollama.Message) bool {
	if rw == nil || s.resumeQuestion != nil || s.deps == nil || s.deps.Jobs == nil {
		return false
	}
	rw.historyCallID = placeholderCallID(history, "wait", rw.callID, rw.index)
	if rw.historyCallID == "" {
		return false
	}
	s.resumeWait = rw
	return true
}

// awaitRestoredWait runs the restored wait to its outcome, records the wait's
// tool_result, and puts the result into the model history in place of the
// replay placeholder. It returns false when a graceful restart suspends the
// wait again (the call stays unresolved for the next reopen), or when the
// session is cancelled or its log fails.
func (s *Session) awaitRestoredWait(rw *resumableWait) bool {
	s.setStatus(event.StatusRunning)
	ws := &tools.Workspace{Root: s.deps.Workspace, Jobs: s.deps.Jobs, Emitter: s.deps.Emitter, InputWake: s.inputWake}
	start := time.Now()
	res, err := tools.ResumeWait(s.ctx, ws, rw.spec, start)
	if err != nil || s.logFailure() != nil {
		return false
	}
	// Like a live wait, a wait that collected delivers its result even when the
	// session is being stopped, so every job_claimed it recorded is matched.
	s.emitter.Emit(event.ToolResult, map[string]any{
		"name":        "wait",
		"result":      res.Content,
		"error":       res.IsError,
		"id":          rw.callID,
		"restored":    true,
		"duration_ms": time.Since(start).Milliseconds(),
	})
	if s.logFailure() != nil {
		return false
	}
	loop := s.currentLoop()
	h := loop.History()
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Role == "tool" && h[i].ToolCallID == rw.historyCallID {
			h[i].Content = res.Content
			loop.SetHistory(h)
			break
		}
	}
	return s.ctx.Err() == nil
}
