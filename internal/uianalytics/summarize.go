package uianalytics

import (
	"sort"
	"strings"
	"time"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// kindOrder sorts report rows by kind, then by count.
var kindOrder = map[string]int{"view": 0, "action": 1, "error": 2, "nav": 3}

type rowAcc struct {
	row       *v1.UiAnalyticsRow
	durations []int64
	days      map[string]bool
}

// Summarize aggregates records (already filtered to the window) and catalogs
// into a report. Enabled/StorageDir/Days are left for the caller.
func Summarize(recs []Record, catalogs map[string]Catalog) *v1.GetUiAnalyticsResponse {
	type clientAcc struct {
		sum      *v1.UiClientSummary
		visits   map[string]bool
		days     map[string]bool
		versions map[string]bool
	}
	clients := make(map[string]*clientAcc)
	rows := make(map[string]*rowAcc)
	row := func(client, kind, name string) *rowAcc {
		key := client + "\x00" + kind + "\x00" + name
		r := rows[key]
		if r == nil {
			r = &rowAcc{row: &v1.UiAnalyticsRow{Client: client, Kind: kind, Name: name}, days: make(map[string]bool)}
			rows[key] = r
		}
		return r
	}
	bump := func(m *map[string]int64, k string) {
		if k == "" {
			return
		}
		if *m == nil {
			*m = make(map[string]int64)
		}
		(*m)[k]++
	}

	for _, rec := range recs {
		c := clients[rec.Client]
		if c == nil {
			c = &clientAcc{sum: &v1.UiClientSummary{Client: rec.Client, FirstMs: rec.Time}, visits: map[string]bool{}, days: map[string]bool{}, versions: map[string]bool{}}
			clients[rec.Client] = c
		}
		day := time.UnixMilli(rec.Time).Local().Format("2006-01-02")
		c.sum.Events++
		c.days[day] = true
		if rec.Visit != "" {
			c.visits[rec.Visit] = true
		}
		if rec.Version != "" {
			c.versions[rec.Version] = true
		}
		if rec.Time < c.sum.FirstMs {
			c.sum.FirstMs = rec.Time
		}
		if rec.Time > c.sum.LastMs {
			c.sum.LastMs = rec.Time
		}
		if rec.Kind == "visit" {
			for k, v := range rec.Attrs {
				bump(&c.sum.VisitAttrs, k+"="+v)
			}
			continue
		}

		r := row(rec.Client, rec.Kind, rec.Name)
		r.row.Count++
		r.days[day] = true
		if rec.Time > r.row.LastMs {
			r.row.LastMs = rec.Time
		}
		if rec.Duration > 0 {
			r.row.TotalDurationMs += rec.Duration
			r.durations = append(r.durations, rec.Duration)
		}
		switch rec.Kind {
		case "view":
			from := rec.Attrs["from"]
			bump(&r.row.Breakdown, from)
			if from != "" {
				n := row(rec.Client, "nav", from+">"+rec.Name)
				n.row.Count++
				n.days[day] = true
				if rec.Time > n.row.LastMs {
					n.row.LastMs = rec.Time
				}
			}
		case "action":
			via := rec.Via
			if via == "" {
				via = "unknown"
			}
			bump(&r.row.Breakdown, via)
			bump(&r.row.Contexts, rec.View)
		case "error":
			code := rec.Attrs["code"]
			if code == "" {
				code = "unknown"
			}
			bump(&r.row.Breakdown, code)
			bump(&r.row.Contexts, rec.View)
		}
	}

	resp := &v1.GetUiAnalyticsResponse{}
	for _, c := range clients {
		c.sum.Visits = int64(len(c.visits))
		c.sum.ActiveDays = int32(len(c.days))
		for v := range c.versions {
			c.sum.Versions = append(c.sum.Versions, v)
		}
		sort.Strings(c.sum.Versions)
		resp.Clients = append(resp.Clients, c.sum)
	}
	sort.Slice(resp.Clients, func(i, j int) bool {
		if resp.Clients[i].Events != resp.Clients[j].Events {
			return resp.Clients[i].Events > resp.Clients[j].Events
		}
		return resp.Clients[i].Client < resp.Clients[j].Client
	})

	flows := make(map[string]*v1.UiFlow)
	for _, r := range rows {
		r.row.ActiveDays = int32(len(r.days))
		if len(r.durations) > 0 {
			sort.Slice(r.durations, func(i, j int) bool { return r.durations[i] < r.durations[j] })
			r.row.MedianDurationMs = r.durations[len(r.durations)/2]
		}
		resp.Rows = append(resp.Rows, r.row)
		if r.row.Kind != "action" {
			continue
		}
		dot := strings.LastIndexByte(r.row.Name, '.')
		if dot <= 0 {
			continue
		}
		base, verb := r.row.Name[:dot], r.row.Name[dot+1:]
		if verb != "open" && verb != "submit" && verb != "cancel" {
			continue
		}
		key := r.row.Client + "\x00" + base
		f := flows[key]
		if f == nil {
			f = &v1.UiFlow{Client: r.row.Client, Name: base}
			flows[key] = f
		}
		switch verb {
		case "open":
			f.Opened += r.row.Count
		case "submit":
			f.Submitted += r.row.Count
		case "cancel":
			f.Cancelled += r.row.Count
		}
	}
	sort.Slice(resp.Rows, func(i, j int) bool {
		a, b := resp.Rows[i], resp.Rows[j]
		if a.Client != b.Client {
			return a.Client < b.Client
		}
		if kindOrder[a.Kind] != kindOrder[b.Kind] {
			return kindOrder[a.Kind] < kindOrder[b.Kind]
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Name < b.Name
	})
	for _, f := range flows {
		// Real flows always close with submit or cancel; a bare `<x>.open`
		// action (e.g. the web palette's `palette.open`) is navigation.
		if f.Opened > 0 && f.Submitted+f.Cancelled > 0 {
			resp.Flows = append(resp.Flows, f)
		}
	}
	sort.Slice(resp.Flows, func(i, j int) bool {
		if resp.Flows[i].Client != resp.Flows[j].Client {
			return resp.Flows[i].Client < resp.Flows[j].Client
		}
		return resp.Flows[i].Opened > resp.Flows[j].Opened
	})

	// Catalog gaps, only for clients active in the window (a client not used
	// at all would otherwise list its whole catalog).
	var clientNames []string
	for name := range catalogs {
		if clients[name] != nil {
			clientNames = append(clientNames, name)
		}
	}
	sort.Strings(clientNames)
	for _, client := range clientNames {
		for _, e := range catalogs[client].Entries {
			r := rows[client+"\x00"+e.Kind+"\x00"+e.Name]
			switch {
			case r == nil:
				resp.Gaps = append(resp.Gaps, &v1.UiCatalogGap{Client: client, Kind: e.Kind, Name: e.Name, Shortcut: e.Shortcut})
			case e.Kind == "action" && e.Shortcut != "" && r.row.Count >= 3 && r.row.Breakdown["shortcut"]*2 < r.row.Count:
				resp.Gaps = append(resp.Gaps, &v1.UiCatalogGap{Client: client, Kind: e.Kind, Name: e.Name, Shortcut: e.Shortcut, ShortcutNotLearned: true, Count: r.row.Count})
			}
		}
	}
	return resp
}
