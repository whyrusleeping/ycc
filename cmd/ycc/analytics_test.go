package main

import (
	"bytes"
	"strings"
	"testing"

	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

func TestRenderAnalytics(t *testing.T) {
	var buf bytes.Buffer
	renderAnalytics(&buf, &v1.GetUiAnalyticsResponse{
		Enabled: true, Days: 30, StorageDir: "/state/ycc/analytics",
		Clients: []*v1.UiClientSummary{{Client: "web", Visits: 4, ActiveDays: 2, Events: 20}},
		Rows: []*v1.UiAnalyticsRow{
			{Client: "web", Kind: "view", Name: "session", Count: 5, TotalDurationMs: 90_000, Breakdown: map[string]int64{"home": 4, "backlog": 1}},
			{Client: "web", Kind: "action", Name: "session.interrupt", Count: 2, Breakdown: map[string]int64{"click": 2}, Contexts: map[string]int64{"session": 2}},
			{Client: "web", Kind: "nav", Name: "home>session", Count: 4},
			{Client: "web", Kind: "error", Name: "backlog.update", Count: 1, Breakdown: map[string]int64{"unavailable": 1}},
		},
		Flows: []*v1.UiFlow{{Client: "web", Name: "new_session", Opened: 5, Submitted: 3, Cancelled: 1}},
		Gaps: []*v1.UiCatalogGap{
			{Client: "web", Kind: "view", Name: "workstreams"},
			{Client: "web", Kind: "action", Name: "session.interrupt", Shortcut: "Esc", ShortcutNotLearned: true, Count: 2},
		},
	}, 25)
	out := buf.String()
	for _, want := range []string{
		"raw events: /state/ycc/analytics",
		"== web: 4 visits over 2 active days",
		"home:4 backlog:1",
		"session.interrupt",
		"home -> session",
		"codes: unavailable:1",
		"new_session                         5 -> 3 / 1 / 1",
		"Never used (1)",
		"view workstreams",
		"action session.interrupt (Esc), used 2 times",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	buf.Reset()
	renderAnalytics(&buf, &v1.GetUiAnalyticsResponse{}, 25)
	if !strings.Contains(buf.String(), "not enabled") {
		t.Errorf("disabled report: %s", buf.String())
	}
}
