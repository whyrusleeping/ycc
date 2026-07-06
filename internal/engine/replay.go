package engine

// This file implements "resume = replay" (spec §4.5/§5/§18.6): reconstructing a
// coordinator loop's conversation history from a session's persisted event log so
// a finished/idle session can be re-opened and continued on the SAME log.
//
// Known lossy edge, explicitly documented as unsupported: tool-result
// images/PDFs are NOT restored on replay (only their counts are recorded on
// tool_result events). Multimodal tool-result content does not round-trip; the
// reconstructed history carries the text result only. This is an accepted
// limitation (see spec §18.6).
//
// The internal truncation-retry nudge IS reproduced on replay: when the live
// loop hits a mid-Run output-token truncation it appends a sanitized assistant
// stub plus an internal user "nudge" message, but the nudge is posted via
// Loop.Post and never recorded in the event log. ReplayHistory synthesizes that
// nudge when it detects a truncated coordinator turn immediately followed by
// another coordinator assistant turn, so the reconstructed conversation keeps
// strict user/assistant alternation (some backends reject two consecutive
// assistant turns).

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
)

// toolIDInvalid matches any character Anthropic rejects in a tool_use id. The
// native /v1/messages API requires tool_use ids (and the tool_result tool_use_id
// that references them) to match ^[a-zA-Z0-9_-]+$. Other backends (and some
// gateways/local models) emit ids with other characters — or none at all — which
// the original backend accepted but Anthropic rejects with a 400 on resume. We
// canonicalize ids on replay so a reopened session is valid regardless of which
// backend originally produced them.
var toolIDInvalid = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// ReplayHistory reconstructs the coordinator agent loop's conversation history
// from a session's events, in order: user inputs, the coordinator's assistant
// turns (with thinking blocks + tool calls), and tool results. Subagent activity
// (actor implementer/reviewer:*) and non-conversational events are ignored — the
// reconstructed history is the COORDINATOR's view, mirroring what Loop builds
// live. Dangling tool calls (a tool_call with no recorded tool_result, e.g. a
// session reopened mid-turn or closed with an ask_user pending) get a synthetic
// tool result inserted immediately after their assistant turn — wherever that
// turn sits in the transcript, not only at the tail — so the conversation stays
// valid for the next turn (Anthropic/OpenAI require every tool_use to be
// answered by the immediately following message).
func ReplayHistory(events []event.Event) []gollama.Message {
	var history []gollama.Message
	// assistantIdx is the index in history of the current coordinator assistant
	// message that subsequent coordinator tool_calls attach to (-1 if none yet).
	assistantIdx := -1
	// answered records tool_call ids that have a matching tool_result, so we can
	// repair any dangling calls at the end.
	answered := map[string]bool{}
	// lastTurnTruncated tracks whether the previous coordinator model_turn was
	// cut off at the output token cap, so we can synthesize the internal nudge
	// (see file doc comment) before the retry turn.
	lastTurnTruncated := false

	// canon maps a raw recorded tool-call id to a canonical, Anthropic-valid id.
	// Ids that are empty or contain characters outside ^[a-zA-Z0-9_-]+$ are
	// rewritten to a unique "call_N"; ids already valid are kept as-is. The map
	// keys on the raw id so repeated references to the same raw id are stable.
	idMap := map[string]string{}
	usedIDs := map[string]bool{}
	canon := func(raw string) string {
		if c, ok := idMap[raw]; ok {
			return c
		}
		c := raw
		if c == "" || toolIDInvalid.MatchString(c) || usedIDs[c] {
			c = fmt.Sprintf("call_%d", len(idMap))
			for usedIDs[c] {
				c = fmt.Sprintf("call_%d", len(idMap)+len(usedIDs))
			}
		}
		idMap[raw] = c
		usedIDs[c] = true
		return c
	}

	// pending is a FIFO queue of canonical tool-call ids awaiting a tool_result,
	// in emission order. Tool results pair to calls by id when the id was recorded,
	// otherwise POSITIONALLY: older logs (before the loop recorded an "id" on
	// tool_result events) omit it, leaving an empty tool_use_id that Anthropic
	// rejects. The live loop emits each ToolResult immediately after its ToolCall,
	// so the FIFO order is exact and lets us recover the correct pairing.
	canonByRaw := map[string]string{} // raw call id -> canonical (for id-based match)
	var pending []string
	popPending := func(id string) {
		for i, p := range pending {
			if p == id {
				pending = append(pending[:i], pending[i+1:]...)
				return
			}
		}
	}

	// repairDangling appends a synthetic tool result for every unanswered
	// tool_call on the CURRENT assistant message. It runs before any new
	// user/assistant message is appended and once more at the end of replay, so
	// a dangling tool_use is repaired in place wherever it occurs — not just at
	// the tail. This matters when a session is closed with a question pending,
	// reopened (the model re-asks, appending new turns), and closed unanswered
	// again: the original dangling tool_use is then mid-transcript, and
	// Anthropic rejects the whole conversation with a 400 unless a tool_result
	// immediately follows it.
	repairDangling := func() {
		if assistantIdx < 0 {
			return
		}
		for _, call := range history[assistantIdx].ToolCalls {
			if answered[call.ID] {
				continue
			}
			history = append(history, gollama.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "(no result recorded; session was reopened)",
			})
			answered[call.ID] = true
			popPending(call.ID)
		}
	}

	// lostJobs tracks background jobs whose start was recorded but whose finish
	// was not (docs/design/async-jobs.md §4): jobs do not survive a daemon
	// restart, so on reopen we synthesize a "(job lost: daemon restarted)" note
	// to keep histories valid. Both a recorded job_finished and a job_notified
	// (its report already injected) clear the entry.
	lostJobs := map[string]string{} // id -> label
	var lostOrder []string

	for _, ev := range events {
		switch ev.Type {
		case event.UserInput:
			// All user inputs belong to the coordinator conversation regardless of
			// actor (they are emitted as actor "user").
			//
			// A queued mid-run echo (queued:true, spec §18.7) has NOT yet entered the
			// conversation at this position: the matching user_input_delivered event
			// appends it at the real delivery point. Skip it here. A queued echo with
			// no delivery (the session was stopped mid-run before the next checkpoint)
			// is deliberately absent from replayed history — it never reached the
			// model, so the reconstructed conversation matches what the model saw.
			if boolv(ev.Data, "queued") {
				continue
			}
			repairDangling()
			history = append(history, gollama.Message{Role: "user", Content: str(ev.Data, "text")})
			assistantIdx = -1
			lastTurnTruncated = false // a real user input breaks the truncation chain
		case event.UserInputDelivered:
			// A queued mid-run input entering the conversation at its safe checkpoint
			// (spec §18.7): append it exactly where the live loop Posted it so the
			// replayed history matches what the model saw, and reset turn state like
			// a normal user input.
			repairDangling()
			history = append(history, gollama.Message{Role: "user", Content: str(ev.Data, "text")})
			assistantIdx = -1
			lastTurnTruncated = false
		case event.ModelTurn:
			if ev.Actor != "coordinator" {
				continue // subagent turn — not part of the coordinator history
			}
			// Repair any dangling tool calls on the previous assistant turn before
			// starting a new one (e.g. an ask_user that was never answered because
			// the session closed, followed on reopen by the model re-asking).
			repairDangling()
			truncated := boolv(ev.Data, "truncated")
			text := str(ev.Data, "text")
			// If the previous turn was truncated and we're about to append another
			// assistant turn (the retry), synthesize the internal user nudge the
			// live loop posts between the truncated stub and the retry. This keeps
			// strict user/assistant alternation, which some backends require.
			if lastTurnTruncated && len(history) > 0 && history[len(history)-1].Role == "assistant" {
				history = append(history, gollama.Message{Role: "user", Content: truncationNudge})
			}
			// A turn cut off at the output token cap (truncated) may carry an
			// unsigned/incomplete thinking block; drop the blocks here to match the
			// live loop's sanitized stub (which omits the cut-off block so Anthropic
			// doesn't reject it on the next request).
			var blocks []gollama.ThinkingBlock
			if !truncated {
				// Drop empty, unreplayable "thinking" blocks recorded by older
				// logs (matches the live loop's sanitize) so reopen doesn't 400.
				blocks = sanitizeThinkingBlocks(parseThinkingBlocks(ev.Data["thinking_blocks"]))
			} else if strings.TrimSpace(text) == "" {
				// Guarantee non-empty content for a truncated turn (empty assistant
				// messages are rejected by backends), matching the live sanitized stub.
				text = truncatedStubContent
			}
			history = append(history, gollama.Message{
				Role:           "assistant",
				Content:        text,
				ThinkingBlocks: blocks,
			})
			assistantIdx = len(history) - 1
			lastTurnTruncated = truncated
		case event.ToolCall:
			if ev.Actor != "coordinator" {
				continue
			}
			if assistantIdx < 0 {
				continue // orphan tool call with no preceding assistant message
			}
			rawID := str(ev.Data, "id")
			id := canon(rawID)
			canonByRaw[rawID] = id
			pending = append(pending, id)
			call := gollama.ToolCall{
				ID:   id,
				Type: "function",
				Function: gollama.ToolCallFunction{
					Name:      str(ev.Data, "name"),
					Arguments: str(ev.Data, "args"),
				},
			}
			history[assistantIdx].ToolCalls = append(history[assistantIdx].ToolCalls, call)
		case event.ToolResult:
			if ev.Actor != "coordinator" {
				continue
			}
			// Resolve the call this result answers. Prefer an id match when the
			// result recorded one and it maps to a known call; otherwise fall back
			// to the oldest unanswered call (positional FIFO), which recovers the
			// pairing for legacy logs that omitted the tool_result id.
			rawID := str(ev.Data, "id")
			var id string
			if rawID != "" {
				if c, ok := canonByRaw[rawID]; ok {
					id = c
				}
			}
			if id == "" && len(pending) > 0 {
				id = pending[0]
			}
			if id == "" {
				// Orphan result with no pending call (shouldn't happen in practice):
				// mint a valid id so the message is at least well-formed.
				id = canon(rawID)
			}
			if answered[id] {
				// The call was already answered (e.g. a synthetic repair was inserted
				// at a message boundary before this late result appeared). A second
				// tool_result for the same tool_use_id would itself be rejected by
				// the API, so drop the late one.
				continue
			}
			popPending(id)
			answered[id] = true
			history = append(history, gollama.Message{Role: "tool", ToolCallID: id, Content: str(ev.Data, "result")})
		case event.JobStarted:
			id := str(ev.Data, "id")
			if id == "" {
				continue
			}
			if _, seen := lostJobs[id]; !seen {
				lostOrder = append(lostOrder, id)
			}
			lostJobs[id] = str(ev.Data, "label")
		case event.JobFinished:
			// The job reached a terminal state before the log ended, so it is not
			// lost. (Its report was delivered either via a wait tool_result or a
			// job_notified event, both already reflected in the history.)
			delete(lostJobs, str(ev.Data, "id"))
		case event.JobNotified:
			// A finished-job final report injected at a Steer checkpoint as a
			// user-role message (docs/design/async-jobs.md §3.3): append it exactly
			// where the live loop Posted it and reset turn state like a user input.
			delete(lostJobs, str(ev.Data, "id"))
			repairDangling()
			history = append(history, gollama.Message{Role: "user", Content: str(ev.Data, "text")})
			assistantIdx = -1
			lastTurnTruncated = false
		case event.BudgetExceeded:
			// A graceful spend-guard halt (task 0137, spec §20.6) injects a wrap-up
			// instruction as a USER-actor budget_exceeded event carrying "text".
			// Reconstruct it as a user message exactly like job_notified so the
			// reopened history keeps user/assistant alternation. The "continue"
			// (confirmed) variant is emitted as a coordinator-actor event with no
			// "text" and is ignored here.
			if ev.Actor != "user" {
				continue
			}
			text := str(ev.Data, "text")
			if text == "" {
				continue
			}
			repairDangling()
			history = append(history, gollama.Message{Role: "user", Content: text})
			assistantIdx = -1
			lastTurnTruncated = false
		}
	}

	// Repair dangling tool calls on the trailing assistant message: any tool_call
	// id that never saw a matching tool_result gets a synthetic result so the
	// reconstructed conversation is valid for the next turn. (Mid-transcript
	// dangling calls were already repaired at message boundaries above.)
	repairDangling()

	// Synthesize a lost-job note for any background job that started but never
	// finished before the log ended (docs/design/async-jobs.md §4): jobs do not
	// survive a daemon restart. Delivered as a user-role message so the model
	// knows those jobs are gone. Merge into a trailing user message when present
	// so we never place two consecutive user turns.
	if len(lostOrder) > 0 {
		var parts []string
		for _, id := range lostOrder {
			if _, ok := lostJobs[id]; !ok {
				continue
			}
			label := lostJobs[id]
			note := fmt.Sprintf("[job %s lost: daemon restarted]", id)
			if strings.TrimSpace(label) != "" {
				note += " " + label
			}
			parts = append(parts, note)
		}
		if len(parts) > 0 {
			text := strings.Join(parts, "\n")
			if n := len(history); n > 0 && history[n-1].Role == "user" {
				history[n-1].Content += "\n\n" + text
			} else {
				history = append(history, gollama.Message{Role: "user", Content: text})
			}
		}
	}

	return history
}

// parseThinkingBlocks defensively decodes the thinking_blocks field from a
// model_turn event into gollama.ThinkingBlock values. It handles both the
// freshly-emitted typed shape ([]event.ThinkingBlock) and — importantly — the
// JSON-decoded-from-disk shape ([]any of map[string]any with keys
// "thinking"/"signature"/"data").
func parseThinkingBlocks(v any) []gollama.ThinkingBlock {
	switch blocks := v.(type) {
	case []event.ThinkingBlock:
		if len(blocks) == 0 {
			return nil
		}
		out := make([]gollama.ThinkingBlock, len(blocks))
		for i, b := range blocks {
			out[i] = gollama.ThinkingBlock{Thinking: b.Thinking, Signature: b.Signature, Redacted: b.Redacted}
		}
		return out
	case []gollama.ThinkingBlock:
		return blocks
	case []any:
		var out []gollama.ThinkingBlock
		for _, e := range blocks {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, gollama.ThinkingBlock{
				Thinking:  strv(m, "thinking"),
				Signature: strv(m, "signature"),
				Redacted:  strv(m, "data"),
			})
		}
		return out
	default:
		return nil
	}
}

// str reads a string field from an event data map (mirrors event.str, kept local
// to the engine package).
func str(m map[string]any, k string) string { return strv(m, k) }

func strv(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

// boolv reads a bool field from an event data map, defensively handling the
// JSON-decoded shape (a plain bool).
func boolv(m map[string]any, k string) bool {
	if m == nil {
		return false
	}
	b, _ := m[k].(bool)
	return b
}
