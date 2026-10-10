package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"runtime/debug"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"connectrpc.com/connect"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// The pointer is shared by Bubble Tea's value-model copies. Only fixed screen,
// action and enum identifiers belong here, never user or daemon content.
type uiRecorder struct {
	mu                   sync.Mutex
	client               yccv1connect.SessionServiceClient
	visitID, version     string
	buf                  []*v1.UiEvent
	view, from           string
	viewStart            time.Time
	started, catalogSent bool
}

func newUIRecorder(client yccv1connect.SessionServiceClient) *uiRecorder {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &uiRecorder{client: client, visitID: hex.EncodeToString(id), version: analyticsVersion()}
}

func analyticsToken(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == ':' || c == '/' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func analyticsVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "(devel)" && analyticsToken(info.Main.Version) {
			return info.Main.Version
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				v := s.Value
				if len(v) > 12 {
					v = v[:12]
				}
				if analyticsToken(v) {
					return v
				}
			}
		}
	}
	return "dev"
}

// Init is also used to retry startup failures; do not multiply visits or timers.
func (r *uiRecorder) visitStart(attrs map[string]string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return false
	}
	r.started = true
	r.buf = append(r.buf, &v1.UiEvent{TimeMs: time.Now().UnixMilli(), Kind: "visit", Name: "start", Attrs: attrs})
	return true
}

func (r *uiRecorder) observeView(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if name == r.view {
		return
	}
	now := time.Now()
	r.closeView(now)
	r.from, r.view, r.viewStart = r.view, name, now
}

// closeView is called with mu held.
func (r *uiRecorder) closeView(now time.Time) {
	if r.view == "" {
		return
	}
	var attrs map[string]string
	if r.from != "" {
		attrs = map[string]string{"from": r.from}
	}
	r.buf = append(r.buf, &v1.UiEvent{TimeMs: now.UnixMilli(), Kind: "view", Name: r.view, DurationMs: now.Sub(r.viewStart).Milliseconds(), Attrs: attrs})
}

func (r *uiRecorder) action(view, name string, attrs map[string]string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, &v1.UiEvent{TimeMs: time.Now().UnixMilli(), Kind: "action", Name: name, View: view, Via: "keyboard", Attrs: attrs})
}

func (r *uiRecorder) errorEvent(view, name, code string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, &v1.UiEvent{TimeMs: time.Now().UnixMilli(), Kind: "error", Name: name, View: view, Attrs: map[string]string{"code": code}})
}

func (r *uiRecorder) full() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf) >= 100
}

func (r *uiRecorder) takeBatch() *v1.RecordUiEventsRequest {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) == 0 && r.catalogSent {
		return nil
	}
	req := &v1.RecordUiEventsRequest{Client: "tui", ClientVersion: r.version, VisitId: r.visitID, Events: r.buf}
	r.buf = nil
	if !r.catalogSent {
		req.Catalog = tuiAnalyticsCatalog
	}
	return req
}

func (r *uiRecorder) send(req *v1.RecordUiEventsRequest, timeout time.Duration) {
	if req == nil {
		return
	}
	// Test clients can embed an unimplemented (nil) client interface. Analytics
	// is best effort and must not bring down the UI, even in that case.
	defer func() { _ = recover() }()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if _, err := r.client.RecordUiEvents(ctx, connect.NewRequest(req)); err == nil && len(req.Catalog) > 0 {
		r.mu.Lock()
		r.catalogSent = true
		r.mu.Unlock()
	}
}

func (r *uiRecorder) flushCmd() tea.Cmd {
	req := r.takeBatch()
	if req == nil {
		return nil
	}
	return func() tea.Msg { r.send(req, 3*time.Second); return nil }
}

func (r *uiRecorder) finish(timeout time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closeView(time.Now())
	r.view = ""
	r.mu.Unlock()
	r.send(r.takeBatch(), timeout)
}

type analyticsTickMsg struct{}

func analyticsTick() tea.Cmd {
	return tea.Tick(30*time.Second, func(time.Time) tea.Msg { return analyticsTickMsg{} })
}

func (m model) recordAction(name string) {
	m.ana.action(m.analyticsView(), name, nil)
}

// Keep the precedence in sync with render(), not input dispatch: underlying
// sessions continue receiving messages while modal screens are visible.
func (m model) analyticsView() string {
	switch {
	case m.err != nil:
		return "error"
	case m.helpOpen:
		return "help"
	case m.cdiffOpen:
		return "commit_diff"
	case m.capture:
		return "quick_capture"
	case m.backlog:
		if m.backlogDetail != nil {
			return "task"
		}
		return "backlog"
	case m.plans:
		if m.planDetail != nil {
			return "plan"
		}
		return "plans"
	case m.cost:
		return "usage"
	case m.ws:
		if m.wsMerge != nil {
			return "workstream_merge"
		}
		return "workstreams"
	case m.digest:
		return "workloop_digest"
	case m.histModal:
		if m.histModalTranscript {
			return "session_transcript"
		}
		return "sessions"
	case m.browse:
		return "browse"
	case m.mbOpen:
		if m.mbView == 1 {
			return "model_editor"
		}
		return "model_backends"
	case m.overlay:
		return "settings"
	case m.state == statePicker:
		switch m.projectMode {
		case projectPickerAdd:
			return "add_project"
		case projectPickerRename:
			return "rename_project"
		}
		return "projects"
	case m.state == stateHistory:
		if m.historyTranscript {
			return "session_transcript"
		}
		return "sessions"
	case m.state == stateMenu:
		return "home"
	default:
		return "session"
	}
}

var tuiAnalyticsCatalog = []*v1.UiCatalogEntry{
	{Kind: "view", Name: "error"},
	{Kind: "view", Name: "help"},
	{Kind: "view", Name: "commit_diff"},
	{Kind: "view", Name: "quick_capture"},
	{Kind: "view", Name: "task"},
	{Kind: "view", Name: "backlog"},
	{Kind: "view", Name: "plan"},
	{Kind: "view", Name: "plans"},
	{Kind: "view", Name: "usage"},
	{Kind: "view", Name: "workstream_merge"},
	{Kind: "view", Name: "workstreams"},
	{Kind: "view", Name: "workloop_digest"},
	{Kind: "view", Name: "session_transcript"},
	{Kind: "view", Name: "sessions"},
	{Kind: "view", Name: "browse"},
	{Kind: "view", Name: "model_editor"},
	{Kind: "view", Name: "model_backends"},
	{Kind: "view", Name: "settings"},
	{Kind: "view", Name: "projects"},
	{Kind: "view", Name: "add_project"},
	{Kind: "view", Name: "rename_project"},
	{Kind: "view", Name: "home"},
	{Kind: "view", Name: "session"},
	// Omit shortcut labels: via=keyboard would otherwise falsely imply shortcuts were not learned.
	{Kind: "action", Name: "help.open"},
	{Kind: "action", Name: "settings.open"},
	{Kind: "action", Name: "backlog.open"},
	{Kind: "action", Name: "browse.open"},
	{Kind: "action", Name: "sessions.open"},
	{Kind: "action", Name: "quick_capture.open"},
	{Kind: "action", Name: "quick_capture.submit"},
	{Kind: "action", Name: "quick_capture.cancel"},
	{Kind: "action", Name: "new_session.submit"},
	{Kind: "action", Name: "workloop.toggle"},
	{Kind: "action", Name: "session.continue_last"},
	{Kind: "action", Name: "sessions.open_waiting"},
	{Kind: "action", Name: "backlog.open_blocked"},
	{Kind: "action", Name: "projects.open"},
	{Kind: "action", Name: "session.send"},
	{Kind: "action", Name: "session.interrupt"},
	{Kind: "action", Name: "session.rollover"},
	{Kind: "action", Name: "session.search"},
	{Kind: "action", Name: "session.jump"},
	{Kind: "action", Name: "session.copy"},
	{Kind: "action", Name: "session.commit_diff"},
	{Kind: "action", Name: "session.quit"},
	{Kind: "action", Name: "question.answer"},
	{Kind: "action", Name: "backlog.toggle_done"},
	{Kind: "action", Name: "backlog.priority"},
	{Kind: "action", Name: "backlog.set_status"},
	{Kind: "action", Name: "backlog.multi_select"},
	{Kind: "action", Name: "backlog.spawn_parallel"},
	{Kind: "action", Name: "task.edit"},
	{Kind: "action", Name: "sessions.view_transcript"},
	{Kind: "action", Name: "sessions.resume"},
	{Kind: "action", Name: "sessions.refresh"},
	{Kind: "action", Name: "sessions.follow_up"},
	{Kind: "action", Name: "sessions.follow_up_filter"},
	{Kind: "action", Name: "workstreams.attach"},
	{Kind: "action", Name: "workstreams.merge"},
	{Kind: "action", Name: "workstreams.discard"},
	{Kind: "action", Name: "usage.group"},
	{Kind: "action", Name: "usage.drill"},
	{Kind: "action", Name: "plans.view"},
	{Kind: "action", Name: "settings.change"},
	{Kind: "action", Name: "model_backends.add"},
	{Kind: "action", Name: "model_backends.edit"},
	{Kind: "action", Name: "model_backends.duplicate"},
	{Kind: "action", Name: "model_backends.delete"},
	{Kind: "action", Name: "projects.switch"},
	{Kind: "action", Name: "projects.add"},
	{Kind: "action", Name: "projects.rename"},
	{Kind: "action", Name: "projects.remove"},
}
