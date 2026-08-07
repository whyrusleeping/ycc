// This file owns event block, header, body, and detail rendering.
package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/reflow/wrap"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"

	"github.com/whyrusleeping/ycc/internal/event"
)

// --- per-event rendering ---
func autoExpand(t string) bool { return t == "session_idle" || t == "question_asked" }

func (m *model) renderBlock(i int, ev *v1.Event) string {
	// An actor's name is spelled out only when it FIRST starts acting in a run
	// of consecutive rows; continuation rows show just its glyph (color + glyph
	// carry the identity), which declutters long single-actor stretches.
	first := m.firstOfRun(i)
	// Tool calls render as LSP-style "cards": a bordered frame whose title is
	// inset into the top border, with a status glyph and a nested Response box.
	// A tool_call is combined with its adjacent tool_result into one card.
	if ev.Type == "tool_call" {
		var res *v1.Event
		if ri := m.mergedResultIdx(i); ri >= 0 {
			res = m.evs[ri]
		}
		return m.renderToolCall(i, ev, res, first)
	}
	body := m.bodyFor(ev)
	hasBody := strings.TrimSpace(body) != ""
	exp := m.eventExpanded(int(ev.Seq), ev.Type)
	header := m.renderHeader(i, ev, i == m.selected, exp && hasBody, hasBody, first)
	if exp && hasBody {
		return header + "\n" + body
	}
	return header
}

// firstOfRun reports whether event i begins a new run of rows by its actor: true
// when the previous *rendered* row (skipping tool_results folded into their
// call) belongs to a different actor. Used to spell out the actor's name only at
// the start of each run.
func (m *model) firstOfRun(i int) bool {
	j := i - 1
	for j >= 0 && m.hiddenRow(j) {
		j--
	}
	if j < 0 {
		return true
	}
	return m.evs[j].Actor != m.evs[i].Actor
}

// lastOfSubRun reports whether event i is the last row of a contiguous run of
// sub-agent (implementer/reviewer) rows: true when the next *rendered* row
// (skipping tool_results folded into their call, mirroring firstOfRun) is not a
// sub-agent actor, or there is none. Drives the └─ vs ├─ tree connector.
func (m *model) lastOfSubRun(i int) bool {
	j := i + 1
	for j < len(m.evs) && m.hiddenRow(j) {
		j++
	}
	if j >= len(m.evs) {
		return true
	}
	return !isSub(m.evs[j].Actor)
}

func (m *model) renderHeader(i int, ev *v1.Event, selected, expanded, hasBody, first bool) string {
	detail := m.detailLineFor(ev)
	if expanded && hasBody {
		// The body box already shows the full content, so the header's one-line
		// snippet would be a redundant echo — drop it for prose rows (keeping only
		// non-body metadata like a turn's elapsed time).
		detail = expandedDetailLine(ev)
	}
	return m.renderHeaderDetail(i, ev, selected, expanded, hasBody, detail, first)
}

// expandedDetailLine is the header detail used when a row is expanded. For prose
// rows whose full text is rendered in the body box, the collapsed snippet is
// redundant, so it's suppressed (a model_turn keeps just its elapsed-time suffix,
// which isn't echoed in the body). Non-prose rows keep their normal summary.
func expandedDetailLine(ev *v1.Event) string {
	switch ev.Type {
	case "model_turn":
		if ms := durationMSField(ev); ms > 0 {
			return dimStyle.Render(fmtDurMS(ms))
		}
		return ""
	case "thinking":
		if tokens := dataField(ev, "reasoning_tokens"); tokens != "" && tokens != "0" {
			return dimStyle.Render(tokens + " hidden reasoning tokens")
		}
		return ""
	case "user_input", "plan_proposed", "session_idle", "question_asked", "question_answered":
		return ""
	}
	return detailLine(ev)
}

func (m *model) renderHeaderDetail(i int, ev *v1.Event, selected, expanded, hasBody bool, detail string, first bool) string {
	bar := "  "
	if selected {
		bar = selBarStyle.Render("▌ ")
	}
	// Sub-agent (implementer/reviewer) rows nest under the coordinator via a tree
	// connector (└─ on the last row of the run, ├─ otherwise) instead of a bare
	// two-space indent, so the nesting reads at a glance.
	indent := ""
	if isSub(ev.Actor) {
		indent = subConnector(m.lastOfSubRun(i))
	}
	tri := "  "
	if hasBody {
		if expanded {
			tri = "▼ "
		} else {
			tri = "▸ "
		}
	}
	// Per-type leading glyph: a fixed 2-cell colored column (glyph + space) placed
	// after the actor column, for fast scanning. Subtract its width from avail so
	// detail truncation stays correct.
	glyph := typeGlyph(ev.Type)
	glyphCol := ""
	if glyph != "" {
		gs := typeGlyphStyle(ev.Type)
		if ev.Type == "review_submitted" {
			gs = verdictStyle(dataField(ev, "verdict"))
		}
		glyphCol = gs.Render(glyph) + " "
	}
	avail := m.w - lipgloss.Width(indent) - 21 - lipgloss.Width(glyphCol)
	if avail < 12 {
		avail = 12
	}
	// model_turn is the agent's own narration — it frames the surrounding tool
	// activity, so we drop the redundant "model_turn" type label and let the
	// words read as prose.
	typeSeg := typeStyle.Render(ev.Type) + " "
	switch ev.Type {
	case "model_turn":
		typeSeg = ""
	case "session_idle":
		// Present the terminal report as a human result, not an internal wire-event
		// name. Its body is always expanded below this success-styled heading.
		typeSeg = successStyle.Render("finished") + " "
	}
	return fmt.Sprintf("%s%s%s%s %s%s",
		bar, indent, dimStyle.Render(tri),
		m.actorColumn(ev.Actor, first),
		glyphCol,
		typeSeg+trunc(detail, avail))
}

func (m *model) bodyFor(ev *v1.Event) string {
	if c, ok := m.bodyCache[int(ev.Seq)]; ok {
		return c
	}
	c := m.renderBody(ev)
	m.bodyCache[int(ev.Seq)] = c
	return c
}

// bodyWrapWidth is the wrap width for hand-assembled (non-markdown) body text,
// accounting for the two-space body indent with a little slack for prefixes.
func (m *model) bodyWrapWidth() int {
	w := m.w - 8
	if w < 20 {
		w = 20
	}
	return w
}

// wrapTo wraps s to width w: word-aware first, then hard-wrapped so an unbroken
// token can never overflow the line (the same pairing the thinking body uses).
func wrapTo(s string, w int) string {
	if w < 1 {
		return s
	}
	return wrap.String(wordwrap.String(s, w), w)
}

// answerLines renders a user answer folded beneath its question: a dim "→ "
// arrow followed by the wrapped answer text, continuation lines aligned under
// the text. indent is prepended to every line.
func answerLines(a string, w int, indent string) string {
	a = strings.TrimSpace(a)
	if a == "" {
		return indent + dimStyle.Render("→ (no answer)")
	}
	lines := strings.Split(wrapTo(a, w-2), "\n")
	for i, ln := range lines {
		if i == 0 {
			lines[i] = indent + dimStyle.Render("→ ") + ln
		} else {
			lines[i] = indent + "  " + ln
		}
	}
	return strings.Join(lines, "\n")
}

// autoAnswerLine is the compact rendering of an unattended auto-answer:
// the canned "no human is available…" paragraph the agent receives adds
// nothing for the human reading the log, so one dim line carries the fact.
func autoAnswerLine(indent string) string {
	return indent + dimStyle.Render("→ auto-answered (unattended execution): agent proceeds on its own judgement")
}

func (m *model) renderBody(ev *v1.Event) string {
	switch ev.Type {
	case "question_asked":
		return m.questionBody(ev)
	case "question_answered":
		// Normally folded into its question_asked row (isFoldedAnswer) and never
		// rendered standalone; this remains only for an orphaned answer whose
		// question isn't in the log.
		if ans := dataList(ev, "answers"); len(ans) > 0 {
			var b strings.Builder
			for i, a := range ans {
				fmt.Fprintf(&b, "A%d: %s\n", i+1, a)
			}
			return indentLines(m.markdown(strings.TrimRight(b.String(), "\n")), "  ")
		}
		txt := firstField(ev, "answer")
		if txt == "" {
			return ""
		}
		return indentLines(m.markdown(txt), "  ")
	case "model_turn", "user_input":
		txt := firstField(ev, "text", "report", "question", "answer")
		if txt == "" {
			return ""
		}
		return indentLines(m.markdown(txt), "  ")
	case "plan_proposed":
		// Plans are authored as Markdown. Render the plan field itself rather than
		// falling back to the event's JSON envelope so numbered steps, headings,
		// lists, and code references remain easy to scan when expanded.
		plan := dataField(ev, "plan")
		if strings.TrimSpace(plan) == "" {
			return ""
		}
		return indentLines(m.markdown(plan), "  ")
	case "session_idle":
		txt := firstField(ev, "report")
		if strings.TrimSpace(txt) == "" {
			return ""
		}
		// The finish report is the canonical human-facing result. Render it in full
		// as Markdown; an immediately preceding duplicate model_turn is folded away
		// by isFinishTurnEcho rather than truncating or hiding this report.
		return indentLines(m.markdown(txt), "  ")
	case "thinking":
		// Render the reasoning summary dimmed so it reads as the model's
		// "inner voice", distinct from its actual response (spec §18).
		txt := dataField(ev, "text")
		if strings.TrimSpace(txt) == "" {
			return ""
		}
		if w := m.w - lipgloss.Width(bodyBar); w > 0 {
			txt = wrap.String(wordwrap.String(txt, w), w)
		}
		return indentLines(styleLines(txt, thinkStyle), bodyBar)
	case "tool_call":
		return indentLines(prettyArgs(dataField(ev, "args")), bodyBar)
	case "tool_result":
		r := dataField(ev, "result")
		if r == "" {
			return ""
		}
		// Error output keeps the existing plain/diff/cat-n behavior — we don't
		// language-highlight error text (it's usually not source code).
		if dataField(ev, "error") == "true" {
			return indentLines(highlightResult(r), bodyBar)
		}
		return indentLines(m.highlightToolResult(r, ev), bodyBar)
	case "review_submitted":
		// The verdict is colorized in a plain header line (model — VERDICT); passing
		// it through glamour would strip the ANSI, so only the summary is markdown-
		// rendered, indented below the header. Both share the "  " body indent.
		verdict := dataField(ev, "verdict")
		head := dataField(ev, "model") + " — " + verdictStyle(verdict).Render(strings.ToUpper(verdict))
		summary := strings.TrimSpace(dataField(ev, "summary"))
		body := head
		if summary != "" {
			body += "\n" + m.markdown(summary)
		}
		return indentLines(body, "  ")
	case "session_error":
		msg := dataField(ev, "msg")
		// Error messages (e.g. a backend 400 invalid_request_error with a long
		// JSON body) are often a single very long line. Wrap to the body width so
		// the text doesn't run off the right edge — wordwrap on spaces, then hard
		// wrap to break any unbroken token (URLs, JSON, etc.).
		if w := m.w - lipgloss.Width(bodyBar); w > 0 {
			msg = wrap.String(wordwrap.String(msg, w), w)
		}
		body := errStyle.Render(msg)
		// Structured classification (engine loop failures, spec §7.2): lead with
		// a compact "kind (status) · N attempts — hint" line so the user sees
		// what class of failure it was without reading the provider body.
		if head := sessionErrorHead(ev); head != "" {
			body = errStyle.Bold(true).Render(head) + "\n" + body
		}
		return indentLines(body, bodyBar)
	default:
		return ""
	}
}

func (m *model) markdown(s string) string {
	if m.glam == nil {
		return s
	}
	out, err := m.glam.Render(s)
	if err != nil {
		return s
	}
	return strings.Trim(out, "\n")
}

const bodyBar = "  │ "

func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// styleLines applies a style to each line independently so lipgloss does not
// pad the block to the longest line's width (which would push lines past the
// terminal edge and cause spurious wraps).
func styleLines(s string, st lipgloss.Style) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = st.Render(ln)
	}
	return strings.Join(lines, "\n")
}

// detailLineFor is detailLine plus session-model context: a queued mid-run
// user_input echo (queued:true) that has not yet been delivered gets a dim
// "(queued)" suffix, so the transcript never claims the message was delivered
// before its checkpoint. Once the matching user_input_delivered event arrives,
// rebuild() re-renders and the suffix disappears (spec §18.7).
func (m *model) detailLineFor(ev *v1.Event) string {
	d := detailLine(ev)
	if ev.Type == "user_input" && dataField(ev, "queued") == "true" && !m.deliveredSeqs[ev.Seq] {
		d += " " + dimStyle.Render("(queued)")
	}
	return d
}

func detailLine(ev *v1.Event) string {
	switch ev.Type {
	case "tool_call":
		return fmt.Sprintf("%s(%s)", dataField(ev, "name"), oneLine(dataField(ev, "args"), 70))
	case "tool_result":
		return appendDur(ev, oneLine(dataField(ev, "result"), 90))
	case "model_turn":
		return appendDur(ev, oneLine(dataField(ev, "text"), 120))
	case "thinking":
		label := "(reasoning summary"
		if tokens := dataField(ev, "reasoning_tokens"); tokens != "" && tokens != "0" {
			label += " · " + tokens + " hidden tokens"
		}
		label += ") "
		return dimStyle.Render(label + oneLine(dataField(ev, "text"), 110))
	case "user_input":
		return "› " + oneLine(dataField(ev, "text"), 120)
	case "question_asked":
		if qs := dataQuestions(ev); len(qs) > 0 {
			prompts := make([]string, len(qs))
			for i, q := range qs {
				prompts[i] = q.prompt
			}
			return "? " + oneLine(fmt.Sprintf("%d questions: %s", len(qs), strings.Join(prompts, " · ")), 120)
		}
		return "? " + oneLine(dataField(ev, "question"), 120)
	case "question_answered":
		if ans := dataList(ev, "answers"); len(ans) > 0 {
			return oneLine(strings.Join(ans, " · "), 120)
		}
		return oneLine(dataField(ev, "answer"), 100)
	case "subagent_spawned", "subagent_finished":
		return strings.TrimSpace(dataField(ev, "role") + " " + dataField(ev, "model"))
	case "job_started":
		return strings.TrimSpace(dataField(ev, "id") + " " + oneLine(dataField(ev, "label"), 100) + " · running")
	case "job_finished":
		return strings.TrimSpace(dataField(ev, "id") + " " + oneLine(dataField(ev, "label"), 80) + " · " + dataField(ev, "status"))
	case "job_notified":
		return oneLine(dataField(ev, "text"), 120)
	case "review_submitted":
		verdict := dataField(ev, "verdict")
		return fmt.Sprintf("%s: %s — %s", dataField(ev, "model"), verdictStyle(verdict).Render(verdict), oneLine(dataField(ev, "summary"), 80))
	case "commit_made":
		return dataField(ev, "sha") + " " + oneLine(dataField(ev, "message"), 80)
	case "doc_updated":
		return strings.TrimSpace(dataField(ev, "task") + " " + dataField(ev, "section") + " " + dataField(ev, "status"))
	case "plan_proposed":
		parts := []string{}
		if task := strings.TrimSpace(dataField(ev, "task")); task != "" {
			parts = append(parts, "task "+task)
		}
		if plan := firstNonEmptyLine(dataField(ev, "plan")); plan != "" {
			parts = append(parts, oneLine(plan, 100))
		}
		return strings.Join(parts, " — ")
	case "mode_changed":
		return dataField(ev, "from") + " → " + dataField(ev, "to")
	case "session_idle":
		return oneLine(dataField(ev, "report"), 120)
	case "session_error":
		return oneLine(dataField(ev, "msg"), 120)
	case "budget_warning":
		return "⚠ budget warning — " + budgetSummary(ev)
	case "budget_exceeded":
		action := dataField(ev, "action")
		suffix := ""
		switch action {
		case "halt":
			suffix = " — halting (wrap up current task)"
		case "continue":
			suffix = " — continuing past cap (confirmed)"
		}
		return "⚠ budget reached — " + budgetSummary(ev) + suffix
	}
	return ""
}

// budgetSummary renders the spent/cap datum carried on a budget_warning /
// budget_exceeded event for the transcript row (task 0137).
func budgetSummary(ev *v1.Event) string {
	tokens := int64(floatField(ev, "tokens"))
	tokenCap := int64(floatField(ev, "token_cap"))
	cost := floatField(ev, "cost")
	costCap := floatField(ev, "cost_cap")
	var parts []string
	if tokenCap > 0 {
		parts = append(parts, fmt.Sprintf("%s/%s tok", fmtTokens(int(tokens)), fmtTokens(int(tokenCap))))
	}
	if costCap > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f/$%.2f", cost, costCap))
	}
	return strings.Join(parts, ", ")
}

// appendDur appends a compact, dim-styled elapsed-duration suffix to a row's
// detail line when the event carries a positive duration_ms, so per-turn and
// per-tool-call timing is visible when scanning the chat log.
func appendDur(ev *v1.Event, s string) string {
	ms := durationMSField(ev)
	if ms <= 0 {
		return s
	}
	return s + dimStyle.Render(" "+fmtDurMS(ms))
}

// durationMSField reads the numeric duration_ms field from an event's data JSON,
// returning 0 when absent or unparsable.
func durationMSField(ev *v1.Event) int64 {
	if ev.DataJson == "" {
		return 0
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return 0
	}
	if v, ok := mp["duration_ms"].(float64); ok {
		return int64(v)
	}
	return 0
}

// fmtDurMS renders a millisecond duration compactly: sub-second as "340ms",
// otherwise one-decimal seconds like "1.2s".
func fmtDurMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// eventUsage extracts the per-turn token usage and the logical model name from a
// model_turn event's data JSON (task 0062). It parses the proto DataJson directly
// (the live stream carries proto Events, not event.Event) and reads the nested
// "usage" object plus "model_name". Numbers decode as float64; a missing or
// unparsable usage block yields the zero Usage so accumulation degrades gracefully.
func eventUsage(ev *v1.Event) (event.Usage, string) {
	if ev == nil || ev.DataJson == "" {
		return event.Usage{}, ""
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return event.Usage{}, ""
	}
	name, _ := mp["model_name"].(string)
	u, _ := mp["usage"].(map[string]any)
	if u == nil {
		return event.Usage{}, name
	}
	num := func(k string) int {
		if f, ok := u[k].(float64); ok {
			return int(f)
		}
		return 0
	}
	return event.Usage{
		Input:      num("input"),
		Output:     num("output"),
		CacheRead:  num("cache_read"),
		CacheWrite: num("cache_write"),
		Total:      num("total"),
	}, name
}

func dataField(ev *v1.Event, key string) string {
	if ev.DataJson == "" {
		return ""
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return ""
	}
	switch v := mp[key].(type) {
	case string:
		return v
	case bool:
		return fmt.Sprintf("%t", v)
	case float64:
		return fmt.Sprintf("%g", v)
	}
	return ""
}

// floatField pulls a numeric field from an event's data JSON as a float64,
// returning 0 when absent or non-numeric (task 0137).
func floatField(ev *v1.Event, key string) float64 {
	if ev.DataJson == "" {
		return 0
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return 0
	}
	if v, ok := mp[key].(float64); ok {
		return v
	}
	return 0
}

// dataList pulls a list-of-strings field from an event's data JSON, dropping
// non-string and empty entries. Returns nil when absent.
func dataList(ev *v1.Event, key string) []string {
	if ev.DataJson == "" {
		return nil
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return nil
	}
	raw, ok := mp[key].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstField(ev *v1.Event, keys ...string) string {
	for _, k := range keys {
		if v := dataField(ev, k); v != "" {
			return v
		}
	}
	return ""
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func oneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return trunc(s, n)
}

func trunc(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	if n < 1 {
		n = 1
	}
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r) + "…"
}
