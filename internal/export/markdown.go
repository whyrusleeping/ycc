// Package export renders a session's event log into shareable markdown. It
// reuses the transcript-folding semantics the TUI applies (spec §18.3, §18.6):
// tool_result folds into its tool_call as one collapsed line, an ask_user
// round-trip collapses to a single Q/A block, empty (tool-calls-only)
// model_turns and echoed session_idle rows are dropped, and the final report is
// pulled out into its own section. The result is a self-contained markdown
// document suitable for pasting into a PR, issue, or chat.
package export

import (
	"fmt"
	"sort"
	"strings"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// Options controls how Markdown renders a transcript.
type Options struct {
	// SessionID is used in the document header.
	SessionID string
	// Full expands each tool call with its argument and result payloads instead
	// of the folded, one-line summary.
	Full bool
	// Usage, when supplied (from GetUsage grouped by session,model and filtered
	// to this session), drives the usage/cost footer and adds a Cost column.
	// When nil the footer falls back to token totals derived from model_turn
	// events (no cost).
	Usage []*v1.UsageRow
	// UsageTotal is the aggregate row for the Usage table's TOTAL line; when nil
	// it is computed by summing Usage rows.
	UsageTotal *v1.UsageRow
}

// Markdown renders the given events (the same []*v1.Event GetSessionTranscript
// returns) into a markdown transcript string.
func Markdown(events []*v1.Event, opts Options) string {
	// Defensively drop transient (broadcast-only) events — they carry seq 0 and
	// are never part of the persisted transcript.
	evs := make([]*v1.Event, 0, len(events))
	for _, ev := range events {
		if ev != nil && !ev.Transient {
			evs = append(evs, ev)
		}
	}
	b := &builder{evs: evs, full: opts.Full}

	var out strings.Builder
	out.WriteString("# Session " + opts.SessionID + "\n")
	if meta := b.metadataLine(); meta != "" {
		out.WriteString("\n" + meta + "\n")
	}

	delivered := deliveredSeqSet(evs)
	body := b.renderBody(delivered)
	if strings.TrimSpace(body) != "" {
		out.WriteString("\n" + body)
	}

	if fr := b.finalReport(); fr != "" {
		out.WriteString("\n## Final report\n\n" + fr + "\n")
	}

	if footer := b.usageFooter(opts); footer != "" {
		out.WriteString("\n" + footer)
	}

	return out.String()
}

type builder struct {
	evs  []*v1.Event
	full bool
}

// renderBody walks the events and emits one markdown block per rendered row,
// honouring the fold/hide rules. It tracks the previous rendered actor so a
// model_turn or sub-agent tool call is labelled only at the start of its run.
func (b *builder) renderBody(delivered map[int64]bool) string {
	var blocks []string
	prevActor := ""
	haveRendered := false
	for i, ev := range b.evs {
		if b.hiddenRow(i) {
			continue
		}
		first := !haveRendered || ev.Actor != prevActor
		block := b.renderEvent(i, first, delivered)
		if strings.TrimSpace(block) == "" {
			continue
		}
		blocks = append(blocks, strings.TrimRight(block, "\n"))
		prevActor = ev.Actor
		haveRendered = true
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// renderEvent renders a single event's markdown block (or "" when it renders
// nothing).
func (b *builder) renderEvent(i int, first bool, delivered map[int64]bool) string {
	ev := b.evs[i]
	switch ev.Type {
	case "user_input":
		txt := strings.TrimSpace(firstField(ev, "text"))
		if txt == "" {
			return ""
		}
		suffix := ""
		if dataField(ev, "queued") == "true" && !delivered[ev.Seq] {
			suffix = " _(queued, undelivered)_"
		}
		return "**user:**" + suffix + "\n\n" + txt

	case "model_turn":
		txt := strings.TrimSpace(firstField(ev, "text"))
		if txt == "" {
			return ""
		}
		if first {
			return "**" + ev.Actor + ":**\n\n" + txt
		}
		return txt

	case "thinking":
		if !b.full {
			return ""
		}
		txt := strings.TrimSpace(dataField(ev, "text"))
		if txt == "" {
			return ""
		}
		return "> _reasoning (" + ev.Actor + "):_\n" + blockquote(txt)

	case "tool_call":
		return b.renderToolCall(i, first)

	case "tool_result":
		// A tool_result reaches here only when it was NOT folded into a preceding
		// call (e.g. an orphaned result or an errored ask_user result). Render it
		// as a standalone bullet so failures are never dropped.
		return b.renderOrphanResult(ev)

	case "question_asked":
		return b.renderQuestion(ev)

	case "review_submitted":
		return b.renderReview(ev)

	case "commit_made":
		sha := short(dataField(ev, "sha"))
		msg := oneLine(dataField(ev, "message"), 200)
		return "- ● commit `" + sha + "` — " + msg

	case "plan_proposed":
		plan := strings.TrimSpace(dataField(ev, "plan"))
		if plan == "" {
			return ""
		}
		head := "**Plan"
		if task := dataField(ev, "task"); task != "" {
			head += " (task " + task + ")"
		}
		head += ":**"
		return head + "\n\n" + blockquote(plan)

	case "session_idle":
		txt := b.idleReport(ev)
		if txt == "" {
			return ""
		}
		return txt

	case "session_error":
		return b.renderError(ev)

	default:
		if line := detailLine(ev); line != "" {
			return "- _" + line + "_"
		}
		return ""
	}
}

// renderToolCall renders the collapsed one-line tool summary (default) plus, in
// --full mode, fenced argument and result payloads.
func (b *builder) renderToolCall(i int, first bool) string {
	call := b.evs[i]
	var res *v1.Event
	if ri := b.mergedResultIdx(i); ri >= 0 {
		res = b.evs[ri]
	}

	glyph := "○"
	switch {
	case res == nil:
		glyph = "○"
	case dataField(res, "error") == "true":
		glyph = "✗"
	default:
		glyph = "✓"
	}

	line := "- " + glyph + " `" + dataField(call, "name") + "`"
	if isSub(call.Actor) && first {
		line += " _(" + call.Actor + ")_"
	}
	if s := argSummary(call); s != "" {
		line += " " + code(oneLine(s, 100))
	}
	if d := durText(res); d != "" {
		line += " (" + d + ")"
	} else if d := durText(call); d != "" {
		line += " (" + d + ")"
	}

	if !b.full {
		return line
	}

	// Full mode: append the argument and result payloads as fenced blocks.
	var sb strings.Builder
	sb.WriteString(line)
	if args := strings.TrimSpace(dataField(call, "args")); args != "" {
		sb.WriteString("\n\n" + fenced(prettyJSON(args), "json"))
	}
	if res != nil {
		if r := strings.TrimSpace(dataField(res, "result")); r != "" {
			sb.WriteString("\n\n" + fenced(r, ""))
		}
	}
	return sb.String()
}

// renderOrphanResult renders a tool_result that was not folded into a call.
func (b *builder) renderOrphanResult(res *v1.Event) string {
	glyph := "✓"
	if dataField(res, "error") == "true" {
		glyph = "✗"
	}
	line := "- " + glyph + " result"
	r := strings.TrimSpace(dataField(res, "result"))
	if r == "" {
		return line
	}
	if b.full {
		return line + "\n\n" + fenced(r, "")
	}
	return line + " " + code(oneLine(r, 100))
}

// renderQuestion renders a question_asked event as the single canonical block
// for the whole ask_user exchange: the question(s) with the folded answer(s)
// beneath (§18.3's one-block rule).
func (b *builder) renderQuestion(ev *v1.Event) string {
	ans := b.answerEventFor(ev)
	auto := ans != nil && dataField(ans, "auto") == "true"

	if qs := dataQuestions(ev); len(qs) > 0 {
		var answers []string
		if ans != nil && !auto {
			answers = dataList(ans, "answers")
		}
		var sb strings.Builder
		sb.WriteString("**Questions:**\n")
		for i, q := range qs {
			sb.WriteString(fmt.Sprintf("\n%d. %s\n", i+1, oneLineKeep(q.prompt)))
			switch {
			case auto:
				sb.WriteString("   → _auto-answered (autonomous mode)_\n")
			case ans != nil:
				a := ""
				if i < len(answers) {
					a = answers[i]
				}
				sb.WriteString("   → " + answerText(a) + "\n")
			}
		}
		return strings.TrimRight(sb.String(), "\n")
	}

	q := strings.TrimSpace(firstField(ev, "question"))
	if q == "" {
		return ""
	}
	block := "**Q:** " + oneLineKeep(q)
	switch {
	case auto:
		block += "\n\n> → _auto-answered (autonomous mode)_"
	case ans != nil:
		block += "\n\n> → " + answerText(dataField(ans, "answer"))
	}
	return block
}

func (b *builder) renderReview(ev *v1.Event) string {
	verdict := strings.ToUpper(strings.TrimSpace(dataField(ev, "verdict")))
	model := dataField(ev, "model")
	line := "- § review"
	if model != "" {
		line += " (" + model + ")"
	}
	line += ": **" + verdict + "**"
	if s := strings.TrimSpace(dataField(ev, "summary")); s != "" {
		if b.full {
			return line + "\n\n" + blockquote(s)
		}
		line += " — " + oneLine(s, 200)
	}
	return line
}

func (b *builder) renderError(ev *v1.Event) string {
	msg := strings.TrimSpace(dataField(ev, "msg"))
	if msg == "" {
		return "**error:**"
	}
	if !b.full {
		// Cap to the first few lines by default; the full text is available with
		// --full.
		lines := strings.Split(msg, "\n")
		if len(lines) > 3 {
			lines = append(lines[:3], "…")
		}
		msg = strings.Join(lines, "\n")
	}
	return "**error:**\n\n" + fenced(msg, "")
}

// idleReport returns the de-duplicated report text for a session_idle event
// (the portion it adds beyond the preceding final model_turn), or "".
func (b *builder) idleReport(ev *v1.Event) string {
	txt := strings.TrimSpace(firstField(ev, "report"))
	if txt == "" {
		return ""
	}
	if prev := b.precedingTurnText(ev.Seq); prev != "" {
		txt = dropDuplicatePrefix(txt, prev)
	}
	return strings.TrimSpace(txt)
}

// finalReport returns the full report text of the LAST session_idle event, or
// "" when there is none.
func (b *builder) finalReport() string {
	for i := len(b.evs) - 1; i >= 0; i-- {
		if b.evs[i].Type == "session_idle" {
			if r := strings.TrimSpace(firstField(b.evs[i], "report")); r != "" {
				return r
			}
		}
	}
	return ""
}

// metadataLine builds a one-line italic metadata summary from the
// session_started event, best-effort.
func (b *builder) metadataLine() string {
	for _, ev := range b.evs {
		if ev.Type != "session_started" {
			continue
		}
		var parts []string
		if v := dataField(ev, "mode"); v != "" {
			parts = append(parts, "mode: "+v)
		}
		if v := dataField(ev, "workspace"); v != "" {
			parts = append(parts, "workspace: "+v)
		}
		if v := dataField(ev, "interaction_level"); v != "" {
			parts = append(parts, "level: "+v)
		}
		if v := ev.Ts; v != "" {
			parts = append(parts, "started: "+v)
		}
		if len(parts) == 0 {
			return ""
		}
		return "_" + strings.Join(parts, " · ") + "_"
	}
	return ""
}

// --- usage/cost footer ---

func (b *builder) usageFooter(opts Options) string {
	if len(opts.Usage) > 0 {
		return b.usageFooterFromRows(opts)
	}
	return b.usageFooterFromEvents()
}

func (b *builder) usageFooterFromRows(opts Options) string {
	rows := opts.Usage
	var sb strings.Builder
	sb.WriteString("## Usage\n\n")
	sb.WriteString("| Model | Input | Output | Cache | Total | Cost |\n")
	sb.WriteString("| --- | ---: | ---: | ---: | ---: | ---: |\n")
	partial := false
	for _, r := range rows {
		model := r.Model
		if model == "" {
			model = "(unknown)"
		}
		cache := r.CacheRead + r.CacheWrite
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n",
			model, commas(r.Input), commas(r.Output), commas(cache), commas(r.Total), costCell(r)))
		if r.PriceStatus == "partial" {
			partial = true
		}
	}
	total := opts.UsageTotal
	if total == nil {
		total = sumRows(rows)
	}
	if total != nil {
		cache := total.CacheRead + total.CacheWrite
		sb.WriteString(fmt.Sprintf("| **TOTAL** | %s | %s | %s | %s | %s |\n",
			commas(total.Input), commas(total.Output), commas(cache), commas(total.Total), costCell(total)))
		if total.PriceStatus == "partial" {
			partial = true
		}
	}
	if partial {
		sb.WriteString("\n_\\* partial pricing (some models unpriced)_\n")
	}
	return sb.String()
}

func (b *builder) usageFooterFromEvents() string {
	type acc struct{ in, out, cacheR, cacheW, total int }
	byModel := map[string]*acc{}
	for _, ev := range b.evs {
		if ev.Type != "model_turn" {
			continue
		}
		u, name := eventUsage(ev)
		if name == "" {
			name = "(unknown)"
		}
		if u.total == 0 && u.in == 0 && u.out == 0 && u.cacheR == 0 && u.cacheW == 0 {
			continue
		}
		a := byModel[name]
		if a == nil {
			a = &acc{}
			byModel[name] = a
		}
		a.in += u.in
		a.out += u.out
		a.cacheR += u.cacheR
		a.cacheW += u.cacheW
		a.total += u.total
	}
	if len(byModel) == 0 {
		return ""
	}
	names := make([]string, 0, len(byModel))
	for n := range byModel {
		names = append(names, n)
	}
	sort.Strings(names)

	var tin, tout, tcache, ttotal int
	var sb strings.Builder
	sb.WriteString("## Usage\n\n")
	sb.WriteString("| Model | Input | Output | Cache | Total |\n")
	sb.WriteString("| --- | ---: | ---: | ---: | ---: |\n")
	for _, n := range names {
		a := byModel[n]
		cache := a.cacheR + a.cacheW
		total := a.total
		if total == 0 {
			total = a.in + a.out + a.cacheR + a.cacheW
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
			n, commas(int64(a.in)), commas(int64(a.out)), commas(int64(cache)), commas(int64(total))))
		tin += a.in
		tout += a.out
		tcache += cache
		ttotal += total
	}
	sb.WriteString(fmt.Sprintf("| **TOTAL** | %s | %s | %s | %s |\n",
		commas(int64(tin)), commas(int64(tout)), commas(int64(tcache)), commas(int64(ttotal))))
	return sb.String()
}

func sumRows(rows []*v1.UsageRow) *v1.UsageRow {
	if len(rows) == 0 {
		return nil
	}
	t := &v1.UsageRow{}
	priced, unpriced := 0, 0
	for _, r := range rows {
		t.Input += r.Input
		t.Output += r.Output
		t.CacheRead += r.CacheRead
		t.CacheWrite += r.CacheWrite
		t.Total += r.Total
		t.Cost += r.Cost
		switch r.PriceStatus {
		case "unpriced":
			unpriced++
		default:
			priced++
		}
	}
	switch {
	case priced > 0 && unpriced == 0:
		t.PriceStatus = "priced"
	case priced > 0:
		t.PriceStatus = "partial"
	default:
		t.PriceStatus = "unpriced"
	}
	return t
}

func costCell(r *v1.UsageRow) string {
	switch r.PriceStatus {
	case "unpriced":
		return "—"
	case "partial":
		return fmt.Sprintf("$%.4f\\*", r.Cost)
	default:
		return fmt.Sprintf("$%.4f", r.Cost)
	}
}
