// This file owns the event pipeline, transcript pairing, folding, and render caches.
package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"

	"github.com/whyrusleeping/ycc/internal/event"
)

func (m *model) toggle(i int) {
	if i < 0 || i >= len(m.evs) {
		return
	}
	// The finish report is the session's primary result and must remain visible;
	// unlike ordinary transcript rows it cannot be manually collapsed.
	if m.evs[i].Type == "session_idle" {
		return
	}
	// A hidden row (folded tool_result or empty model_turn) toggles the visible
	// row it shares.
	for m.hiddenRow(i) && i > 0 {
		i--
	}
	seq := int(m.evs[i].Seq)
	cur := m.eventExpanded(seq, m.evs[i].Type)
	m.expanded[seq] = !cur
	m.invalidateRow(i) // expansion changes this row's rendered block
	m.rebuild()
	m.ensureVisible()
}

// applyTransient routes a transient (broadcast-only) event into ephemeral live
// UI state and reports whether that state changed. Transients NEVER enter m.evs,
// the reducers, or seq tracking (task 0129) — they only drive the live tail row.
//
// turn_delta carries {"text": <snapshot>} where text is the FULL accumulated
// turn output so far (snapshot semantics), so a new delta simply replaces the
// actor's tail. A done/empty delta ({"text":"","done":true}) clears it so no
// stale tail survives the end of a turn. retry carries an API-failure backoff
// notice ({attempt, max_attempts, delay_ms, kind, status}) rendered as a
// per-actor note; a non-empty delta (the next attempt streaming) clears it.
// Other (future) transient types are ignored.
func (m *model) applyTransient(ev *v1.Event) bool {
	if ev == nil {
		return false
	}
	if ev.Type == "retry" {
		if m.retryNotes == nil {
			m.retryNotes = map[string]string{}
		}
		m.retryNotes[ev.Actor] = retryNoteText(ev)
		return true
	}
	if ev.Type != "turn_delta" {
		return false
	}
	if m.liveTails == nil {
		m.liveTails = map[string]string{}
	}
	text := dataField(ev, "text")
	done := dataField(ev, "done") == "true"
	if done || strings.TrimSpace(text) == "" {
		if _, ok := m.liveTails[ev.Actor]; ok {
			delete(m.liveTails, ev.Actor)
			return true
		}
		return false
	}
	changed := false
	// A fresh attempt is streaming: any pending retry note for this actor is
	// obsolete.
	if _, ok := m.retryNotes[ev.Actor]; ok {
		delete(m.retryNotes, ev.Actor)
		changed = true
	}
	if m.liveTails[ev.Actor] == text {
		return changed
	}
	m.liveTails[ev.Actor] = text
	return true
}

// retryNoteText renders a transient retry event's data as a one-line note, e.g.
// "rate_limit (429): retrying in 8s — attempt 2/3".
func retryNoteText(ev *v1.Event) string {
	kind := dataField(ev, "kind")
	if kind == "" {
		kind = "api error"
	}
	head := kind
	if st := dataField(ev, "status"); st != "" && st != "0" {
		head += " (" + st + ")"
	}
	note := head + ": retrying"
	if ms := dataField(ev, "delay_ms"); ms != "" {
		if v, err := strconv.ParseFloat(ms, 64); err == nil && v > 0 {
			note += " in " + (time.Duration(v) * time.Millisecond).Round(100*time.Millisecond).String()
		}
	}
	if a, max := dataField(ev, "attempt"), dataField(ev, "max_attempts"); a != "" && max != "" {
		note += fmt.Sprintf(" — attempt %s/%s", a, max)
	}
	return note
}

// sessionErrorHead renders the structured classification a session_error event
// may carry (kind/status/attempts, emitted by the engine loop, spec §7.2) as a
// compact lead line, plus an actionable hint for the kinds a user can act on.
// Returns "" for legacy/unclassified errors so they render exactly as before.
func sessionErrorHead(ev *v1.Event) string {
	kind := dataField(ev, "kind")
	if kind == "" || kind == "unknown" {
		return ""
	}
	head := kind
	if st := dataField(ev, "status"); st != "" && st != "0" {
		head += " (" + st + ")"
	}
	if a := dataField(ev, "attempts"); a != "" && a != "1" {
		head += " · " + a + " attempts"
	}
	switch kind {
	case "auth":
		head += " — check the model's API key / credentials"
	case "rate_limit", "overloaded", "server", "timeout", "network":
		head += " — transient; sending a message retries the turn"
	}
	return head
}

func (m *model) appendEvent(ev *v1.Event) {
	m.evs = append(m.evs, ev)
	n := len(m.evs) - 1
	// A new event can change how the rows just before it render: the previous
	// rendered row's └─/├─ sub-run connector, or a tool_call's collapsed summary
	// once this tool_result folds into it (in-flight ○ becomes ✓/✗ + response).
	// Drop that row's cached block so only IT re-renders, not the whole log.
	for j := n - 1; j >= 0; j-- {
		if !m.hiddenRow(j) {
			m.invalidateRow(j)
			break
		}
	}
	if ev.Type == "user_input_delivered" {
		// Mark the queued echo this delivery pairs with as delivered so it stops
		// rendering "(queued)" once it actually entered the conversation (§18.7).
		if seq, ok := deliveredSeq(ev); ok {
			if m.deliveredSeqs == nil {
				m.deliveredSeqs = map[int64]bool{}
			}
			m.deliveredSeqs[seq] = true
			// The queued echo row (possibly far back) now drops its "(queued)"
			// tag — invalidate its cached block so it re-renders.
			for j := n - 1; j >= 0; j-- {
				if m.evs[j].Seq == seq {
					m.invalidateRow(j)
					break
				}
			}
		}
	}
	switch ev.Type {
	case "model_turn":
		// A coordinator turn identifies the model that actually produced it. Keep
		// the top bar aligned with recorded reality (especially when replaying a
		// session) rather than relying only on the latest global role defaults.
		if ev.Actor == "coordinator" {
			if name := dataField(ev, "model_name"); name != "" {
				m.roleCoord = name
			}
		}
		// The durable turn supersedes any live streamed tail for this actor: drop
		// it so the persisted row replaces the in-progress row with no stale tail
		// (task 0129). A clearing turn_delta usually arrives too, but clearing here
		// makes the swap deterministic even if that transient is lost. Any pending
		// retry note is likewise superseded by the turn's outcome.
		delete(m.liveTails, ev.Actor)
		delete(m.retryNotes, ev.Actor)
		// Accumulate the turn's usage into the running per-model tally that feeds
		// the live token/cost readout (task 0062, spec §20.1). Parsing is best-effort:
		// a turn without a usage block contributes nothing.
		if u, name := eventUsage(ev); u != (event.Usage{}) {
			if m.usageByModel == nil {
				m.usageByModel = map[string]event.Usage{}
			}
			cur := m.usageByModel[name]
			cur.Input += u.Input
			cur.Output += u.Output
			cur.CacheRead += u.CacheRead
			cur.CacheWrite += u.CacheWrite
			cur.Total += u.Total
			m.usageByModel[name] = cur
		}
	case "budget_warning":
		// Session crossed ~80% of a configured cap (task 0137, spec §20.6): surface
		// a distinct status-bar warning. Track the highest pct seen.
		if p := floatField(ev, "pct"); p > m.budgetPct {
			m.budgetPct = p
		}
	case "budget_exceeded":
		m.budgetExceeded = true
	case "task_focus":
		// The session's focus moved to a (new) backlog task: surface it in the
		// status bar so the header always names the task being worked on.
		if t := dataField(ev, "task"); t != "" {
			m.focusTask = t
			m.focusTaskTitle = dataField(ev, "title")
		}
	case "plan_proposed":
		// This is the canonical row for propose_plan. Fold away the tool-call card
		// that was visible in flight and refresh its neighbors' run boundaries.
		for j := len(m.evs) - 2; j >= 0; j-- {
			pv := m.evs[j]
			if pv.Actor != ev.Actor {
				continue
			}
			if pv.Type == "tool_call" && dataField(pv, "name") == "propose_plan" {
				m.invalidateRow(j)
				m.invalidateNeighbors(j)
			}
			if pv.Type == "tool_call" || pv.Type == "tool_result" || pv.Type == "plan_proposed" {
				break
			}
		}
	case "question_asked":
		// This question is now the canonical row for its ask_user exchange: the
		// tool_call that produced it (rendered while in flight, possibly with
		// other-actor rows in between) folds away. Drop its cached block/fold
		// state — and its rendered neighbors', whose run boundaries shift.
		for j := len(m.evs) - 2; j >= 0; j-- {
			pv := m.evs[j]
			if pv.Actor != ev.Actor {
				continue
			}
			if pv.Type == "tool_call" && dataField(pv, "name") == "ask_user" {
				m.invalidateRow(j)
				m.invalidateNeighbors(j)
			}
			if pv.Type == "tool_call" || pv.Type == "tool_result" || pv.Type == "question_asked" {
				break
			}
		}
		if qs := dataQuestions(ev); len(qs) > 0 {
			// Multi-question form: start the questionnaire wizard.
			m.startWizard(qs, ev.Seq)
			break
		}
		m.pending = dataField(ev, "question")
		m.pendingSeq = ev.Seq
		m.status = "waiting for your answer"
		m.pickerOpts = dataList(ev, "options")
		if len(m.pickerOpts) > 0 {
			m.picking = true
			m.pickerCursor = 0
			m.input.Blur()
		} else {
			m.picking = false
		}
	case "question_answered":
		m.pending = ""
		m.pendingSeq = 0
		m.status = "running"
		m.pickerOpts = nil
		m.picking = false
		// clearWizard also wipes the body cache, which the single-question path
		// needs too: the answer now folds into the question_asked row's body.
		m.clearWizard()
		// Safety net: a picker question blurred the textarea; make sure the
		// confirmed answer hands focus back so the input box is typable again.
		// Focus() flips the state synchronously — the discarded cmd is only the
		// cursor blink. Skip while the transcript search bar owns input.
		if !m.searching {
			m.input.Focus()
		}
	case "session_idle":
		m.status = "idle"
		// The idle report supersedes an echoed final coordinator model_turn. That
		// earlier row may already have a cached visible fold from the previous frame,
		// so invalidate its fold and rendered neighbors now that the pairing exists.
		for j := n - 1; j >= 0; j-- {
			if m.evs[j].Type == "model_turn" && m.evs[j].Actor == "coordinator" &&
				strings.TrimSpace(dataField(m.evs[j], "text")) != "" {
				m.invalidateRow(j)
				m.invalidateNeighbors(j)
				break
			}
		}
	case "session_reopened":
		// Reopen marker: the daemon reconstructed the model history and repaired
		// any dangling ask_user tool call with a synthetic result (engine replay),
		// so a question_asked replayed just before this marker is stale — no
		// answer can ever be delivered to it. Drop the picker/wizard and give the
		// input box back, or the reopened session starts with dead input.
		if m.pending != "" || m.picking || m.wizActive {
			m.pending = ""
			m.pendingSeq = 0
			m.pickerOpts = nil
			m.picking = false
			m.clearWizard()
			// The stale question also latched "waiting for your answer"; the
			// daemon follows up with its real state (session_idle / activity).
			m.status = "running"
			if !m.searching {
				m.input.Focus()
			}
		}
	case "session_error":
		m.status = "error"
		// A failed turn ends any in-progress stream: drop the actor's live tail so
		// no stale streamed text lingers below the error (task 0129), and drop any
		// pending retry note (the failure is now durable).
		delete(m.liveTails, ev.Actor)
		delete(m.retryNotes, ev.Actor)
	case "interrupted":
		m.status = "paused"
		m.paused = true
	case "resumed":
		m.status = "running"
		m.paused = false
	case "mode_changed":
		m.mode = dataField(ev, "to")
		m.status = "running"
	case "thinking_level_changed":
		if to := dataField(ev, "to"); to != "" {
			role := dataField(ev, "role")
			if role == "" || role == "all" {
				m.thinkLevels["coordinator"] = to
				m.thinkLevels["implementer"] = to
				m.thinkLevels["reviewers"] = to
			} else {
				m.thinkLevels[role] = to
			}
		}
	case "role_config_changed":
		if c := dataField(ev, "coordinator"); c != "" {
			m.roleCoord = c
		}
		if i := dataField(ev, "implementer"); i != "" {
			m.roleImpl = i
		}
		if rv := dataList(ev, "reviewers"); len(rv) > 0 {
			m.roleReviewrs = rv
		}
	}
	// Clear a latched error status once real activity resumes (task 0051):
	// the header must not stay stuck on "error" after recovery. An idle status
	// clears the same way: prodding a finished session emits a user_input echo
	// the moment the daemon accepts it, but the first model event can lag tens
	// of seconds behind (long context + thinking) — without this the header
	// keeps saying "idle", the footer keeps offering "session finished", and no
	// spinner runs, so the accepted follow-up looks like it went nowhere.
	if m.status == "error" || m.status == "idle" {
		switch ev.Type {
		case "model_turn", "tool_call", "tool_result", "thinking", "user_input", "user_input_delivered":
			m.status = "running"
		}
	}
	if m.follow {
		m.selected = len(m.evs) - 1
	}
	// NOTE: appendEvent deliberately does NOT call rebuild() — the caller batches
	// a burst of events (e.g. the persisted log replayed on reopen) and rebuilds
	// once, turning an O(N^2) "rebuild per event" reload into a single O(N) pass.
}

// mergedResultIdx reports the index of the tool_result that should be folded
// into the tool_call at index i (rendered as one combined row), or -1 when the
// call at i has no adjacent matching result. Pairing is by adjacency (result at
// i+1) which naturally excludes spawn-style tools whose subagent events appear
// between the parent's call and result.
func (m *model) mergedResultIdx(i int) int {
	if i < 0 || i+1 >= len(m.evs) {
		return -1
	}
	call, res := m.evs[i], m.evs[i+1]
	if call.Type != "tool_call" || res.Type != "tool_result" {
		return -1
	}
	if call.Actor != res.Actor {
		return -1
	}
	cid, rid := dataField(call, "id"), dataField(res, "id")
	if cid != "" && rid != "" && cid != rid {
		return -1
	}
	return i + 1
}

// isMergedResult reports whether the event at index j is a tool_result that has
// been folded into its preceding tool_call's combined row.
func (m *model) isMergedResult(j int) bool {
	return j > 0 && m.mergedResultIdx(j-1) == j
}

// askQuestionIdx returns the index of the question_asked event produced by the
// ask_user tool_call at i, or -1. The interaction gate emits question_asked
// while the tool call is executing, so it is the next same-actor question
// event; hitting any other same-actor tool_call/tool_result first means the
// call never asked (e.g. a validation error) and it must stay visible.
func (m *model) askQuestionIdx(i int) int {
	if i < 0 || i >= len(m.evs) {
		return -1
	}
	call := m.evs[i]
	if call.Type != "tool_call" || dataField(call, "name") != "ask_user" {
		return -1
	}
	for j := i + 1; j < len(m.evs); j++ {
		ev := m.evs[j]
		if ev.Actor != call.Actor {
			continue
		}
		switch ev.Type {
		case "question_asked":
			return j
		case "tool_call", "tool_result":
			return -1
		}
	}
	return -1
}

// resultCallIdx returns the index of the tool_call that produced the
// tool_result at i, scanning backward over the interleaved question events an
// ask_user call emits (so, unlike mergedResultIdx, it does not require
// adjacency). A same-actor tool_result encountered first means the result at i
// belongs to some earlier call and pairing fails.
func (m *model) resultCallIdx(i int) int {
	if i < 0 || i >= len(m.evs) || m.evs[i].Type != "tool_result" {
		return -1
	}
	res := m.evs[i]
	for j := i - 1; j >= 0; j-- {
		ev := m.evs[j]
		if ev.Actor != res.Actor {
			continue
		}
		switch ev.Type {
		case "tool_call":
			cid, rid := dataField(ev, "id"), dataField(res, "id")
			if cid != "" && rid != "" && cid != rid {
				return -1
			}
			return j
		case "tool_result":
			return -1
		}
	}
	return -1
}

// answerIdxFor returns the index of the question_answered event that resolved
// the question_asked at qi, or -1 while it is still unanswered. Questions are
// strictly serialized per actor (the interaction gate holds one pending
// question at a time), so the next same-actor question event decides.
func (m *model) answerIdxFor(qi int) int {
	if qi < 0 || qi >= len(m.evs) || m.evs[qi].Type != "question_asked" {
		return -1
	}
	for j := qi + 1; j < len(m.evs); j++ {
		ev := m.evs[j]
		if ev.Actor != m.evs[qi].Actor {
			continue
		}
		switch ev.Type {
		case "question_answered":
			return j
		case "question_asked":
			return -1
		}
	}
	return -1
}

// questionIdxForAnswer is the inverse of answerIdxFor: the index of the
// question_asked that the question_answered at i resolves, or -1.
func (m *model) questionIdxForAnswer(i int) int {
	if i < 0 || i >= len(m.evs) || m.evs[i].Type != "question_answered" {
		return -1
	}
	for j := i - 1; j >= 0; j-- {
		ev := m.evs[j]
		if ev.Actor != m.evs[i].Actor {
			continue
		}
		switch ev.Type {
		case "question_asked":
			return j
		case "question_answered":
			return -1
		}
	}
	return -1
}

// answerEventFor returns the question_answered event paired with the given
// question_asked event, or nil while it is unanswered. Used by questionBody to
// fold the answer into the question's rendered block.
func (m *model) answerEventFor(q *v1.Event) *v1.Event {
	for i, ev := range m.evs {
		if ev.Seq == q.Seq && ev.Type == "question_asked" {
			if ai := m.answerIdxFor(i); ai >= 0 {
				return m.evs[ai]
			}
			return nil
		}
	}
	return nil
}

// isAskUserPlumbing reports whether event i is ask_user tool plumbing already
// represented by its question_asked row: the ask_user tool_call itself (once
// its question_asked exists) or that call's tool_result (whose payload just
// repeats the answer). An errored result (e.g. the ask was cancelled) stays
// visible so failures are never silently swallowed.
func (m *model) isAskUserPlumbing(i int) bool {
	if i < 0 || i >= len(m.evs) {
		return false
	}
	switch m.evs[i].Type {
	case "tool_call":
		return m.askQuestionIdx(i) >= 0
	case "tool_result":
		if dataField(m.evs[i], "error") == "true" {
			return false
		}
		ci := m.resultCallIdx(i)
		return ci >= 0 && m.askQuestionIdx(ci) >= 0
	}
	return false
}

// isFoldedAnswer reports whether event i is a question_answered folded into its
// preceding question_asked row (which renders the Q and the A as one block).
func (m *model) isFoldedAnswer(i int) bool {
	if i < 0 || i >= len(m.evs) || m.evs[i].Type != "question_answered" {
		return false
	}
	return m.questionIdxForAnswer(i) >= 0
}

// planProposalIdx returns the plan_proposed event emitted while the propose_plan
// tool call at i executes. Like ask_user, this tool emits a human-facing event
// between its call and result; that event is the canonical transcript row.
func (m *model) planProposalIdx(i int) int {
	if i < 0 || i >= len(m.evs) {
		return -1
	}
	call := m.evs[i]
	if call.Type != "tool_call" || dataField(call, "name") != "propose_plan" {
		return -1
	}
	for j := i + 1; j < len(m.evs); j++ {
		ev := m.evs[j]
		if ev.Actor != call.Actor {
			continue
		}
		switch ev.Type {
		case "plan_proposed":
			return j
		case "tool_call", "tool_result":
			return -1
		}
	}
	return -1
}

// isPlanProposalPlumbing folds a successful propose_plan tool call/result into
// its plan_proposed row. Failed results remain visible so persistence errors are
// never hidden from the transcript.
func (m *model) isPlanProposalPlumbing(i int) bool {
	if i < 0 || i >= len(m.evs) {
		return false
	}
	switch m.evs[i].Type {
	case "tool_call":
		return m.planProposalIdx(i) >= 0
	case "tool_result":
		if dataField(m.evs[i], "error") == "true" {
			return false
		}
		ci := m.resultCallIdx(i)
		return ci >= 0 && m.planProposalIdx(ci) >= 0
	}
	return false
}

// isEmptyModelTurn reports whether the event at index i is a model_turn that
// carries no text — i.e. an agent turn whose only payload was tool calls. Such a
// turn would otherwise render as a bare row showing just its timing/usage
// suffix, so we hide it and let the surrounding tool calls stand on their own.
// Per-turn token usage is still accumulated from the raw event stream elsewhere,
// so suppressing the row does not affect cost tracking.
func (m *model) isEmptyModelTurn(i int) bool {
	if i < 0 || i >= len(m.evs) {
		return false
	}
	ev := m.evs[i]
	return ev.Type == "model_turn" && strings.TrimSpace(dataField(ev, "text")) == ""
}

// hiddenRow reports whether event i renders no block of its own and instead
// shares the previous rendered row's start line: a tool_result folded into its
// preceding tool_call, an empty (tool-calls-only) model_turn, a final model_turn
// folded into its canonical session_idle finish report, ask_user tool plumbing
// (the question_asked row is the canonical rendering of the exchange), or a
// question_answered folded into its question_asked row. The result is memoized
// per index (see hiddenCache): the pairing scans behind it re-parse event JSON
// and can walk the log, so recomputing them for every row on every rebuild made
// long sessions quadratic. Entries are invalidated when a later event can flip
// an earlier row's fold (appendEvent) or wholesale via invalidateRender.
func (m *model) hiddenRow(i int) bool {
	if v, ok := m.hiddenCache[i]; ok {
		return v
	}
	h := m.computeHiddenRow(i)
	if m.hiddenCache == nil {
		m.hiddenCache = map[int]bool{}
	}
	m.hiddenCache[i] = h
	return h
}

func (m *model) computeHiddenRow(i int) bool {
	if i >= 0 && i < len(m.evs) && m.evs[i].Type == "user_input_delivered" {
		// The delivery marker is a bookkeeping event, not a message: its text is
		// already shown by the (now-upgraded) queued user_input row, so it renders
		// no block of its own — otherwise the message would appear twice (§18.7).
		return true
	}
	return m.isMergedResult(i) || m.isEmptyModelTurn(i) || m.isFinishTurnEcho(i) ||
		m.isAskUserPlumbing(i) || m.isPlanProposalPlumbing(i) || m.isFoldedAnswer(i)
}

// eventAt returns the index of the event whose rendered block contains content
// line `row`, or -1.
func (m *model) eventAt(row int) int {
	if row < 0 {
		return -1
	}
	for i := len(m.eventStart) - 1; i >= 0; i-- {
		if row >= m.eventStart[i] {
			idx := i
			for idx > 0 && m.hiddenRow(idx) {
				idx--
			}
			return idx
		}
	}
	return -1
}

func (m *model) ensureVisible() {
	if m.selected < 0 || m.selected >= len(m.eventStart) {
		return
	}
	start := m.eventStart[m.selected]
	if start < m.vp.YOffset() {
		m.vp.SetYOffset(start)
	} else if start >= m.vp.YOffset()+m.vp.Height() {
		m.vp.SetYOffset(start - m.vp.Height() + 1)
	}
}

func (m *model) makeRenderer() {
	w := m.w - 4
	if w < 20 {
		w = 20
	}
	// Use a fixed style, NOT WithAutoStyle: auto-style queries the terminal's
	// background by reading stdin, which Bubble Tea already owns — that blocks the
	// event loop and freezes the UI. The style is chosen from the user's explicit
	// theme pref (never by querying the terminal).
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(themeByName(m.prefs.Theme).glamourStyle), glamour.WithWordWrap(w))
	if err == nil {
		m.glam = r
	}
}

// invalidateRender drops every render cache: per-event bodies, whole rendered
// blocks, and memoized hidden-row folds. Called whenever a global rendering
// input changes (width, theme, auto-expand pref, picker/wizard state, or the
// event log being swapped out) so every row re-renders under the new inputs.
func (m *model) invalidateRender() {
	m.bodyCache = map[int]string{}
	m.blockCache = map[int]string{}
	m.hiddenCache = map[int]bool{}
}

// invalidateRow drops the cached block + hidden-fold state for event index i,
// forcing that single row to re-render on the next rebuild.
func (m *model) invalidateRow(i int) {
	delete(m.blockCache, i)
	delete(m.hiddenCache, i)
}

// invalidateNeighbors drops the cached blocks of the rendered rows immediately
// before and after index i. Used when row i's visibility flips (e.g. an
// ask_user tool_call folding away once its question_asked arrives): the
// neighbors' run-boundary rendering (actor name spelled vs glyph-only,
// └─ vs ├─ connectors) depends on which rows around them are visible.
func (m *model) invalidateNeighbors(i int) {
	for j := i - 1; j >= 0; j-- {
		if !m.hiddenRow(j) {
			m.invalidateRow(j)
			break
		}
	}
	for j := i + 1; j < len(m.evs); j++ {
		if !m.hiddenRow(j) {
			m.invalidateRow(j)
			break
		}
	}
}

// rebuild re-renders the whole event stream into the viewport, tracking the line
// offset of each event for click mapping.
func (m *model) rebuild() {
	if !m.ready {
		return
	}
	if m.blockCache == nil {
		m.blockCache = map[int]string{}
	}
	var b strings.Builder
	m.eventStart = m.eventStart[:0]
	line := 0
	for i, ev := range m.evs {
		m.eventStart = append(m.eventStart, line)
		// A hidden row (a tool_result folded into its preceding tool_call, or an
		// empty tool-calls-only model_turn) shares the previous rendered row's
		// start line and emits no block of its own.
		if m.hiddenRow(i) {
			if i > 0 {
				m.eventStart[i] = m.eventStart[i-1]
			}
			continue
		}
		// A row rendered in its selected state (either directly or because the
		// tool_result merged into it is selected) is drawn fresh and never cached:
		// selection moves constantly, and skipping the store means the previously
		// selected row simply re-renders once after the cursor leaves it.
		sel := i == m.selected || (i+1 == m.selected && m.mergedResultIdx(i) == i+1)
		block, ok := m.blockCache[i]
		if !ok || sel {
			block = m.renderBlock(i, ev)
			if !sel {
				m.blockCache[i] = block
			}
		}
		b.WriteString(block)
		b.WriteByte('\n')
		line += strings.Count(block, "\n") + 1
	}
	// Append the in-progress streamed tail rows (transient turn_delta output)
	// after the persisted conversation. They are ephemeral and carry no seq, so
	// they are NOT added to m.eventStart / selection tracking (task 0129).
	if tail := m.renderLiveTails(); tail != "" {
		b.WriteString(tail)
		b.WriteByte('\n')
	}
	m.vpContent = b.String()
	m.vp.SetContent(m.vpContent)
	if m.follow {
		m.vp.GotoBottom()
	}
}

// liveTailMaxLines caps how many trailing lines of an in-progress streamed turn
// the live tail row shows, so a long streaming turn can't dominate the viewport.
const liveTailMaxLines = 6

// renderLiveTails renders the in-progress streamed output (fed by transient
// turn_delta snapshots, task 0129) as dim, visibly in-progress tail rows appended
// after the persisted conversation, followed by any per-actor retry notes (fed
// by transient retry events — an API-failure backoff in progress, spec §7.2).
// Values are the full accumulated turn text so far, so each render just reflects
// the latest snapshot; the durable model_turn replaces the tail seamlessly
// (appendEvent clears the actor's entry when it arrives). Returns "" when
// nothing is streaming or retrying.
func (m *model) renderLiveTails() string {
	if len(m.liveTails) == 0 && len(m.retryNotes) == 0 {
		return ""
	}
	actors := make([]string, 0, len(m.liveTails))
	for a := range m.liveTails {
		actors = append(actors, a)
	}
	sort.Strings(actors)
	var b strings.Builder
	for _, actor := range actors {
		text := strings.TrimRight(m.liveTails[actor], "\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		w := m.w - lipgloss.Width(bodyBar)
		if w < 1 {
			w = 1
		}
		lines := strings.Split(wrapTo(text, w), "\n")
		if len(lines) > liveTailMaxLines {
			lines = lines[len(lines)-liveTailMaxLines:]
		}
		body := indentLines(styleLines(strings.Join(lines, "\n"), dimStyle), bodyBar)
		header := "  " + dimStyle.Render("▸ ") +
			actorStyle(actor).Render(fmt.Sprintf("%-13s", actor)) +
			dimStyle.Render("streaming…")
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(header)
		b.WriteByte('\n')
		b.WriteString(body)
	}
	// Retry notes render after the streamed tails: one warn-styled line per
	// actor waiting out an API-failure backoff.
	noteActors := make([]string, 0, len(m.retryNotes))
	for a := range m.retryNotes {
		noteActors = append(noteActors, a)
	}
	sort.Strings(noteActors)
	for _, actor := range noteActors {
		note := m.retryNotes[actor]
		if strings.TrimSpace(note) == "" {
			continue
		}
		line := "  " + warnStyle.Render("⟳ ") +
			actorStyle(actor).Render(fmt.Sprintf("%-13s", actor)) +
			warnStyle.Render(note)
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}

// isFinishTurnEcho reports whether model_turn i is duplicated by the next
// session_idle report. The finish event is the canonical human-facing result, so
// the earlier narration row is folded away and the report remains fully visible.
func (m *model) isFinishTurnEcho(i int) bool {
	if i < 0 || i >= len(m.evs) || m.evs[i].Type != "model_turn" || m.evs[i].Actor != "coordinator" {
		return false
	}
	turn := strings.TrimSpace(dataField(m.evs[i], "text"))
	if turn == "" {
		return false
	}
	for j := i + 1; j < len(m.evs); j++ {
		next := m.evs[j]
		if next.Type == "session_idle" {
			report := strings.TrimSpace(dataField(next, "report"))
			return report == turn || strings.HasPrefix(report, turn+"\n")
		}
		if next.Type == "model_turn" && next.Actor == "coordinator" &&
			strings.TrimSpace(dataField(next, "text")) != "" {
			// A newer coordinator response is the candidate finish turn instead.
			return false
		}
	}
	return false
}

// deliveredSeq extracts the queued-echo seq a user_input_delivered event refers
// to (spec §18.7). Returns false for other event types or malformed data.
func deliveredSeq(ev *v1.Event) (int64, bool) {
	if ev.Type != "user_input_delivered" || ev.DataJson == "" {
		return 0, false
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return 0, false
	}
	if f, ok := mp["seq"].(float64); ok {
		return int64(f), true
	}
	return 0, false
}

// deliveredSeqSet builds the set of delivered queued-echo seqs from an event
// slice, used when the transcript view loads a whole log at once.
func deliveredSeqSet(evs []*v1.Event) map[int64]bool {
	set := map[int64]bool{}
	for _, ev := range evs {
		if seq, ok := deliveredSeq(ev); ok {
			set[seq] = true
		}
	}
	return set
}
