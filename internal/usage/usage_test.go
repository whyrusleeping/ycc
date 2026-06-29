package usage

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
)

// fakePricer prices a fixed set of models and leaves the rest unpriced.
type fakePricer map[string]config.Pricing

func (f fakePricer) PricingFor(name string) config.Pricing { return f[name] }

func ts(day string) time.Time {
	t, _ := time.Parse("2006-01-02", day)
	return t
}

func turn(day, model string, u event.Usage) event.Event {
	return event.Event{TS: ts(day), Type: event.ModelTurn, Data: map[string]any{
		"model_name": model, "usage": u,
	}}
}

// turnBy is like turn but tags the model_turn with the actor (agent) that spent
// the tokens, so attribution by agent role can be exercised.
func turnBy(day, model, actor string, u event.Usage) event.Event {
	ev := turn(day, model, u)
	ev.Actor = actor
	return ev
}

func focus(task string) event.Event {
	return event.Event{Type: event.TaskFocus, Data: map[string]any{"task": task}}
}

// jsonRoundTrip marshals then unmarshals events so usage comes back as the
// map[string]any (float64) representation, exercising the on-disk decode path.
func jsonRoundTrip(t *testing.T, evs []event.Event) []event.Event {
	t.Helper()
	out := make([]event.Event, len(evs))
	for i, ev := range evs {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := json.Unmarshal(b, &out[i]); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	}
	return out
}

func TestReduceEventsAttributesToFocus(t *testing.T) {
	evs := []event.Event{
		turn("2026-06-01", "claude", event.Usage{Input: 100, Output: 10, Total: 110}), // before any focus
		focus("0007"),
		turn("2026-06-01", "claude", event.Usage{Input: 200, Output: 20, Total: 220}),
		turn("2026-06-01", "claude", event.Usage{Input: 50, Output: 5, Total: 55}),
		focus("0008"),
		turn("2026-06-02", "gpt", event.Usage{Input: 300, Output: 30, Total: 330}),
	}
	for _, variant := range []struct {
		name string
		in   []event.Event
	}{
		{"in-memory", evs},
		{"json", jsonRoundTrip(t, evs)},
	} {
		t.Run(variant.name, func(t *testing.T) {
			entries := ReduceEvents("s1", variant.in)
			got := map[string]Tokens{}
			for _, e := range entries {
				if e.Session != "s1" {
					t.Fatalf("session = %q, want s1", e.Session)
				}
				got[e.Task+"/"+e.Model+"/"+e.Day] = e.Tokens
			}
			if tk := got["/claude/2026-06-01"]; tk.Total != 110 {
				t.Fatalf("unattributed total = %d, want 110", tk.Total)
			}
			if tk := got["0007/claude/2026-06-01"]; tk.Total != 275 || tk.Input != 250 {
				t.Fatalf("0007 tokens = %+v, want total 275 input 250", tk)
			}
			if tk := got["0008/gpt/2026-06-02"]; tk.Total != 330 {
				t.Fatalf("0008 total = %d, want 330", tk.Total)
			}
		})
	}
}

func sampleEntries() []Entry {
	return []Entry{
		{Session: "s1", Task: "0007", Model: "claude", Day: "2026-06-01", Tokens: Tokens{Input: 1000, Output: 100, Total: 1100}},
		{Session: "s1", Task: "0007", Model: "claude", Day: "2026-06-02", Tokens: Tokens{Input: 500, Output: 50, Total: 550}},
		{Session: "s2", Task: "0008", Model: "gpt", Day: "2026-06-02", Tokens: Tokens{Input: 2000, Output: 200, Total: 2200}},
	}
}

func pricer() fakePricer {
	return fakePricer{
		"claude": {Input: 3, Output: 15, Configured: true},
		"gpt":    {Input: 2, Output: 10, Configured: true},
	}
}

func rowByTask(rows []Row, task string) (Row, bool) {
	for _, r := range rows {
		if r.Task == task {
			return r, true
		}
	}
	return Row{}, false
}

func TestAggregateByTaskPriced(t *testing.T) {
	res := Aggregate(sampleEntries(), pricer(), Options{GroupBy: []Dim{DimTask}})
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(res.Rows))
	}
	r7, _ := rowByTask(res.Rows, "0007")
	if r7.Tokens.Total != 1650 {
		t.Fatalf("0007 total = %d, want 1650", r7.Tokens.Total)
	}
	// cost = (1500*3 + 150*15)/1e6 = (4500 + 2250)/1e6 = 0.00675
	if !approx(r7.Cost, 0.00675) {
		t.Fatalf("0007 cost = %v, want 0.00675", r7.Cost)
	}
	if r7.Status != StatusPriced {
		t.Fatalf("0007 status = %v, want priced", r7.Status)
	}
	// Sorted by total desc: 0008 (2200) before 0007 (1650).
	if res.Rows[0].Task != "0008" {
		t.Fatalf("first row = %q, want 0008", res.Rows[0].Task)
	}
	if res.Total.Tokens.Total != 3850 {
		t.Fatalf("total tokens = %d, want 3850", res.Total.Tokens.Total)
	}
}

func TestAggregateByAgentCollapsesReviewers(t *testing.T) {
	evs := []event.Event{
		focus("0007"),
		turnBy("2026-06-01", "claude", "coordinator", event.Usage{Input: 100, Output: 10, Total: 110}),
		turnBy("2026-06-01", "claude", "implementer", event.Usage{Input: 1000, Output: 100, Total: 1100}),
		turnBy("2026-06-01", "gpt", "reviewer:gpt", event.Usage{Input: 200, Output: 20, Total: 220}),
		turnBy("2026-06-01", "glm", "reviewer:glm", event.Usage{Input: 300, Output: 30, Total: 330}),
	}
	entries := ReduceEvents("s1", evs)

	// Grouping by agent collapses reviewer:gpt + reviewer:glm into one "reviewer".
	rows := Aggregate(entries, pricer(), Options{GroupBy: []Dim{DimAgent}}).Rows
	byAgent := map[string]Row{}
	for _, r := range rows {
		byAgent[r.Agent] = r
	}
	if len(byAgent) != 3 {
		t.Fatalf("agents = %v, want coordinator/implementer/reviewer", byAgent)
	}
	if rv := byAgent["reviewer"]; rv.Tokens.Total != 550 {
		t.Fatalf("reviewer total = %d, want 550 (gpt 220 + glm 330)", rv.Tokens.Total)
	}
	if byAgent["implementer"].Tokens.Total != 1100 {
		t.Fatalf("implementer total = %d, want 1100", byAgent["implementer"].Tokens.Total)
	}
	if byAgent["coordinator"].Tokens.Total != 110 {
		t.Fatalf("coordinator total = %d, want 110", byAgent["coordinator"].Tokens.Total)
	}

	// Grouping by agent+model keeps reviewers split per model.
	am := Aggregate(entries, pricer(), Options{GroupBy: []Dim{DimAgent, DimModel}}).Rows
	if len(am) != 4 {
		t.Fatalf("agent+model rows = %d, want 4", len(am))
	}
}

func TestAggregateByModelSessionDay(t *testing.T) {
	es := sampleEntries()
	if rows := Aggregate(es, pricer(), Options{GroupBy: []Dim{DimModel}}).Rows; len(rows) != 2 {
		t.Fatalf("by model rows = %d, want 2", len(rows))
	}
	if rows := Aggregate(es, pricer(), Options{GroupBy: []Dim{DimSession}}).Rows; len(rows) != 2 {
		t.Fatalf("by session rows = %d, want 2", len(rows))
	}
	// Two distinct days: 06-01 and 06-02.
	rows := Aggregate(es, pricer(), Options{GroupBy: []Dim{DimDay}}).Rows
	if len(rows) != 2 {
		t.Fatalf("by day rows = %d, want 2", len(rows))
	}
}

func TestAggregateUnpricedAndPartial(t *testing.T) {
	es := sampleEntries()
	// nil pricer => everything unpriced.
	res := Aggregate(es, nil, Options{GroupBy: []Dim{DimTask}})
	for _, r := range res.Rows {
		if r.Status != StatusUnpriced || r.Cost != 0 {
			t.Fatalf("nil pricer row %q status=%v cost=%v, want unpriced/0", r.Task, r.Status, r.Cost)
		}
	}
	if res.Total.Status != StatusUnpriced {
		t.Fatalf("total status = %v, want unpriced", res.Total.Status)
	}

	// Only claude priced; gpt unpriced -> by-task each row is fully priced or
	// fully unpriced, but the project total mixes them => partial.
	p := fakePricer{"claude": {Input: 3, Output: 15, Configured: true}}
	res = Aggregate(es, p, Options{GroupBy: []Dim{DimTask}})
	r7, _ := rowByTask(res.Rows, "0007")
	if r7.Status != StatusPriced {
		t.Fatalf("0007 status = %v, want priced", r7.Status)
	}
	r8, _ := rowByTask(res.Rows, "0008")
	if r8.Status != StatusUnpriced {
		t.Fatalf("0008 status = %v, want unpriced", r8.Status)
	}
	if res.Total.Status != StatusPartial {
		t.Fatalf("total status = %v, want partial", res.Total.Status)
	}
}

func TestAggregateDateFilter(t *testing.T) {
	es := sampleEntries()
	res := Aggregate(es, pricer(), Options{GroupBy: []Dim{DimDay}, Since: ts("2026-06-02")})
	if len(res.Rows) != 1 || res.Rows[0].Day != "2026-06-02" {
		t.Fatalf("since filter rows = %+v", res.Rows)
	}
	res = Aggregate(es, pricer(), Options{GroupBy: []Dim{DimDay}, Until: ts("2026-06-01")})
	if len(res.Rows) != 1 || res.Rows[0].Day != "2026-06-01" {
		t.Fatalf("until filter rows = %+v", res.Rows)
	}
}

func TestFormatWorkLogLine(t *testing.T) {
	priced := Row{Tokens: Tokens{Input: 8000, Output: 4000, CacheRead: 300, CacheWrite: 45, Total: 12345}, Cost: 0.1234, Status: StatusPriced}
	got := FormatWorkLogLine(priced)
	if !strings.Contains(got, "12,345 tok") || !strings.Contains(got, "$0.1234") {
		t.Fatalf("priced line = %q", got)
	}
	unpriced := Row{Tokens: Tokens{Total: 100}, Status: StatusUnpriced}
	got = FormatWorkLogLine(unpriced)
	if !strings.Contains(got, "unpriced") || strings.Contains(got, "$") {
		t.Fatalf("unpriced line = %q", got)
	}
	partial := Row{Tokens: Tokens{Total: 100}, Cost: 0.5, Status: StatusPartial}
	if got = FormatWorkLogLine(partial); !strings.Contains(got, "partial") {
		t.Fatalf("partial line = %q", got)
	}
}

func TestRenderTable(t *testing.T) {
	res := Aggregate(sampleEntries(), pricer(), Options{GroupBy: []Dim{DimTask}})
	var buf bytes.Buffer
	Render(&buf, res, []Dim{DimTask})
	out := buf.String()
	if !strings.Contains(out, "TOTAL") || !strings.Contains(out, "Task") {
		t.Fatalf("render missing header/total:\n%s", out)
	}
}

func TestScan(t *testing.T) {
	dir := t.TempDir()
	write := func(id string, evs []event.Event) {
		p := filepath.Join(dir, ".ycc", "sessions", id, "events.jsonl")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		for _, ev := range evs {
			line, _ := json.Marshal(ev)
			b.Write(line)
			b.WriteByte('\n')
		}
		if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("s_aaa", []event.Event{
		focus("0007"),
		turn("2026-06-01", "claude", event.Usage{Input: 100, Total: 100}),
	})
	write("s_bbb", []event.Event{
		focus("0008"),
		turn("2026-06-01", "gpt", event.Usage{Input: 200, Total: 200}),
	})

	entries, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	bySession := map[string]Entry{}
	for _, e := range entries {
		bySession[e.Session] = e
	}
	if bySession["s_aaa"].Task != "0007" || bySession["s_aaa"].Tokens.Total != 100 {
		t.Fatalf("s_aaa = %+v", bySession["s_aaa"])
	}
	if bySession["s_bbb"].Task != "0008" || bySession["s_bbb"].Model != "gpt" {
		t.Fatalf("s_bbb = %+v", bySession["s_bbb"])
	}

	// Missing sessions dir => no entries, no error.
	empty, err := Scan(t.TempDir())
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty scan = %v, %v", empty, err)
	}
}

func approx(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
