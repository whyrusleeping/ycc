package usage

import (
	"encoding/json"
	"math/bits"
	"sort"
	"strings"
	"time"
)

// busySlot is the resolution of busy time. A session counts as busy for every
// slot in which one of its model turns or tool calls was running, so concurrent
// agents in one session count once while concurrent sessions add up.
const busySlot = 10 * time.Second

const slotsPerHour = int(time.Hour / busySlot)

// slotMask is one hour's busy slots, one bit per slot.
type slotMask [(slotsPerHour + 63) / 64]uint64

func (m *slotMask) set(i int) { m[i/64] |= 1 << (i % 64) }

func (m *slotMask) or(o slotMask) {
	for i := range m {
		m[i] |= o[i]
	}
}

func (m slotMask) count() int {
	n := 0
	for _, w := range m {
		n += bits.OnesCount64(w)
	}
	return n
}

type hourKey struct {
	task string
	hour int64 // Unix seconds of the UTC hour's start
}

type hourBucket struct {
	tokens int
	turns  int
	busy   slotMask
}

// HourEntry is one session's activity in one UTC hour while focused on
// Task ("" = unattributed).
type HourEntry struct {
	Session string
	Task    string
	Hour    time.Time // UTC start of the hour
	Tokens  int       // Tokens.Total of the model turns that finished in the hour
	Turns   int       // model turns that finished in the hour
	busy    slotMask
}

// HourTotal is the aggregated activity of one UTC hour.
type HourTotal struct {
	Hour        time.Time
	Tokens      int
	Turns       int
	BusySeconds int // summed per-session busy wall time
}

func (r *reducer) hour(ts time.Time) *hourBucket {
	k := hourKey{task: r.focus, hour: ts.UTC().Truncate(time.Hour).Unix()}
	a, ok := r.hours[k]
	if !ok {
		a = &hourBucket{}
		r.hours[k] = a
	}
	return a
}

// markBusy marks the interval [end-duration, end] busy. Events are recorded
// when the turn or tool call finishes, so the interval ends at their timestamp
// and may reach back across hour boundaries.
func (r *reducer) markBusy(end time.Time, durationMS any) {
	d := time.Duration(msField(durationMS)) * time.Millisecond
	if d < 0 {
		d = 0
	}
	first := end.Add(-d).UnixNano() / int64(busySlot)
	last := end.UnixNano() / int64(busySlot)
	if d > 0 && end.UnixNano()%int64(busySlot) == 0 {
		last-- // an interval ending exactly on a slot boundary does not touch the next slot
	}
	for s := first; s <= last; s++ {
		at := time.Unix(0, s*int64(busySlot))
		r.hour(at).busy.set(int(at.UTC().Sub(at.UTC().Truncate(time.Hour)) / busySlot))
	}
}

func msField(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

func (r *reducer) hourEntries() []HourEntry {
	out := make([]HourEntry, 0, len(r.hours))
	for k, a := range r.hours {
		out = append(out, HourEntry{
			Session: r.session, Task: k.task, Hour: time.Unix(k.hour, 0).UTC(),
			Tokens: a.tokens, Turns: a.turns, busy: a.busy,
		})
	}
	return out
}

// ScanHours is Scan for hourly activity, sharing Scan's per-log cache.
func (s *Scanner) ScanHours(workspace string) ([]HourEntry, error) {
	var out []HourEntry
	err := s.scan(workspace, func(r *reducer) { out = append(out, r.hourEntries()...) })
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AggregateHours sums entries into hours, ascending, filtered by the task
// and UTC date range in opts (GroupBy is ignored). Busy time is the union of a
// session's slots in the hour, so a session busy under two task focuses in one
// hour still counts its wall time once. Hours with no activity are omitted.
func AggregateHours(entries []HourEntry, opts Options) []HourTotal {
	since, until := "", ""
	if !opts.Since.IsZero() {
		since = opts.Since.UTC().Format("2006-01-02")
	}
	if !opts.Until.IsZero() {
		until = opts.Until.UTC().Format("2006-01-02")
	}
	task := strings.TrimSpace(opts.Task)
	type sessionHour struct {
		session string
		hour    int64
	}
	hours := map[int64]*HourTotal{}
	busy := map[sessionHour]*slotMask{}
	for _, e := range entries {
		if task != "" && e.Task != task {
			continue
		}
		day := e.Hour.UTC().Format("2006-01-02")
		if (since != "" && day < since) || (until != "" && day > until) {
			continue
		}
		h := e.Hour.Unix()
		a, ok := hours[h]
		if !ok {
			a = &HourTotal{Hour: e.Hour.UTC()}
			hours[h] = a
		}
		a.Tokens += e.Tokens
		a.Turns += e.Turns
		k := sessionHour{e.Session, h}
		m, ok := busy[k]
		if !ok {
			m = &slotMask{}
			busy[k] = m
		}
		m.or(e.busy)
	}
	for k, m := range busy {
		hours[k.hour].BusySeconds += m.count() * int(busySlot/time.Second)
	}
	out := make([]HourTotal, 0, len(hours))
	for _, a := range hours {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hour.Before(out[j].Hour) })
	return out
}
