package uianalytics

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func testStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "analytics"))
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	return s
}

func TestValidateDropsContentAndClampsTime(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	req := &v1.RecordUiEventsRequest{
		Client:        "web",
		ClientVersion: "has spaces", // invalid optional field: cleared, not fatal
		VisitId:       "v1",
		Events: []*v1.UiEvent{
			{TimeMs: now.Add(-time.Minute).UnixMilli(), Kind: "view", Name: "backlog", DurationMs: 1500, Attrs: map[string]string{"from": "home"}},
			{TimeMs: 1, Kind: "action", Name: "session.interrupt", Via: "shortcut"}, // implausible clock
			{Kind: "action", Name: "fix the login bug"},                             // prose name
			{Kind: "bogus", Name: "x"}, // unknown kind
			{Kind: "error", Name: "backlog.update", Attrs: map[string]string{"code": "task not found"}}, // prose attr
			{Kind: "view", Name: "session", DurationMs: -5},
		},
	}
	recs, dropped, err := Validate(req, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || dropped != 3 {
		t.Fatalf("got %d records, %d dropped; want 3, 3", len(recs), dropped)
	}
	if recs[0].Version != "" || recs[0].Visit != "v1" || recs[0].Client != "web" {
		t.Fatalf("request fields: %+v", recs[0])
	}
	if recs[1].Time != now.UnixMilli() {
		t.Fatalf("implausible client time not replaced: %d", recs[1].Time)
	}
	if recs[2].Duration != 0 {
		t.Fatalf("negative duration not clamped: %d", recs[2].Duration)
	}
	if _, _, err := Validate(&v1.RecordUiEventsRequest{Client: "my laptop"}, now); err != ErrInvalidClient {
		t.Fatalf("invalid client: %v", err)
	}
}

func TestRecordIsOwnerOnlyAndLoadsBack(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	s := testStore(t, now)
	acc, dropped, err := s.Record(&v1.RecordUiEventsRequest{Client: "ios", VisitId: "a", Events: []*v1.UiEvent{
		{TimeMs: now.UnixMilli(), Kind: "view", Name: "home"},
		{TimeMs: now.UnixMilli(), Kind: "action", Name: "composer.send", Via: "click", View: "session"},
	}})
	if err != nil || acc != 2 || dropped != 0 {
		t.Fatalf("Record = %d, %d, %v", acc, dropped, err)
	}
	path := filepath.Join(s.Dir(), "events-2026-10.jsonl")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("event file mode %v", st.Mode().Perm())
	}
	if dst, _ := os.Stat(s.Dir()); dst.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", dst.Mode().Perm())
	}
	recs, err := s.Load(now.Add(-time.Hour), "")
	if err != nil || len(recs) != 2 {
		t.Fatalf("Load = %d, %v", len(recs), err)
	}
	if recs, _ := s.Load(now.Add(-time.Hour), "web"); len(recs) != 0 {
		t.Fatalf("client filter leaked %d records", len(recs))
	}
	if recs, _ := s.Load(now.Add(time.Hour), ""); len(recs) != 0 {
		t.Fatalf("since filter leaked %d records", len(recs))
	}
}

func TestLoadSkipsTornLines(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	s := testStore(t, now)
	line := `{"t":` + strconv.FormatInt(now.UnixMilli(), 10) + `,"client":"web","kind":"view","name":"home"}` + "\n"
	if err := os.WriteFile(filepath.Join(s.Dir(), "events-2026-10.jsonl"), []byte(line+`{"t":12,"cli`), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := s.Load(now.Add(-time.Hour), "")
	if err != nil || len(recs) != 1 {
		t.Fatalf("Load = %d, %v", len(recs), err)
	}
}

func TestRetentionPrunesOldMonths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "analytics")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"2025-10", "2025-11", "2026-10"} {
		if err := os.WriteFile(filepath.Join(dir, "events-"+m+".jsonl"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{dir: dir, now: func() time.Time { return time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC) }}
	s.pruneLocked()
	for m, want := range map[string]bool{"2025-10": false, "2025-11": true, "2026-10": true} {
		_, err := os.Stat(filepath.Join(dir, "events-"+m+".jsonl"))
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", m, err == nil, want)
		}
	}
}

func TestReportSummarisesUsage(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	s := testStore(t, now)
	ms := now.UnixMilli()
	ev := func(kind, name, view, via string, dur int64, attrs map[string]string) *v1.UiEvent {
		return &v1.UiEvent{TimeMs: ms, Kind: kind, Name: name, View: view, Via: via, DurationMs: dur, Attrs: attrs}
	}
	_, _, err := s.Record(&v1.RecordUiEventsRequest{
		Client: "web", ClientVersion: "abc123", VisitId: "v1",
		Events: []*v1.UiEvent{
			ev("visit", "start", "", "", 0, map[string]string{"layout": "wide"}),
			ev("view", "home", "", "", 1000, nil),
			ev("view", "session", "", "", 3000, map[string]string{"from": "home"}),
			ev("view", "session", "", "", 5000, map[string]string{"from": "backlog"}),
			ev("view", "session", "", "", 4000, map[string]string{"from": "home"}),
			ev("action", "palette.open_backlog", "session", "palette", 0, nil),
			ev("action", "palette.open_backlog", "session", "click", 0, nil),
			ev("action", "palette.open_backlog", "home", "palette", 0, nil),
			ev("action", "new_session.open", "home", "click", 0, nil),
			ev("action", "new_session.open", "home", "shortcut", 0, nil),
			ev("action", "new_session.submit", "new_session", "click", 0, nil),
			ev("error", "backlog.update", "task", "", 0, map[string]string{"code": "unavailable"}),
		},
		Catalog: []*v1.UiCatalogEntry{
			{Kind: "action", Name: "palette.open_backlog", Shortcut: "Alt+B"},
			{Kind: "action", Name: "session.interrupt", Shortcut: "Esc"},
			{Kind: "view", Name: "workstreams"},
			{Kind: "view", Name: "home"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A second client without a catalog, and an idle client's catalog that
	// must not produce gaps.
	if _, _, err := s.Record(&v1.RecordUiEventsRequest{Client: "ios", VisitId: "i1", Events: []*v1.UiEvent{ev("view", "home", "", "", 0, nil)}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Record(&v1.RecordUiEventsRequest{Client: "tui", Catalog: []*v1.UiCatalogEntry{{Kind: "view", Name: "home"}}}); err != nil {
		t.Fatal(err)
	}

	resp, err := s.Report(30, "")
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Enabled || resp.Days != 30 || resp.StorageDir != s.Dir() {
		t.Fatalf("header: %+v", resp)
	}
	if len(resp.Clients) != 2 || resp.Clients[0].Client != "web" || resp.Clients[0].Visits != 1 || resp.Clients[0].VisitAttrs["layout=wide"] != 1 {
		t.Fatalf("clients: %+v", resp.Clients)
	}
	find := func(client, kind, name string) *v1.UiAnalyticsRow {
		for _, r := range resp.Rows {
			if r.Client == client && r.Kind == kind && r.Name == name {
				return r
			}
		}
		t.Fatalf("missing row %s %s %s", client, kind, name)
		return nil
	}
	sess := find("web", "view", "session")
	if sess.Count != 3 || sess.TotalDurationMs != 12000 || sess.MedianDurationMs != 4000 || sess.Breakdown["home"] != 2 {
		t.Fatalf("session view: %+v", sess)
	}
	if nav := find("web", "nav", "home>session"); nav.Count != 2 {
		t.Fatalf("nav: %+v", nav)
	}
	pal := find("web", "action", "palette.open_backlog")
	if pal.Count != 3 || pal.Breakdown["palette"] != 2 || pal.Contexts["session"] != 2 {
		t.Fatalf("action: %+v", pal)
	}
	if e := find("web", "error", "backlog.update"); e.Breakdown["unavailable"] != 1 || e.Contexts["task"] != 1 {
		t.Fatalf("error: %+v", e)
	}
	if len(resp.Flows) != 1 || resp.Flows[0].Name != "new_session" || resp.Flows[0].Opened != 2 || resp.Flows[0].Submitted != 1 {
		t.Fatalf("flows: %+v", resp.Flows)
	}
	gaps := map[string]*v1.UiCatalogGap{}
	for _, g := range resp.Gaps {
		if g.Client != "web" {
			t.Fatalf("gap for inactive client: %+v", g)
		}
		gaps[g.Kind+" "+g.Name] = g
	}
	if len(gaps) != 3 || gaps["action session.interrupt"] == nil || gaps["view workstreams"] == nil ||
		!gaps["action palette.open_backlog"].GetShortcutNotLearned() {
		t.Fatalf("gaps: %+v", resp.Gaps)
	}

	only, err := s.Report(30, "ios")
	if err != nil || len(only.Clients) != 1 || only.Clients[0].Client != "ios" || len(only.Gaps) != 0 {
		t.Fatalf("client filter: %+v, %v", only, err)
	}
}

func TestCatalogRejectsBadEntries(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	s := testStore(t, now)
	if _, _, err := s.Record(&v1.RecordUiEventsRequest{Client: "web", Catalog: []*v1.UiCatalogEntry{
		{Kind: "action", Name: "session.stop", Shortcut: "⌘."},
		{Kind: "action", Name: "session.stop"},
		{Kind: "action", Name: "Open my secret file"},
		{Kind: "visit", Name: "start"},
		{Kind: "view", Name: "home", Shortcut: "bad\nlabel"},
	}}); err != nil {
		t.Fatal(err)
	}
	cats, err := s.Catalogs()
	if err != nil {
		t.Fatal(err)
	}
	got := cats["web"].Entries
	if len(got) != 2 || got[0] != (CatalogEntry{Kind: "action", Name: "session.stop", Shortcut: "⌘."}) || got[1] != (CatalogEntry{Kind: "view", Name: "home"}) {
		t.Fatalf("catalog: %+v", got)
	}
}
