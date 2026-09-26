package tui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"connectrpc.com/connect"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

type flapServer struct {
	yccv1connect.UnimplementedSessionServiceHandler
	mu      sync.Mutex
	from    []int64
	flaps   int
	strict  bool
	flood   bool
	live    chan *v1.Event
	code    connect.Code
	resumes int
}

func (s *flapServer) Subscribe(ctx context.Context, req *connect.Request[v1.SubscribeRequest], stream *connect.ServerStream[v1.Event]) error {
	s.mu.Lock()
	s.from = append(s.from, req.Msg.FromSeq)
	attempt := len(s.from)
	flaps, code, flood, strict := s.flaps, s.code, s.flood, s.strict
	s.mu.Unlock()
	if code != 0 {
		return connect.NewError(code, errors.New("subscription failed"))
	}
	// Non-strict mode simulates a proxy replaying a duplicate despite from_seq.
	// Strict mode implements the daemon's seq > from_seq contract.
	if !strict || req.Msg.FromSeq < 1 {
		if err := stream.Send(&v1.Event{Seq: 1, Type: "user_input", Actor: "user", DataJson: `{"text":"once"}`}); err != nil {
			return err
		}
	}
	if attempt <= flaps {
		return connect.NewError(connect.CodeUnavailable, errors.New("network dropped"))
	}
	if flood {
		for i := int64(2); i < 600; i++ {
			if err := stream.Send(&v1.Event{Seq: i, Type: "user_input"}); err != nil {
				return err
			}
		}
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case ev := <-s.live:
		return stream.Send(ev)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *flapServer) ResumeSession(_ context.Context, req *connect.Request[v1.ResumeSessionRequest]) (*connect.Response[v1.ResumeSessionResponse], error) {
	s.mu.Lock()
	s.resumes++
	s.code = 0
	s.mu.Unlock()
	return connect.NewResponse(&v1.ResumeSessionResponse{SessionId: req.Msg.SessionId, Mode: "work"}), nil
}

func newFlapModel(t *testing.T, s *flapServer) model {
	t.Helper()
	path, handler := yccv1connect.NewSessionServiceHandler(s)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, path) {
			handler.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := readyStreamModel(t)
	m.client = yccv1connect.NewSessionServiceClient(httpServer.Client(), httpServer.URL)
	m.ctx = ctx
	m.sessionCtx, m.sessionCancel = context.WithCancel(ctx)
	t.Cleanup(m.sessionCancel)
	return m
}

// Take one event from the stream, then fold it through Update. No follow-up
// Bubble Tea commands are run: this test explicitly drives each wait/retry step.
func stepStream(t *testing.T, m model) (model, tea.Msg) {
	t.Helper()
	ch := make(chan tea.Msg, 1)
	go func() { ch <- waitEvent(m.sub, m.sessionID, m.subGen)() }()
	select {
	case msg := <-ch:
		updated, _ := m.Update(msg)
		return updated.(model), msg
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not produce an event/end")
		return m, nil
	}
}

func TestSubscriptionReconnectReplayAndLive(t *testing.T) {
	s := &flapServer{flaps: 3, live: make(chan *v1.Event, 1)}
	m := newFlapModel(t, s)
	m.input.SetValue("unfinished draft")
	m.follow = false
	m.pending, m.picking = "Which option?", true
	offset := 0
	m.liveTails = map[string]string{"coordinator": "obsolete tail"}
	m.retryNotes = map[string]string{"coordinator": "obsolete retry"}
	for i := 0; i <= s.flaps; i++ {
		var cmd tea.Cmd
		if i == 0 {
			cmd = m.startSubscription()
		} else {
			var updated tea.Model
			updated, cmd = m.Update(reconnectTickMsg{sessionID: m.sessionID, gen: m.subGen})
			m = updated.(model)
		}
		first := cmd()
		updated, _ := m.Update(first)
		m = updated.(model)
		if _, ok := first.(sessionEvMsg); !ok {
			t.Fatalf("attempt %d: expected first event, got %T", i, first)
		}
		if i < s.flaps {
			var end tea.Msg
			m, end = stepStream(t, m)
			if _, ok := end.(streamEndMsg); !ok {
				t.Fatalf("attempt %d: expected dropped stream, got %T", i, end)
			}
			if m.conn != connRetrying || m.reconnectAttempt != i+1 || len(m.liveTails) != 0 || len(m.retryNotes) != 0 {
				t.Fatalf("attempt %d: reconnect state=%v cursor=%d tails=%v retry=%v", i, m.conn, m.lastSeq, m.liveTails, m.retryNotes)
			}
			if i == 0 {
				// Build a long transcript so the viewport has a real scroll offset.
				for seq := int64(2); seq <= 40; seq++ {
					m.applyLiveEvent(&v1.Event{Seq: seq, Type: "user_input", Actor: "user", DataJson: `{"text":"earlier line"}`})
				}
				m.rebuild()
				m.vp.SetYOffset(5)
				offset = m.vp.YOffset()
				if offset == 0 {
					t.Fatal("long transcript did not scroll")
				}
			}
		}
	}
	// Live continuation follows replay; the buffered event has a fresh seq.
	s.live <- &v1.Event{Seq: 41, Type: "user_input", Actor: "user", DataJson: `{"text":"next"}`}
	var msg tea.Msg
	m, msg = stepStream(t, m)
	if _, ok := msg.(sessionEvMsg); !ok {
		t.Fatalf("live continuation: got %T", msg)
	}
	if m.lastSeq != 41 || len(m.evs) != 41 || m.conn != connLive || m.input.Value() != "unfinished draft" || m.follow || m.pending != "Which option?" || !m.picking {
		t.Fatalf("lost state on reconnect: cursor=%d rows=%d conn=%v draft=%q follow=%v question=%q", m.lastSeq, len(m.evs), m.conn, m.input.Value(), m.follow, m.pending)
	}
	if m.vp.YOffset() != offset {
		t.Fatalf("scroll offset changed: %d -> %d", offset, m.vp.YOffset())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, from := range s.from {
		if i == 0 && from != 0 || i > 0 && from != 39 {
			t.Fatalf("from_seq on attempt %d = %d; all=%v", i, from, s.from)
		}
	}
}

func TestSubscriptionIdleReconnectBecomesLive(t *testing.T) {
	// After the first drop the daemon has no new events: only replay of the
	// last durable row can confirm the stream is alive.
	s := &flapServer{flaps: 1, strict: true}
	m := newFlapModel(t, s)
	first := m.startSubscription()()
	updated, _ := m.Update(first)
	m = updated.(model)
	m, _ = stepStream(t, m)
	if m.conn != connRetrying || m.lastSeq != 1 || len(m.evs) != 1 {
		t.Fatalf("after drop: conn=%v seq=%d rows=%d", m.conn, m.lastSeq, len(m.evs))
	}
	updated, retry := m.Update(reconnectTickMsg{sessionID: m.sessionID, gen: m.subGen})
	m = updated.(model)
	updated, _ = m.Update(retry())
	m = updated.(model)
	if m.conn != connLive || m.lastSeq != 1 || len(m.evs) != 1 {
		t.Fatalf("idle reattach: conn=%v seq=%d rows=%d", m.conn, m.lastSeq, len(m.evs))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.from) != 2 || s.from[0] != 0 || s.from[1] != 0 {
		t.Fatalf("idle reconnect from_seq = %v; want [0 0]", s.from)
	}
}

func TestSubscriptionRepeatedFlapsStopAtLimit(t *testing.T) {
	s := &flapServer{flaps: maxReconnectAttempts + 2}
	m := newFlapModel(t, s)
	for i := 0; i < maxReconnectAttempts; i++ {
		var cmd tea.Cmd
		if i == 0 {
			cmd = m.startSubscription()
		} else {
			updated, retry := m.Update(reconnectTickMsg{sessionID: m.sessionID, gen: m.subGen})
			m, cmd = updated.(model), retry
		}
		first := cmd()
		updated, _ := m.Update(first)
		m = updated.(model)
		m, _ = stepStream(t, m)
	}
	if m.conn != connLost || m.reconnectAttempt != maxReconnectAttempts {
		t.Fatalf("repeated drops should stop automatic retry: conn=%v attempts=%d", m.conn, m.reconnectAttempt)
	}
	updated, _ := m.Update(reconnectTickMsg{sessionID: m.sessionID, gen: m.subGen})
	if updated.(model).subGen != m.subGen {
		t.Fatal("lost session accepted a retry tick")
	}
}

func TestSubscriptionHealthyIntervalResetsFlapCount(t *testing.T) {
	oldThreshold := healthySubscriptionDuration
	healthySubscriptionDuration = time.Nanosecond
	t.Cleanup(func() { healthySubscriptionDuration = oldThreshold })
	s := &flapServer{flaps: 2, strict: true}
	m := newFlapModel(t, s)
	first := m.startSubscription()()
	updated, _ := m.Update(first)
	m = updated.(model)
	m, _ = stepStream(t, m)
	m.reconnectAttempt = maxReconnectAttempts - 1
	updated, cmd := m.Update(reconnectTickMsg{sessionID: m.sessionID, gen: m.subGen})
	m = updated.(model)
	updated, _ = m.Update(cmd()) // duplicate replay, no durable progress
	m = updated.(model)
	if m.reconnectAttempt != maxReconnectAttempts-1 || m.lastSeq != 1 {
		t.Fatalf("duplicate replay incorrectly reset failure count: attempts=%d seq=%d", m.reconnectAttempt, m.lastSeq)
	}
	m, _ = stepStream(t, m)
	if m.conn != connRetrying || m.reconnectAttempt != 1 || len(m.evs) != 1 {
		t.Fatalf("healthy interval counted as continuous flaps: conn=%v attempts=%d rows=%d", m.conn, m.reconnectAttempt, len(m.evs))
	}
}

func TestSubscriptionTerminalAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		code    connect.Code
		want    connectionState
		reopens bool
	}{
		{connect.CodeUnauthenticated, connAuth, false},
		{connect.CodeNotFound, connNotFound, true},
		{connect.CodeInvalidArgument, connTerminal, false},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			s := &flapServer{code: tc.code, live: make(chan *v1.Event, 1)}
			m := newFlapModel(t, s)
			m.input.SetValue("keep me")
			msg := m.startSubscription()()
			updated, cmd := m.Update(msg)
			m = updated.(model)
			if m.conn != tc.want || cmd != nil || !strings.Contains(m.statusBar(), "ctrl+y") {
				t.Fatalf("terminal state=%v command=%v bar=%s", m.conn, cmd, m.statusBar())
			}
			updated, cmd = m.Update(keyMsg("ctrl+y"))
			m = updated.(model)
			if tc.reopens {
				updated, cmd = m.Update(cmd())
				m = updated.(model)
				s.mu.Lock()
				resumes := s.resumes
				s.mu.Unlock()
				if resumes != 1 {
					t.Fatalf("recovery did not reopen: resumes=%d", resumes)
				}
			} else {
				s.mu.Lock()
				s.code = 0
				s.mu.Unlock()
			}
			if m.input.Value() != "keep me" {
				t.Fatal("recovery discarded draft")
			}
			if cmd == nil {
				t.Fatal("recovery did not resubscribe")
			}
			updated, _ = m.Update(cmd())
			m = updated.(model)
			if m.conn != connLive || m.lastSeq != 1 || len(m.evs) != 1 {
				t.Fatalf("recovery failed: conn=%v cursor=%d rows=%d", m.conn, m.lastSeq, len(m.evs))
			}
		})
	}
}

func TestSubscriptionCancelAndStaleGeneration(t *testing.T) {
	m := readyStreamModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	m.sessionCtx, m.sessionCancel = ctx, cancel
	m.sessionID, m.subGen, m.conn = "s", 4, connRetrying
	m.sub = &subscription{events: make(chan *v1.Event, 1)}
	m.sub.events <- &v1.Event{Seq: 1}
	m.applyLiveEvent(&v1.Event{Seq: 2, Type: "user_input"})
	m.cancelSubscription()
	if ctx.Err() == nil {
		t.Fatal("subscription context not canceled")
	}
	oldSeq := m.lastSeq
	for _, msg := range []tea.Msg{
		sessionEvMsg{sessionID: "s", gen: 4, ev: &v1.Event{Seq: 3}},
		streamEndMsg{sessionID: "s", gen: 4, err: errors.New("drop")},
		reconnectTickMsg{sessionID: "s", gen: 4},
	} {
		updated, cmd := m.Update(msg)
		m = updated.(model)
		if cmd != nil || m.lastSeq != oldSeq {
			t.Fatalf("stale message %T changed state", msg)
		}
	}
}

func TestSubscriptionReplacementCancelsOldStream(t *testing.T) {
	s := &flapServer{live: make(chan *v1.Event)}
	m := newFlapModel(t, s)
	first := m.startSubscription()()
	updated, _ := m.Update(first)
	m = updated.(model)
	old := m.sub
	m.conn = connLost
	updated, cmd := m.Update(keyMsg("ctrl+y"))
	m = updated.(model)
	defer m.cancelSubscription()
	if cmd == nil || m.sub == old {
		t.Fatal("manual retry did not replace subscription")
	}
	closed := make(chan struct{})
	go func() {
		for range old.events {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("replaced stream did not stop")
	}
}

func TestSubscriptionBackoffCancels(t *testing.T) {
	m := readyStreamModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	m.sessionCtx, m.sessionCancel = ctx, cancel
	m.sessionID = "s"
	cmd := m.subscriptionFailed(connect.NewError(connect.CodeUnavailable, errors.New("offline")))
	m.cancelSubscription()
	finished := make(chan tea.Msg, 1)
	go func() { finished <- cmd() }()
	select {
	case msg := <-finished:
		if msg != nil {
			t.Fatalf("canceled timer emitted %T", msg)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("backoff timer continued after session switch")
	}
}

func TestSubscriptionCancelWithFullBuffer(t *testing.T) {
	s := &flapServer{flood: true}
	m := newFlapModel(t, s)
	if _, ok := m.startSubscription()().(sessionEvMsg); !ok {
		t.Fatal("stream did not start")
	}
	sub := m.sub
	deadline := time.After(3 * time.Second)
	for len(sub.events) < cap(sub.events) {
		select {
		case <-deadline:
			t.Fatal("stream never filled its forwarding buffer")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	m.cancelSubscription()
	// Drain the buffer to observe close: the forwarder must have stopped despite
	// having been blocked on sending the next event when we canceled.
	closed := make(chan struct{})
	go func() {
		for range sub.events {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("forwarder stuck after cancellation with full buffer")
	}
}
