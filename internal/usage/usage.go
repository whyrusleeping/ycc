// Package usage aggregates captured per-turn token usage (spec §20.1) across a
// workspace's sessions and produces the "detailed cost breakdown by backlog task
// over time" (spec §20.3, §20.5). The session event logs are the source of truth:
// the breakdown is recomputed by scanning and reducing every events.jsonl, never
// kept as a separate ledger. It joins per-turn usage with the session's task
// focus (spec §20.2) and per-model pricing (spec §20.4) so cost can be grouped by
// task × model × day, with priced dollars where pricing is configured and token
// counts only where it is not.
package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
)

// Tokens is a token-count breakdown by class, mirroring event.Usage but used as
// the accumulator type for aggregation.
type Tokens struct {
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
	Total      int
}

func (t *Tokens) addUsage(u event.Usage) {
	t.Input += u.Input
	t.Output += u.Output
	t.CacheRead += u.CacheRead
	t.CacheWrite += u.CacheWrite
	t.Total += u.Total
}

func (t *Tokens) add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
	t.Total += o.Total
}

func (t Tokens) usage() event.Usage {
	return event.Usage{Input: t.Input, Output: t.Output, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite, Total: t.Total}
}

// Entry is one reduced (session, task, model, agent, day) bucket of token usage.
// Task "" means usage that occurred before any task_focus ("unattributed");
// Model "" means the turn did not record a model name. Agent is the raw event
// actor that spent the tokens (e.g. "coordinator", "implementer",
// "reviewer:gpt"); see AgentRole for the collapsed role used by the agent
// grouping dimension.
type Entry struct {
	Session string
	Task    string
	Model   string
	Agent   string
	Day     string // YYYY-MM-DD in UTC
	Tokens  Tokens
}

// AgentRole collapses a raw event actor to its role: the segment before the
// first ":". So "reviewer:gpt" and "reviewer:glm" both become "reviewer", while
// "coordinator" and "implementer" pass through unchanged. This is the value used
// by the DimAgent grouping dimension so the default agent view separates the
// coordinator, the implementer, and the reviewers as a group; pair it with
// DimModel to split reviewers per model.
func AgentRole(actor string) string {
	if i := strings.IndexByte(actor, ':'); i >= 0 {
		return actor[:i]
	}
	return actor
}

// Dim names a grouping dimension for the breakdown.
type Dim string

const (
	DimTask    Dim = "task"
	DimModel   Dim = "model"
	DimSession Dim = "session"
	DimAgent   Dim = "agent"
	DimDay     Dim = "day"
)

// ParseDim resolves a dimension name, returning an error for unknown values.
func ParseDim(s string) (Dim, error) {
	switch Dim(s) {
	case DimTask, DimModel, DimSession, DimAgent, DimDay:
		return Dim(s), nil
	default:
		return "", fmt.Errorf("unknown group-by dimension %q (want task|model|session|agent|day)", s)
	}
}

// Pricer resolves per-model pricing; satisfied by *config.Registry.
type Pricer interface {
	PricingFor(name string) config.Pricing
}

// PriceStatus describes whether a grouped row could be fully priced.
type PriceStatus string

const (
	StatusPriced   PriceStatus = "priced"
	StatusUnpriced PriceStatus = "unpriced"
	StatusPartial  PriceStatus = "partial"
)

// Row is one grouped line of the breakdown. Only the dimensions selected in the
// Options.GroupBy carry values; unselected dimension fields are "".
type Row struct {
	Task    string
	Model   string
	Session string
	Agent   string
	Day     string
	Tokens  Tokens
	Cost    float64
	Status  PriceStatus
}

// Options control aggregation: which dimensions to group by and an optional
// inclusive date range (UTC YYYY-MM-DD).
type Options struct {
	GroupBy []Dim
	Since   time.Time
	Until   time.Time
}

// Result is the aggregated breakdown for a workspace.
type Result struct {
	Workspace string
	Rows      []Row
	Total     Row
}

// ReduceEvents folds one session's events into per-(task,model,agent,day)
// entries, attributing each model_turn to the most recent task_focus and to the
// actor that emitted it (the agent that spent the tokens).
func ReduceEvents(sessionID string, events []event.Event) []Entry {
	type key struct{ task, model, agent, day string }
	buckets := map[key]*Entry{}
	var order []key
	focus := ""
	for _, ev := range events {
		switch ev.Type {
		case event.TaskFocus:
			focus = stringField(ev.Data, "task")
		case event.ModelTurn:
			u := usageFromData(ev.Data["usage"])
			model := stringField(ev.Data, "model_name")
			day := ev.TS.UTC().Format("2006-01-02")
			k := key{task: focus, model: model, agent: ev.Actor, day: day}
			e, ok := buckets[k]
			if !ok {
				e = &Entry{Session: sessionID, Task: focus, Model: model, Agent: ev.Actor, Day: day}
				buckets[k] = e
				order = append(order, k)
			}
			e.Tokens.addUsage(u)
		}
	}
	out := make([]Entry, 0, len(order))
	for _, k := range order {
		out = append(out, *buckets[k])
	}
	return out
}

// usageFromData extracts an event.Usage from a model_turn's "usage" field. It
// accepts both an in-memory event.Usage/​*event.Usage value and a JSON-decoded
// map[string]any (numbers as float64), mirroring event.usageTotal so both the
// live and on-disk representations reduce identically.
func usageFromData(v any) event.Usage {
	switch u := v.(type) {
	case event.Usage:
		return u
	case *event.Usage:
		if u != nil {
			return *u
		}
	case map[string]any:
		return event.Usage{
			Input:      intField(u, "input"),
			Output:     intField(u, "output"),
			CacheRead:  intField(u, "cache_read"),
			CacheWrite: intField(u, "cache_write"),
			Total:      intField(u, "total"),
		}
	}
	return event.Usage{}
}

func intField(m map[string]any, k string) int {
	switch n := m[k].(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func stringField(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

// Scan reads every session event log under <workspace>/.ycc/sessions/*/events.jsonl
// and returns the reduced entries (each tagged with its session id). A missing
// sessions directory yields nil entries (not an error); a corrupt log line is a
// hard error wrapping the path.
func Scan(workspace string) ([]Entry, error) {
	glob := filepath.Join(workspace, ".ycc", "sessions", "*", "events.jsonl")
	paths, err := filepath.Glob(glob)
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []Entry
	for _, path := range paths {
		id := filepath.Base(filepath.Dir(path))
		evs, err := readEvents(path)
		if err != nil {
			return nil, err
		}
		out = append(out, ReduceEvents(id, evs)...)
	}
	return out, nil
}

func readEvents(path string) ([]event.Event, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []event.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("corrupt event log %s: %w", path, err)
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}

// Aggregate groups entries by the selected dimensions (default: task), filters
// by the optional date range, prices each entry via pricer, and returns the
// sorted rows plus a project total. A nil pricer treats every model as unpriced.
func Aggregate(entries []Entry, pricer Pricer, opts Options) Result {
	groupBy := opts.GroupBy
	if len(groupBy) == 0 {
		groupBy = []Dim{DimTask}
	}
	since := ""
	if !opts.Since.IsZero() {
		since = opts.Since.UTC().Format("2006-01-02")
	}
	until := ""
	if !opts.Until.IsZero() {
		until = opts.Until.UTC().Format("2006-01-02")
	}

	groups := map[string]*rowAgg{}
	var order []string
	var total rowAgg

	for _, e := range entries {
		if since != "" && e.Day < since {
			continue
		}
		if until != "" && e.Day > until {
			continue
		}
		key := groupKey(e, groupBy)
		g, ok := groups[key]
		if !ok {
			g = &rowAgg{row: rowFor(e, groupBy)}
			groups[key] = g
			order = append(order, key)
		}
		g.accumulate(e, pricer)
		total.accumulate(e, pricer)
	}

	rows := make([]Row, 0, len(order))
	for _, key := range order {
		g := groups[key]
		rows = append(rows, finalizeRow(g.row, g.cost, g.priced, g.unpriced))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Tokens.Total != rows[j].Tokens.Total {
			return rows[i].Tokens.Total > rows[j].Tokens.Total
		}
		return rowSortKey(rows[i], groupBy) < rowSortKey(rows[j], groupBy)
	})

	totalRow := finalizeRow(total.row, total.cost, total.priced, total.unpriced)
	return Result{Rows: rows, Total: totalRow}
}

// rowAgg accumulates tokens + cost + priced/unpriced counts for a group.
type rowAgg struct {
	row      Row
	cost     float64
	priced   int
	unpriced int
}

func (g *rowAgg) accumulate(e Entry, pricer Pricer) {
	g.row.Tokens.add(e.Tokens)
	var pr config.Pricing
	if pricer != nil {
		pr = pricer.PricingFor(e.Model)
	}
	cost, priced := pr.Cost(e.Tokens.usage())
	if priced {
		g.cost += cost
		g.priced++
	} else {
		g.unpriced++
	}
}

func finalizeRow(row Row, cost float64, priced, unpriced int) Row {
	row.Cost = cost
	switch {
	case priced > 0 && unpriced == 0:
		row.Status = StatusPriced
	case priced == 0:
		row.Status = StatusUnpriced
	default:
		row.Status = StatusPartial
	}
	return row
}

func groupKey(e Entry, dims []Dim) string {
	parts := make([]string, len(dims))
	for i, d := range dims {
		parts[i] = dimValue(e, d)
	}
	return strings.Join(parts, "\x00")
}

func rowFor(e Entry, dims []Dim) Row {
	var r Row
	for _, d := range dims {
		switch d {
		case DimTask:
			r.Task = e.Task
		case DimModel:
			r.Model = e.Model
		case DimSession:
			r.Session = e.Session
		case DimAgent:
			r.Agent = AgentRole(e.Agent)
		case DimDay:
			r.Day = e.Day
		}
	}
	return r
}

func dimValue(e Entry, d Dim) string {
	switch d {
	case DimTask:
		return e.Task
	case DimModel:
		return e.Model
	case DimSession:
		return e.Session
	case DimAgent:
		return AgentRole(e.Agent)
	case DimDay:
		return e.Day
	}
	return ""
}

func rowSortKey(r Row, dims []Dim) string {
	parts := make([]string, len(dims))
	for i, d := range dims {
		switch d {
		case DimTask:
			parts[i] = r.Task
		case DimModel:
			parts[i] = r.Model
		case DimSession:
			parts[i] = r.Session
		case DimAgent:
			parts[i] = r.Agent
		case DimDay:
			parts[i] = r.Day
		}
	}
	return strings.Join(parts, "\x00")
}

// FormatWorkLogLine renders a one-line usage/cost summary for a task's work log
// (spec §6.2) so per-task cost accrues in the backlog across sessions.
func FormatWorkLogLine(r Row) string {
	return "usage: " + formatTokensCost(r)
}

// formatTokensCost renders the token breakdown and cost suffix for a row,
// without the "usage: " prefix, e.g.:
//
//	"7,645 tok (in 30, out 7,615, cache_r 273,570, cache_w 19,750) · $0.1234"
//
// The cost suffix is " · $%.4f" when priced, " · cost n/a (unpriced)" when
// unpriced, and " · $%.4f (partial pricing)" when partially priced.
func formatTokensCost(r Row) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s tok (in %s, out %s, cache_r %s, cache_w %s)",
		commas(r.Tokens.Total), commas(r.Tokens.Input), commas(r.Tokens.Output),
		commas(r.Tokens.CacheRead), commas(r.Tokens.CacheWrite))
	switch r.Status {
	case StatusUnpriced:
		b.WriteString(" · cost n/a (unpriced)")
	case StatusPartial:
		fmt.Fprintf(&b, " · $%.4f (partial pricing)", r.Cost)
	default:
		fmt.Fprintf(&b, " · $%.4f", r.Cost)
	}
	return b.String()
}

// AgentRows groups entries by RAW actor (Entry.Agent, NOT the collapsed
// AgentRole — so reviewer:gpt and reviewer:claude stay distinct), prices each,
// drops zero-token actors, and returns rows sorted by Tokens.Total desc
// (tie-break by Agent name asc). Row.Agent holds the raw actor.
func AgentRows(entries []Entry, pricer Pricer) []Row {
	groups := map[string]*rowAgg{}
	var order []string
	for _, e := range entries {
		g, ok := groups[e.Agent]
		if !ok {
			g = &rowAgg{row: Row{Agent: e.Agent}}
			groups[e.Agent] = g
			order = append(order, e.Agent)
		}
		g.accumulate(e, pricer)
	}
	rows := make([]Row, 0, len(order))
	for _, agent := range order {
		g := groups[agent]
		if g.row.Tokens.Total == 0 {
			continue
		}
		rows = append(rows, finalizeRow(g.row, g.cost, g.priced, g.unpriced))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Tokens.Total != rows[j].Tokens.Total {
			return rows[i].Tokens.Total > rows[j].Tokens.Total
		}
		return rows[i].Agent < rows[j].Agent
	})
	return rows
}

// FormatWorkLogSummary renders the multi-line per-task usage summary: the
// aggregate line (FormatWorkLogLine(total)) followed by an indented per-agent
// breakdown, one line per actor: "  <actor>: " + formatTokensCost(row).
// Reviewers appear individually by name. When there are fewer than 2 agent rows
// the breakdown adds no information, so just return FormatWorkLogLine(total).
func FormatWorkLogSummary(total Row, agents []Row) string {
	line := FormatWorkLogLine(total)
	if len(agents) < 2 {
		return line
	}
	var b strings.Builder
	b.WriteString(line)
	for _, a := range agents {
		b.WriteString("\n  ")
		b.WriteString(a.Agent)
		b.WriteString(": ")
		b.WriteString(formatTokensCost(a))
	}
	return b.String()
}

// Render writes a readable aligned table of the breakdown to w.
func Render(w io.Writer, res Result, groupBy []Dim) {
	if len(groupBy) == 0 {
		groupBy = []Dim{DimTask}
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	header := make([]string, 0, len(groupBy)+5)
	for _, d := range groupBy {
		header = append(header, title(string(d)))
	}
	header = append(header, "Input", "Output", "Cache R", "Cache W", "Total", "Cost")
	fmt.Fprintln(tw, strings.Join(header, "\t"))

	partial := res.Total.Status == StatusPartial
	for _, r := range res.Rows {
		fmt.Fprintln(tw, rowLine(r, groupBy))
		if r.Status == StatusPartial {
			partial = true
		}
	}
	// TOTAL row spans the grouping columns.
	totalCells := make([]string, len(groupBy))
	totalCells[0] = "TOTAL"
	for i := 1; i < len(totalCells); i++ {
		totalCells[i] = ""
	}
	totalCells = append(totalCells,
		commas(res.Total.Tokens.Input), commas(res.Total.Tokens.Output),
		commas(res.Total.Tokens.CacheRead), commas(res.Total.Tokens.CacheWrite),
		commas(res.Total.Tokens.Total), costCell(res.Total))
	fmt.Fprintln(tw, strings.Join(totalCells, "\t"))
	tw.Flush()
	if partial {
		fmt.Fprintln(w, "* partial pricing (some models unpriced)")
	}
}

func rowLine(r Row, groupBy []Dim) string {
	cells := make([]string, 0, len(groupBy)+5)
	for _, d := range groupBy {
		v := ""
		switch d {
		case DimTask:
			v = r.Task
			if v == "" {
				v = "(unattributed)"
			}
		case DimModel:
			v = r.Model
			if v == "" {
				v = "(unknown)"
			}
		case DimSession:
			v = r.Session
		case DimAgent:
			v = r.Agent
			if v == "" {
				v = "(unknown)"
			}
		case DimDay:
			v = r.Day
		}
		cells = append(cells, v)
	}
	cells = append(cells,
		commas(r.Tokens.Input), commas(r.Tokens.Output),
		commas(r.Tokens.CacheRead), commas(r.Tokens.CacheWrite),
		commas(r.Tokens.Total), costCell(r))
	return strings.Join(cells, "\t")
}

func costCell(r Row) string {
	switch r.Status {
	case StatusUnpriced:
		return "—"
	case StatusPartial:
		return fmt.Sprintf("$%.4f*", r.Cost)
	default:
		return fmt.Sprintf("$%.4f", r.Cost)
	}
}

// commas formats an integer with thousands separators.
func commas(n int) string {
	s := strconv.Itoa(n)
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
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

// title upper-cases the first letter of s (ASCII), for column headers.
func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
