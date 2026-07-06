package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// This file ports the transcript-folding helpers from internal/tui/tui.go so the
// markdown export applies the same fold/hide semantics without importing the TUI
// (whose versions are entangled with the model struct). The pairing scans are
// same-actor and id-matched, matching §18.3.

// mergedResultIdx reports the index of the tool_result folded into the tool_call
// at i (rendered as one combined row), or -1 when there is no adjacent matching
// result. Pairing is by adjacency (result at i+1), which excludes spawn-style
// tools whose subagent events appear between the parent's call and result.
func (b *builder) mergedResultIdx(i int) int {
	if i < 0 || i+1 >= len(b.evs) {
		return -1
	}
	call, res := b.evs[i], b.evs[i+1]
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

func (b *builder) isMergedResult(j int) bool {
	return j > 0 && b.mergedResultIdx(j-1) == j
}

// askQuestionIdx returns the index of the question_asked produced by the
// ask_user tool_call at i, or -1.
func (b *builder) askQuestionIdx(i int) int {
	if i < 0 || i >= len(b.evs) {
		return -1
	}
	call := b.evs[i]
	if call.Type != "tool_call" || dataField(call, "name") != "ask_user" {
		return -1
	}
	for j := i + 1; j < len(b.evs); j++ {
		ev := b.evs[j]
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

// resultCallIdx returns the index of the tool_call that produced the tool_result
// at i, scanning backward over the interleaved question events an ask_user call
// emits, or -1.
func (b *builder) resultCallIdx(i int) int {
	if i < 0 || i >= len(b.evs) || b.evs[i].Type != "tool_result" {
		return -1
	}
	res := b.evs[i]
	for j := i - 1; j >= 0; j-- {
		ev := b.evs[j]
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

// answerIdxFor returns the index of the question_answered resolving the
// question_asked at qi, or -1.
func (b *builder) answerIdxFor(qi int) int {
	if qi < 0 || qi >= len(b.evs) || b.evs[qi].Type != "question_asked" {
		return -1
	}
	for j := qi + 1; j < len(b.evs); j++ {
		ev := b.evs[j]
		if ev.Actor != b.evs[qi].Actor {
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

// questionIdxForAnswer is the inverse of answerIdxFor.
func (b *builder) questionIdxForAnswer(i int) int {
	if i < 0 || i >= len(b.evs) || b.evs[i].Type != "question_answered" {
		return -1
	}
	for j := i - 1; j >= 0; j-- {
		ev := b.evs[j]
		if ev.Actor != b.evs[i].Actor {
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

// answerEventFor returns the question_answered paired with the given
// question_asked event, or nil.
func (b *builder) answerEventFor(q *v1.Event) *v1.Event {
	for i, ev := range b.evs {
		if ev.Seq == q.Seq && ev.Type == "question_asked" {
			if ai := b.answerIdxFor(i); ai >= 0 {
				return b.evs[ai]
			}
			return nil
		}
	}
	return nil
}

// isAskUserPlumbing reports whether event i is ask_user tool plumbing already
// represented by its question_asked row. An errored result stays visible.
func (b *builder) isAskUserPlumbing(i int) bool {
	if i < 0 || i >= len(b.evs) {
		return false
	}
	switch b.evs[i].Type {
	case "tool_call":
		return b.askQuestionIdx(i) >= 0
	case "tool_result":
		if dataField(b.evs[i], "error") == "true" {
			return false
		}
		ci := b.resultCallIdx(i)
		return ci >= 0 && b.askQuestionIdx(ci) >= 0
	}
	return false
}

func (b *builder) isFoldedAnswer(i int) bool {
	if i < 0 || i >= len(b.evs) || b.evs[i].Type != "question_answered" {
		return false
	}
	return b.questionIdxForAnswer(i) >= 0
}

// isEmptyModelTurn reports whether the event at i is a text-less model_turn
// (tool-calls only).
func (b *builder) isEmptyModelTurn(i int) bool {
	if i < 0 || i >= len(b.evs) {
		return false
	}
	ev := b.evs[i]
	return ev.Type == "model_turn" && strings.TrimSpace(dataField(ev, "text")) == ""
}

// isEchoedIdle reports whether the event at i is a session_idle whose report
// merely echoes the preceding final model_turn (so its de-duped body is empty).
func (b *builder) isEchoedIdle(i int) bool {
	if i < 0 || i >= len(b.evs) {
		return false
	}
	ev := b.evs[i]
	return ev.Type == "session_idle" && b.idleReport(ev) == ""
}

// hiddenRow reports whether event i renders no block of its own.
func (b *builder) hiddenRow(i int) bool {
	if i >= 0 && i < len(b.evs) && b.evs[i].Type == "user_input_delivered" {
		return true
	}
	return b.isMergedResult(i) || b.isEmptyModelTurn(i) || b.isEchoedIdle(i) ||
		b.isAskUserPlumbing(i) || b.isFoldedAnswer(i)
}

// precedingTurnText returns the text of the last model_turn before seq.
func (b *builder) precedingTurnText(seq int64) string {
	last := ""
	for _, ev := range b.evs {
		if ev.Seq >= seq {
			break
		}
		if ev.Type == "model_turn" {
			if t := firstField(ev, "text"); strings.TrimSpace(t) != "" {
				last = t
			}
		}
	}
	return last
}

// dropDuplicatePrefix removes a leading occurrence of prev from s (trimmed).
func dropDuplicatePrefix(s, prev string) string {
	ts, tp := strings.TrimSpace(s), strings.TrimSpace(prev)
	if tp == "" {
		return s
	}
	if ts == tp {
		return ""
	}
	if strings.HasPrefix(ts, tp) {
		return strings.TrimSpace(ts[len(tp):])
	}
	return s
}

// --- data helpers (ported from tui.go) ---

func dataField(ev *v1.Event, key string) string {
	if ev == nil || ev.DataJson == "" {
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

func floatField(ev *v1.Event, key string) float64 {
	if ev == nil || ev.DataJson == "" {
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

func dataList(ev *v1.Event, key string) []string {
	if ev == nil || ev.DataJson == "" {
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

type question struct {
	prompt  string
	options []string
}

// dataQuestions parses the `questions` field of a question_asked event.
func dataQuestions(ev *v1.Event) []question {
	if ev == nil || ev.DataJson == "" {
		return nil
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return nil
	}
	raw, ok := mp["questions"].([]any)
	if !ok {
		return nil
	}
	var out []question
	for _, item := range raw {
		qm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		prompt, _ := qm["question"].(string)
		if strings.TrimSpace(prompt) == "" {
			continue
		}
		q := question{prompt: prompt}
		if opts, ok := qm["options"].([]any); ok {
			for _, o := range opts {
				if s, ok := o.(string); ok && strings.TrimSpace(s) != "" {
					q.options = append(q.options, s)
				}
			}
		}
		out = append(out, q)
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

// usage is a compact per-turn token tally.
type usage struct {
	in, out, cacheR, cacheW, total int
}

// eventUsage extracts per-turn token usage and the logical model name from a
// model_turn event's data JSON.
func eventUsage(ev *v1.Event) (usage, string) {
	if ev == nil || ev.DataJson == "" {
		return usage{}, ""
	}
	var mp map[string]any
	if json.Unmarshal([]byte(ev.DataJson), &mp) != nil {
		return usage{}, ""
	}
	name, _ := mp["model_name"].(string)
	u, _ := mp["usage"].(map[string]any)
	if u == nil {
		return usage{}, name
	}
	num := func(k string) int {
		if f, ok := u[k].(float64); ok {
			return int(f)
		}
		return 0
	}
	return usage{
		in:     num("input"),
		out:    num("output"),
		cacheR: num("cache_read"),
		cacheW: num("cache_write"),
		total:  num("total"),
	}, name
}

// deliveredSeq extracts the queued-echo seq a user_input_delivered event refers
// to (spec §18.7).
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

func deliveredSeqSet(evs []*v1.Event) map[int64]bool {
	set := map[int64]bool{}
	for _, ev := range evs {
		if seq, ok := deliveredSeq(ev); ok {
			set[seq] = true
		}
	}
	return set
}

// argSummary picks the most useful single argument to summarise a tool call.
func argSummary(call *v1.Event) string {
	for _, k := range []string{"file_path", "path", "pattern", "command", "query", "url", "task_id"} {
		if v := argField(call, k); v != "" {
			return v
		}
	}
	return oneLine(dataField(call, "args"), 100)
}

// argField unmarshals a tool_call's args JSON (itself a JSON string) and returns
// the named string field, or "".
func argField(call *v1.Event, key string) string {
	if call == nil {
		return ""
	}
	args := dataField(call, "args")
	if args == "" {
		return ""
	}
	var mp map[string]any
	if json.Unmarshal([]byte(args), &mp) != nil {
		return ""
	}
	if v, ok := mp[key].(string); ok {
		return v
	}
	return ""
}

// detailLine renders a terse one-liner for bookkeeping event types, mirroring
// the TUI's detailLine. Returns "" for types the export renders elsewhere or
// omits.
func detailLine(ev *v1.Event) string {
	switch ev.Type {
	case "subagent_spawned":
		return "subagent spawned: " + strings.TrimSpace(dataField(ev, "role")+" "+dataField(ev, "model"))
	case "subagent_finished":
		return "subagent finished: " + strings.TrimSpace(dataField(ev, "role")+" "+dataField(ev, "model"))
	case "job_started":
		return "job " + strings.TrimSpace(dataField(ev, "id")+" "+oneLine(dataField(ev, "label"), 80)) + " · running"
	case "job_finished":
		return "job " + strings.TrimSpace(dataField(ev, "id")+" "+oneLine(dataField(ev, "label"), 80)+" · "+dataField(ev, "status"))
	case "job_notified":
		return "job notified: " + oneLine(dataField(ev, "text"), 120)
	case "mode_changed":
		return "mode changed: " + dataField(ev, "from") + " → " + dataField(ev, "to")
	case "interrupted":
		return "interrupted"
	case "resumed":
		return "resumed"
	case "budget_warning":
		return "budget warning — " + budgetSummary(ev)
	case "budget_exceeded":
		suffix := ""
		switch dataField(ev, "action") {
		case "halt":
			suffix = " — halting (wrap up current task)"
		case "continue":
			suffix = " — continuing past cap (confirmed)"
		}
		return "budget reached — " + budgetSummary(ev) + suffix
	case "workstream_created":
		return "workstream created: " + strings.TrimSpace(dataField(ev, "workstream")+" "+dataField(ev, "branch"))
	case "workstream_merged":
		return "workstream merged: " + strings.TrimSpace(dataField(ev, "workstream"))
	case "workstream_abandoned":
		return "workstream abandoned: " + strings.TrimSpace(dataField(ev, "workstream"))
	}
	if strings.HasPrefix(ev.Type, "workstream_") {
		return strings.ReplaceAll(ev.Type, "_", " ") + ": " + strings.TrimSpace(dataField(ev, "workstream"))
	}
	return ""
}

func budgetSummary(ev *v1.Event) string {
	tokens := int64(floatField(ev, "tokens"))
	tokenCap := int64(floatField(ev, "token_cap"))
	cost := floatField(ev, "cost")
	costCap := floatField(ev, "cost_cap")
	var parts []string
	if tokenCap > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d tok", tokens, tokenCap))
	}
	if costCap > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f/$%.2f", cost, costCap))
	}
	return strings.Join(parts, ", ")
}

// durText returns a compact duration string (e.g. "1.2s") for an event's
// duration_ms, or "".
func durText(ev *v1.Event) string {
	if ev == nil {
		return ""
	}
	ms := int64(floatField(ev, "duration_ms"))
	if ms <= 0 {
		return ""
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// --- string/markdown helpers ---

func oneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	return trunc(s, n)
}

// oneLineKeep collapses newlines to spaces but does not truncate (for question
// prompts, which should be shown in full).
func oneLineKeep(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		n = 1
	}
	return string(r[:n]) + "…"
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func isSub(actor string) bool {
	return actor == "implementer" || strings.HasPrefix(actor, "reviewer")
}

// code wraps s as inline markdown code, neutralising any backticks it contains.
func code(s string) string {
	if s == "" {
		return ""
	}
	return "`" + strings.ReplaceAll(s, "`", "'") + "`"
}

// blockquote prefixes every line of s with "> ".
func blockquote(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if ln == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + ln
		}
	}
	return strings.Join(lines, "\n")
}

// answerText renders a folded answer, italicising the empty case.
func answerText(a string) string {
	a = strings.TrimSpace(a)
	if a == "" {
		return "_(no answer)_"
	}
	return oneLineKeep(a)
}

// fenced wraps content in a fenced code block, extending the fence when the
// content itself contains a run of backticks.
func fenced(content, lang string) string {
	fence := "```"
	for strings.Contains(content, fence) {
		fence += "`"
	}
	return fence + lang + "\n" + content + "\n" + fence
}

// prettyJSON re-indents a JSON string for readability; returns the input
// unchanged when it is not valid JSON.
func prettyJSON(s string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		return s
	}
	return buf.String()
}

// commas formats an int64 with thousands separators.
func commas(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
