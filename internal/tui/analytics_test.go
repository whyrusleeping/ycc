package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/uianalytics"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

type analyticsClient struct {
	yccv1connect.SessionServiceClient
	requests []*v1.RecordUiEventsRequest
	err      error
}

func (c *analyticsClient) RecordUiEvents(ctx context.Context, req *connect.Request[v1.RecordUiEventsRequest]) (*connect.Response[v1.RecordUiEventsResponse], error) {
	c.requests = append(c.requests, req.Msg)
	if _, ok := ctx.Deadline(); !ok {
		panic("analytics RPC has no deadline")
	}
	return connect.NewResponse(&v1.RecordUiEventsResponse{}), c.err
}

func TestAnalyticsViewsAndExit(t *testing.T) {
	client := &analyticsClient{}
	r := newUIRecorder(client)
	if !r.visitStart(map[string]string{"input": "keyboard"}) || r.visitStart(nil) {
		t.Fatal("one run should start exactly one visit, even after startup retry")
	}
	r.observeView("home")
	r.viewStart = time.Now().Add(-2 * time.Second)
	r.observeView("backlog")
	r.observeView("backlog") // messages on the same screen do not split its dwell
	r.viewStart = time.Now().Add(-time.Second)
	r.action("backlog", "backlog.set_status", map[string]string{"status": "in_review"})
	r.finish(time.Second)
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d", len(client.requests))
	}
	req := client.requests[0]
	if len(req.Events) != 4 {
		t.Fatalf("events = %v", req.Events)
	}
	home, action, backlog := req.Events[1], req.Events[2], req.Events[3]
	if home.Kind != "view" || home.Name != "home" || home.DurationMs < 2000 || len(home.Attrs) != 0 {
		t.Fatalf("home dwell = %v", home)
	}
	if backlog.Kind != "view" || backlog.Name != "backlog" || backlog.Attrs["from"] != "home" || backlog.DurationMs < 1000 {
		t.Fatalf("backlog dwell = %v", backlog)
	}
	if action.Kind != "action" || action.Via != "keyboard" || action.View != "backlog" {
		t.Fatalf("action = %v", action)
	}
	records, dropped, err := uianalytics.Validate(req, time.Now())
	if err != nil || dropped != 0 || len(records) != len(req.Events) {
		t.Fatalf("validate: %v, dropped=%d", err, dropped)
	}
	if r.flushCmd() != nil {
		t.Fatal("exit should drain the buffer")
	}
}

func TestAnalyticsFlushDropsFailuresAndSendsCatalogUntilSuccess(t *testing.T) {
	client := &analyticsClient{err: errors.New("offline")}
	r := newUIRecorder(client)
	r.action("home", "browse.open", nil)
	cmd := r.flushCmd()
	if len(client.requests) != 0 {
		t.Fatal("flush must not do network IO on the UI loop")
	}
	if cmd() != nil {
		t.Fatal("analytics must not emit an error message")
	}
	if len(r.buf) != 0 {
		t.Fatal("failed events should be dropped")
	}
	client.err = nil
	// An empty batch can still deliver the catalog after a failed first send.
	r.flushCmd()()
	r.action("session", "session.interrupt", nil)
	r.flushCmd()()
	if r.flushCmd() != nil {
		t.Fatal("empty successful recorder should have no command")
	}
	if len(client.requests) != 3 {
		t.Fatalf("requests = %d", len(client.requests))
	}
	for i, req := range client.requests {
		if req.Client != "tui" || !analyticsToken(req.VisitId) || !analyticsToken(req.ClientVersion) {
			t.Fatalf("request metadata = %v", req)
		}
		if (len(req.Catalog) > 0) != (i < 2) {
			t.Fatalf("catalog in request %d = %v", i, req.Catalog)
		}
		if req.VisitId != r.visitID {
			t.Fatal("visit changed between flushes")
		}
	}
	if len(client.requests[1].Events) != 0 {
		t.Fatal("failed events must not be retried")
	}
}

func TestAnalyticsNilAndUnimplementedClients(t *testing.T) {
	var r *uiRecorder
	r.visitStart(nil)
	r.observeView("home")
	r.action("home", "browse.open", nil)
	r.errorEvent("home", "flash", "unknown")
	r.finish(time.Second)
	if r.full() || r.flushCmd() != nil {
		t.Fatal("nil recorder should do nothing")
	}
	// Embedding a nil interface is common in the existing TUI fake clients.
	r = newUIRecorder(struct {
		yccv1connect.SessionServiceClient
	}{})
	r.visitStart(nil)
	if r.flushCmd()() != nil {
		t.Fatal("unimplemented analytics must be ignored")
	}
}

func TestAnalyticsCatalogValid(t *testing.T) {
	req := &v1.RecordUiEventsRequest{Client: "tui"}
	seen := map[string]bool{}
	for _, e := range tuiAnalyticsCatalog {
		key := e.Kind + ":" + e.Name
		if seen[key] {
			t.Fatalf("duplicate catalog entry %s", key)
		}
		seen[key] = true
		if e.Shortcut != "" {
			t.Fatalf("TUI catalog must not advertise shortcut learning: %s", key)
		}
		req.Events = append(req.Events, &v1.UiEvent{Kind: e.Kind, Name: e.Name})
	}
	_, dropped, err := uianalytics.Validate(req, time.Now())
	if err != nil || dropped != 0 {
		t.Fatalf("catalog identifiers: %v, dropped=%d", err, dropped)
	}
}

func TestAnalyticsUpdateAndPrivacy(t *testing.T) {
	client := &analyticsClient{}
	m := initialModel(context.Background(), client, "private/workspace", false)
	m.ana.observeView("home")
	m.entries = []menuEntry{{mode: "chat", preset: "private-preset", openingPrompt: "private title"}}
	m.prompt.SetValue("private user prompt")
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	next, _ = m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m = next.(model)
	m.connected = true
	m.flash(connect.NewError(connect.CodeUnavailable, errors.New("private path and content")))
	m.ana.flushCmd()()
	events := client.requests[0].Events
	if len(events) != 4 {
		t.Fatalf("events = %v", events)
	}
	if events[0].Name != "new_session.submit" || events[0].Attrs["mode"] != "chat" || events[0].Attrs["preset"] != "true" || len(events[0].Attrs) != 2 {
		t.Fatalf("new-session attributes = %v", events[0])
	}
	if events[1].Name != "backlog.open" || events[1].View != "home" || events[2].Name != "home" {
		t.Fatalf("navigation events = %v", events)
	}
	if events[3].Kind != "error" || events[3].View != "backlog" || events[3].Attrs["code"] != "unavailable" || len(events[3].Attrs) != 1 {
		t.Fatalf("error event = %v", events[3])
	}
	_, dropped, err := uianalytics.Validate(client.requests[0], time.Now())
	if err != nil || dropped != 0 {
		t.Fatalf("validate: %v, dropped=%d", err, dropped)
	}

	// Reaching the event threshold schedules a flush without waiting for the tick.
	for i := 0; i < 100; i++ {
		m.recordAction("backlog.toggle_done")
	}
	_, cmd := m.Update(struct{}{})
	if cmd == nil || len(m.ana.buf) != 0 {
		t.Fatal("threshold must schedule and drain a batch")
	}
	m.recordAction("backlog.toggle_done")
	_, cmd = m.Update(analyticsTickMsg{})
	if cmd == nil || len(m.ana.buf) != 0 {
		t.Fatal("periodic tick must flush below-threshold events")
	}
	if m.ana.view != "backlog" {
		t.Fatal("flush should not end the current screen's dwell")
	}
}

func TestAnalyticsSettingsInterruptOrigin(t *testing.T) {
	client := &analyticsClient{}
	m := model{
		ana: newUIRecorder(client), state: stateSession, sessionID: "private-session",
		status: "running", overlay: true, ovCursor: ovInterrupt,
	}
	m.ana.observeView("settings")
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next.(model).overlay || cmd == nil {
		t.Fatal("interrupt should close settings and schedule the RPC")
	}
	m.ana.flushCmd()()
	actions := 0
	for _, e := range client.requests[0].Events {
		if e.Kind != "action" {
			continue
		}
		actions++
		if e.Name != "session.interrupt" || e.View != "settings" || e.Via != "keyboard" {
			t.Fatalf("interrupt event = %v", e)
		}
	}
	if actions != 1 {
		t.Fatalf("interrupt actions = %d, want 1", actions)
	}
}

func TestAnalyticsViewPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name string
		m    model
		want string
	}{
		{"home", model{state: stateMenu}, "home"},
		{"session", model{state: stateSession}, "session"},
		{"modal", model{state: stateSession, backlog: true}, "backlog"},
		{"task", model{backlog: true, backlogDetail: &v1.TaskDetail{}}, "task"},
		{"help over diff", model{helpOpen: true, cdiffOpen: true}, "help"},
		{"fatal over help", model{err: errors.New("offline"), helpOpen: true}, "error"},
		{"transcript", model{state: stateHistory, historyTranscript: true}, "session_transcript"},
		{"settings over picker", model{state: statePicker, overlay: true}, "settings"},
		{"add project", model{state: statePicker, projectMode: projectPickerAdd}, "add_project"},
		{"model form", model{mbOpen: true, mbView: 1}, "model_editor"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.analyticsView(); got != tt.want {
				t.Fatalf("view = %q, want %q", got, tt.want)
			}
		})
	}
}
