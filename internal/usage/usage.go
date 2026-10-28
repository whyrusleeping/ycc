// Package usage aggregates captured per-turn token usage across a
// workspace's sessions and produces the "detailed cost breakdown by backlog task
// over time". The session event logs are the source of truth:
// the breakdown is recomputed by scanning and reducing every events.jsonl, never
// kept as a separate ledger. It joins per-turn usage with the session's task
// focus and per-model pricing so cost can be grouped by
// task × model × day, with priced dollars where pricing is configured and token
// counts only where it is not.
package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// Options control aggregation: which dimensions to group by, an optional
// inclusive date range (UTC YYYY-MM-DD), and an optional exact task id filter.
type Options struct {
	GroupBy []Dim
	Since   time.Time
	Until   time.Time
	Task    string // Exact Entry.Task match after trimming; empty means no filter.
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
	r := newReducer(sessionID)
	r.add(events)
	return r.entries()
}

// reducer is ReduceEvents' incremental state, so a growing log can be folded
// in appended chunks without rereading its prefix.
type reducer struct {
	session string
	focus   string
	buckets map[entryKey]*Entry
	order   []entryKey
}

type entryKey struct{ task, model, agent, day string }

func newReducer(sessionID string) *reducer {
	return &reducer{session: sessionID, buckets: map[entryKey]*Entry{}}
}

func (r *reducer) add(events []event.Event) {
	for _, ev := range events {
		r.observe(ev.Type, ev.Actor, ev.TS, ev.Data["task"], ev.Data["model_name"], ev.Data["usage"])
	}
}

// observe folds one event given just the fields usage depends on (data.task,
// data.model_name and data.usage as decoded into any).
func (r *reducer) observe(typ event.Type, actor string, ts time.Time, task, modelName, usage any) {
	switch typ {
	case event.TaskFocus:
		r.focus, _ = task.(string)
	case event.ModelTurn:
		u := usageFromData(usage)
		model, _ := modelName.(string)
		day := ts.UTC().Format("2006-01-02")
		k := entryKey{task: r.focus, model: model, agent: actor, day: day}
		e, ok := r.buckets[k]
		if !ok {
			e = &Entry{Session: r.session, Task: r.focus, Model: model, Agent: actor, Day: day}
			r.buckets[k] = e
			r.order = append(r.order, k)
		}
		e.Tokens.addUsage(u)
	}
}

func (r *reducer) entries() []Entry {
	out := make([]Entry, 0, len(r.order))
	for _, k := range r.order {
		out = append(out, *r.buckets[k])
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

// Scan reads every session event log under <workspace>/.ycc/sessions/*/events.jsonl
// and returns the reduced entries (each tagged with its session id). A missing
// sessions directory yields nil entries (not an error); a corrupt usage record
// is a hard error wrapping the path. Repeated scans should share a Scanner.
func Scan(workspace string) ([]Entry, error) {
	return NewScanner().Scan(workspace)
}

// Scanner is Scan with a per-log cache. Usage reports are recomputed from the
// event logs, which run to gigabytes across a long-lived daemon's projects, so
// re-decoding them on every request made usage views take tens of seconds.
// Logs are append-only: an unchanged log reuses its reduced state and a
// grown one folds in only the appended records. Safe for concurrent use;
// scans are serialized so overlapping requests share the work.
type Scanner struct {
	mu   sync.Mutex
	logs map[string]*scannedLog
}

type scannedLog struct {
	info os.FileInfo // identity, size and mtime at the last read
	end  int64       // committed offset the reducer has consumed
	r    *reducer
}

// NewScanner returns an empty Scanner.
func NewScanner() *Scanner {
	return &Scanner{logs: map[string]*scannedLog{}}
}

// maxScanWorkers bounds a scan's decode parallelism (the daemon host may be
// shared, and a cold scan is a one-off burst).
const maxScanWorkers = 8

// Scan is the cached equivalent of the package-level Scan. Logs that need
// reading are decoded in parallel.
func (s *Scanner) Scan(workspace string) ([]Entry, error) {
	sessionsDir := filepath.Join(workspace, ".ycc", "sessions")
	paths, err := filepath.Glob(filepath.Join(sessionsDir, "*", "events.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	s.mu.Lock()
	defer s.mu.Unlock()

	// Workers own disjoint indexes; the map is only touched on this goroutine.
	logs := make([]*scannedLog, len(paths))
	errs := make([]error, len(paths))
	for i, path := range paths {
		logs[i] = s.logs[path]
	}
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), maxScanWorkers, len(paths)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				logs[i], errs[i] = refreshLog(paths[i], logs[i])
			}
		}()
	}
	for i := range paths {
		next <- i
	}
	close(next)
	wg.Wait()

	seen := make(map[string]bool, len(paths))
	var out []Entry
	var firstErr error
	for i, path := range paths {
		seen[path] = true
		if errs[i] != nil || logs[i] == nil {
			// A failed refresh may have partially folded new records.
			delete(s.logs, path)
			if firstErr == nil {
				firstErr = errs[i]
			}
			continue
		}
		s.logs[path] = logs[i]
		out = append(out, logs[i].r.entries()...)
	}
	// The glob is the source of truth for removals (e.g. reaped sessions).
	prefix := sessionsDir + string(os.PathSeparator)
	for path := range s.logs {
		if strings.HasPrefix(path, prefix) && !seen[path] {
			delete(s.logs, path)
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// refreshLog brings the cached state lg (nil when uncached) for one log up to
// date, rereading from the start only when the log is new, replaced, or shrank.
// It returns nil state for a log that no longer exists.
func refreshLog(path string, lg *scannedLog) (*scannedLog, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if lg != nil && os.SameFile(lg.info, info) && info.Size() >= lg.end {
		if info.Size() == lg.info.Size() && info.ModTime().Equal(lg.info.ModTime()) {
			return lg, nil
		}
	} else {
		lg = &scannedLog{r: newReducer(filepath.Base(filepath.Dir(path)))}
	}
	end, err := event.ScanLogFrom(path, lg.end, func(line []byte) error {
		if !mayAffectUsage(line) {
			return nil
		}
		var rec usageRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("corrupt event log %s: %w", path, err)
		}
		lg.r.observe(rec.Type, rec.Actor, rec.TS, rec.Data.Task, rec.Data.ModelName, rec.Data.Usage)
		return nil
	})
	if err != nil {
		return nil, err
	}
	lg.end = end
	lg.info = info
	return lg, nil
}

// usageRecord decodes only the event fields usage depends on, so large
// payloads such as model_turn text are skipped instead of materialized.
type usageRecord struct {
	TS    time.Time  `json:"ts"`
	Actor string     `json:"actor"`
	Type  event.Type `json:"type"`
	Data  struct {
		Task      any `json:"task"`
		ModelName any `json:"model_name"`
		Usage     any `json:"usage"`
	} `json:"data"`
}

// mayAffectUsage cheaply rejects log lines that cannot be a usage-relevant
// event, so tool output (the bulk of a log) is not decoded. It is a superset
// filter: the reducer still switches on the decoded type.
func mayAffectUsage(line []byte) bool {
	return bytes.Contains(line, []byte(`"model_turn"`)) || bytes.Contains(line, []byte(`"task_focus"`))
}

// Aggregate groups entries by the selected dimensions (default: task), filters
// by the optional task and date range, prices each entry via pricer, and returns
// the sorted rows plus a project total. A nil pricer treats every model as unpriced.
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
	task := strings.TrimSpace(opts.Task)

	groups := map[string]*rowAgg{}
	var order []string
	var total rowAgg

	for _, e := range entries {
		if task != "" && e.Task != task {
			continue
		}
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

// FormatWorkLogLine renders a one-line usage/cost summary so per-task cost
// accrues in the backlog across sessions.
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
